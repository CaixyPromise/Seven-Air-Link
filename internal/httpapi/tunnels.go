package httpapi

import (
	"context"
	"net"
	"net/http"
	"seven-air-link/internal/controlws"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"seven-air-link/internal/tunnel"
	"strings"
	"sync/atomic"
	"time"
)

type activeTunnel struct {
	id, forwardID, targetSession string
	service                      model.Service
	actor                        store.Subject
	sourceHash, targetHash       string
	sourceUsed, targetUsed       bool
	source, target               *controlws.Conn
	internal, started            bool
	expires                      time.Time
	timer                        *time.Timer
	ctx                          context.Context
	cancel                       context.CancelFunc
	done                         chan struct{}
	ready                        chan net.Conn
}
type failedTunnel struct {
	actor   store.Subject
	code    string
	expires time.Time
}

func (s *Server) newTunnel(sub store.Subject, serviceID, forwardID string, internal bool) (*activeTunnel, string, error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, "", networkErr("service_unavailable", "server closing")
	}
	v, e := s.serviceForUseLocked(sub, serviceID)
	if e != nil {
		s.mu.Unlock()
		return nil, "", e
	}
	if forwardID != "" {
		f, ok := s.forwards[forwardID]
		if !ok || time.Now().After(f.ExpiresAt) || f.ActorID != sub.ID || f.ActorRole != sub.Role || f.CredentialStamp != sub.CredentialStamp || f.ServiceID != v.ID {
			s.mu.Unlock()
			return nil, "", networkErr("forward_not_found", "forward lease no longer valid")
		}
	}
	byService, byActor := 0, 0
	for _, t := range s.tunnels {
		if t.service.ID == v.ID {
			byService++
		}
		if t.actor == sub {
			byActor++
		}
	}
	if len(s.tunnels) >= s.opt.MaxTunnels || byService >= s.opt.MaxPerService || byActor >= s.opt.MaxPerActor {
		s.mu.Unlock()
		return nil, "", networkErr("quota_exceeded", "concurrent tunnel limit reached")
	}
	sourceTicket, targetTicket := secure.Random("src_"), secure.Random("dst_")
	ctx, cancel := context.WithCancel(s.ctx)
	n := s.nodes[v.OwnerDeviceID]
	t := &activeTunnel{id: secure.Random("tun_")[:28], forwardID: forwardID, targetSession: n.session, service: v, actor: sub, sourceHash: secure.Hash(sourceTicket), targetHash: secure.Hash(targetTicket), internal: internal, expires: time.Now().Add(s.opt.ConnectTimeout), ctx: ctx, cancel: cancel, done: make(chan struct{}), ready: make(chan net.Conn, 1)}
	s.tunnels[t.id] = t
	s.wg.Add(1)
	t.timer = time.AfterFunc(s.opt.ConnectTimeout, func() { s.endTunnel(t.id, "timeout") })
	s.mu.Unlock()
	// No target address may be substituted later: the Node compares this with its local policy.
	e = n.conn.WriteJSON(model.Control{Type: "tunnel.open", TunnelID: t.id, ServiceID: v.ID, Ticket: targetTicket, Target: v.Target, SessionID: n.session, ExpiresAt: t.expires})
	if e != nil {
		s.endTunnel(t.id, "device_offline")
		return nil, "", networkErr("device_offline", "control channel unavailable")
	}
	return t, sourceTicket, nil
}
func (s *Server) endTunnel(id, code string) {
	s.mu.Lock()
	t, ok := s.tunnels[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.tunnels, id)
	t.timer.Stop()
	t.cancel()
	close(t.done)
	if code != "" {
		if len(s.failed) >= 512 {
			for k, v := range s.failed {
				if time.Now().After(v.expires) {
					delete(s.failed, k)
				}
			}
		}
		if len(s.failed) < 512 {
			s.failed[id] = failedTunnel{actor: t.actor, code: code, expires: time.Now().Add(30 * time.Second)}
		}
	}
	conns := []*controlws.Conn{t.source, t.target}
	started := t.started
	s.mu.Unlock()
	for _, c := range conns {
		if c == nil {
			continue
		}
		if !started && code != "" {
			_ = c.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			_ = tunnel.SendReset(c, code)
		}
		_ = c.Close()
	}
	result := "success"
	if code != "" {
		result = "failed"
	}
	s.audit(model.Event{ActorID: t.actor.ID, DeviceID: t.service.OwnerDeviceID, Action: "tunnel.closed", ResourceID: t.service.ID, Result: result, Code: code})
	s.wg.Done()
}
func (s *Server) closeMatching(predicate func(*activeTunnel) bool, code string) {
	s.mu.Lock()
	ids := []string{}
	for id, t := range s.tunnels {
		if predicate(t) {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.endTunnel(id, code)
	}
}
func (s *Server) closeServiceFlows(id, code string) {
	s.closeMatching(func(t *activeTunnel) bool { return t.service.ID == id }, code)
}
func (s *Server) closeForwardFlows(id, code string) {
	s.closeMatching(func(t *activeTunnel) bool { return t.forwardID == id }, code)
}
func (s *Server) closeDeviceFlows(id, code string) {
	s.closeMatching(func(t *activeTunnel) bool {
		return t.service.OwnerDeviceID == id || t.actor.Role == "device" && t.actor.ID == id
	}, code)
}

func (s *Server) dataSocket(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		s.deny(w, r, 403, "access_denied", "data channels require explicit bearer credentials, not browser cookies")
		return
	}
	if r.Header.Get("Origin") != "" {
		s.deny(w, r, 403, "origin_denied", "data channels are not browser endpoints")
		return
	}
	id, ticket := r.Header.Get("X-SLB-Tunnel-ID"), r.Header.Get("X-SLB-Tunnel-Ticket")
	if len(ticket) < 32 || len(ticket) > 160 {
		s.deny(w, r, 403, "invalid_tunnel_ticket", "single-use header ticket required")
		return
	}
	s.mu.Lock()
	t, ok := s.tunnels[id]
	if !ok {
		f, found := s.failed[id]
		s.mu.Unlock()
		if found && f.actor == sub && time.Now().Before(f.expires) {
			s.networkError(w, r, networkErr(f.code, "tunnel establishment failed"))
		} else {
			s.deny(w, r, 410, "tunnel_expired", "unknown or expired tunnel")
		}
		return
	}
	if t.started || time.Now().After(t.expires) {
		s.mu.Unlock()
		s.deny(w, r, 410, "tunnel_expired", "channel already connected or ticket expired")
		return
	}
	// Recheck ACL and target session on every data attach, not just ticket issuance.
	v, e := s.serviceForUseLocked(t.actor, t.service.ID)
	n := s.nodes[t.service.OwnerDeviceID]
	if e != nil || n == nil || n.session != t.targetSession || v.Revision != t.service.Revision {
		s.mu.Unlock()
		s.deny(w, r, 403, "access_denied", "service authorization/session changed")
		s.endTunnel(id, "authorization_changed")
		return
	}
	hash := secure.Hash(ticket)
	target := false
	switch {
	case secure.Equal(hash, t.targetHash) && sub.Role == "device" && sub.ID == t.service.OwnerDeviceID && !t.targetUsed:
		target = true
	case secure.Equal(hash, t.sourceHash) && sub == t.actor && !t.internal && !t.sourceUsed:
	default:
		s.mu.Unlock()
		s.deny(w, r, 403, "invalid_tunnel_ticket", "wrong identity, replayed ticket or invalid side")
		return
	}
	c, e := controlws.UpgradeData(w, r)
	if e != nil {
		s.mu.Unlock()
		return
	}
	if target {
		t.targetUsed = true
		t.target = c
	} else {
		t.sourceUsed = true
		t.source = c
	}
	start := t.target != nil && (t.internal || t.source != nil)
	if start {
		t.started = true
		t.timer.Stop()
	}
	s.mu.Unlock()
	if start {
		go s.startTunnel(t)
	}
	<-t.done
}
func (s *Server) startTunnel(t *activeTunnel) {
	// Both socket pointers become immutable before this goroutine is launched.
	if e := tunnel.SendReady(t.target); e != nil {
		s.endTunnel(t.id, "target_disconnected")
		return
	}
	s.audit(model.Event{ActorID: t.actor.ID, DeviceID: t.service.OwnerDeviceID, Action: "tunnel.opened", ResourceID: t.service.ID, Result: "success"})
	if t.internal {
		stream := tunnel.New(t.target, func() { s.endTunnel(t.id, "") })
		select {
		case t.ready <- stream:
		case <-t.done:
			stream.Close()
		}
		return
	}
	if e := tunnel.SendReady(t.source); e != nil {
		s.endTunnel(t.id, "source_disconnected")
		return
	}
	result := tunnel.Bridge(t.ctx, tunnel.New(t.source, nil), tunnel.New(t.target, nil))
	atomic.AddInt64(&s.bytesUp, result.AtoB)
	atomic.AddInt64(&s.bytesDown, result.BtoA)
	code := ""
	if result.Err != nil {
		code = "stream_error"
	}
	s.endTunnel(t.id, code)
}
func (s *Server) dialService(ctx context.Context, sub store.Subject, id string) (net.Conn, error) {
	t, _, e := s.newTunnel(sub, id, "", true)
	if e != nil {
		return nil, e
	}
	select {
	case c := <-t.ready:
		// Dial's context may be canceled by net/http once Dial returns. Do not
		// bind the lifetime of an established stream to that short-lived context.
		return c, nil
	case <-ctx.Done():
		s.endTunnel(t.id, "request_canceled")
		return nil, ctx.Err()
	case <-t.done:
		s.mu.Lock()
		f := s.failed[t.id]
		s.mu.Unlock()
		code := f.code
		if code == "" {
			code = "service_unavailable"
		}
		return nil, networkErr(code, "target stream unavailable")
	}
}
func (s *Server) networkMaintenance() {
	defer s.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
			now := time.Now()
			ids := []string{}
			s.mu.Lock()
			for id, f := range s.forwards {
				if now.After(f.ExpiresAt) || !s.st.Active(store.Subject{ID: f.ActorID, Role: f.ActorRole, CredentialStamp: f.CredentialStamp}) {
					delete(s.forwards, id)
					ids = append(ids, id)
				}
			}
			for id, f := range s.failed {
				if now.After(f.expires) {
					delete(s.failed, id)
				}
			}
			s.mu.Unlock()
			for _, id := range ids {
				s.closeForwardFlows(id, "forward_lease_expired")
			}
			s.closeMatching(func(t *activeTunnel) bool { return !s.st.Active(t.actor) }, "principal_inactive")
		}
	}
}
