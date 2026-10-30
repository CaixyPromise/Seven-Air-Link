// Package httpapi exposes one authenticated API to the CLI and the embedded UI.
package httpapi

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"seven-air-link/internal/controlws"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"seven-air-link/web"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Heartbeat, Timeout                     time.Duration
	MaxDevices                             int
	ConnectTimeout, ForwardLease           time.Duration
	MaxTunnels, MaxPerService, MaxPerActor int
}
type online struct {
	services    map[string]bool
	conn        *controlws.Conn
	seen        time.Time
	ip, session string
}
type browserTicket struct {
	webRevision int64
	expires     time.Time
	subject     store.Subject
}
type browserSession struct {
	id, method, ip, userAgent string
	created                   time.Time
	webRevision               int64
	subject                   store.Subject
	expires                   time.Time
	csrf                      string
}
type rateWindow struct {
	start time.Time
	n     int
}
type Server struct {
	passwordSlots      chan struct{}
	noteCount          map[string]int
	bootID             string
	st                 *store.Store
	tunnels            map[string]*activeTunnel
	forwards           map[string]model.Forward
	failed             map[string]failedTunnel
	bytesUp, bytesDown int64
	proxyPort          int
	proxyTickets       map[string]proxyGrant
	proxySessions      map[string]proxyGrant
	controlPort        int
	opt                Options
	pin                string
	hosts              map[string]bool
	mu                 sync.Mutex
	nodes              map[string]*online
	closing            bool
	wg                 sync.WaitGroup
	authMu             sync.Mutex
	tickets            map[string]browserTicket
	sessions           map[string]browserSession
	rates              map[string]rateWindow
	sseCount           int
	inboxCount         map[string]int
	activityCount      map[string]int
	uploadSlots        chan struct{}
	ctx                context.Context
	cancel             context.CancelFunc
}

func New(st *store.Store, cert *x509.Certificate, opt Options) (*Server, error) {
	if opt.Heartbeat < 50*time.Millisecond || opt.Timeout < 3*opt.Heartbeat || opt.MaxDevices < 1 || opt.MaxDevices > 256 {
		return nil, errors.New("invalid heartbeat/timeout/device limit")
	}
	if opt.ConnectTimeout <= 0 {
		opt.ConnectTimeout = 5 * time.Second
	}
	if opt.ForwardLease <= 0 {
		opt.ForwardLease = 15 * time.Second
	}
	if opt.MaxTunnels <= 0 {
		opt.MaxTunnels = 256
	}
	if opt.MaxPerService <= 0 {
		opt.MaxPerService = 64
	}
	if opt.MaxPerActor <= 0 {
		opt.MaxPerActor = 128
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{passwordSlots: make(chan struct{}, 2), noteCount: map[string]int{}, st: st, opt: opt, pin: secure.Fingerprint(cert.Raw), hosts: map[string]bool{}, nodes: map[string]*online{}, tickets: map[string]browserTicket{}, inboxCount: map[string]int{}, activityCount: map[string]int{}, uploadSlots: make(chan struct{}, 4), sessions: map[string]browserSession{}, rates: map[string]rateWindow{}, ctx: ctx, cancel: cancel}
	for _, h := range cert.DNSNames {
		s.hosts[strings.ToLower(h)] = true
	}
	for _, ip := range cert.IPAddresses {
		s.hosts[ip.String()] = true
	}
	s.bootID = secure.Random("boot_")[:28]
	s.tunnels = map[string]*activeTunnel{}
	s.forwards = map[string]model.Forward{}
	s.failed = map[string]failedTunnel{}
	s.proxyTickets = map[string]proxyGrant{}
	s.proxySessions = map[string]proxyGrant{}
	s.wg.Add(1)
	go s.networkMaintenance()
	return s, nil
}
func (s *Server) Close() {
	s.cancel()
	s.mu.Lock()
	s.closing = true
	conns := []*controlws.Conn{}
	for _, n := range s.nodes {
		conns = append(conns, n.conn)
	}
	s.mu.Unlock()
	// Hard-close on process shutdown: never wait on an unresponsive peer.
	for _, c := range conns {
		_ = c.Close()
	}
	s.closeMatching(func(*activeTunnel) bool { return true }, "server_shutdown")
	s.wg.Wait()
}
func (s *Server) audit(ev model.Event) {
	if e := s.st.Audit(ev); e != nil {
		log.Printf("audit write failed: %v", e)
	}
}
func (s *Server) deny(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": model.APIError{Code: code, Message: message, RequestID: w.Header().Get("X-Request-ID"), Retryable: status == 429 || status == 503}})
}
func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) body(w http.ResponseWriter, r *http.Request, v any) bool {
	mt, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || mt != "application/json" {
		s.deny(w, r, 415, "unsupported_media_type", "application/json is required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e = d.Decode(v); e == nil {
		var extra any
		e = d.Decode(&extra)
		if e == io.EOF {
			return true
		}
		if e == nil {
			e = errors.New("multiple JSON values")
		}
	}
	var max *http.MaxBytesError
	if errors.As(e, &max) {
		s.deny(w, r, 413, "body_too_large", "JSON request exceeds 8 KiB")
	} else {
		s.deny(w, r, 400, "invalid_request", "malformed JSON or unknown field")
	}
	return false
}
func hostName(host string) string {
	h, _, e := net.SplitHostPort(host)
	if e == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(host)
}
func remoteIP(r *http.Request) string {
	h, _, e := net.SplitHostPort(r.RemoteAddr)
	if e == nil {
		return h
	}
	return r.RemoteAddr
}
func (s *Server) allowedRate(ip string) bool {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	now := time.Now()
	for k, v := range s.rates {
		if now.Sub(v.start) > time.Minute {
			delete(s.rates, k)
		}
	}
	v, ok := s.rates[ip]
	if !ok {
		if len(s.rates) >= 1024 {
			return false
		}
		v = rateWindow{start: now}
	}
	v.n++
	s.rates[ip] = v
	return v.n <= 60
}
func (s *Server) auth(r *http.Request) (store.Subject, string, error) {
	a := r.Header.Get("Authorization")
	if a != "" {
		if !strings.HasPrefix(a, "Bearer ") {
			return store.Subject{}, "", store.ErrAuth
		}
		sub, e := s.st.Auth(strings.TrimPrefix(a, "Bearer "))
		return sub, "", e
	}
	c, e := r.Cookie("slb_session")
	if e != nil {
		return store.Subject{}, "", store.ErrAuth
	}
	s.authMu.Lock()
	bs, ok := s.sessions[secure.Hash(c.Value)]
	if ok && time.Now().After(bs.expires) {
		delete(s.sessions, secure.Hash(c.Value))
		ok = false
	}
	s.authMu.Unlock()
	if !ok {
		return store.Subject{}, "", store.ErrAuth
	}
	sub := bs.subject
	if !s.st.Active(sub) || bs.webRevision != s.ownerWebRevision(sub) {
		return store.Subject{}, "", store.ErrPrincipalInactive
	}
	return sub, bs.csrf, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqID := secure.Random("req_")[:28]
	w.Header().Set("X-Request-ID", reqID)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if !s.hosts[hostName(r.Host)] {
		s.deny(w, r, 421, "untrusted_host", "Host is not in this server certificate; add it using init --tls-host before initialization")
		return
	}
	if s.proxyPort > 0 {
		csp := w.Header().Get("Content-Security-Policy")
		w.Header().Set("Content-Security-Policy", strings.Replace(csp, "form-action 'self'", "form-action 'self' https://"+net.JoinHostPort(hostName(r.Host), strconv.Itoa(s.proxyPort)), 1))
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, e := url.Parse(o)
		if e != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, r.Host) || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			s.deny(w, r, 403, "origin_denied", "cross-origin access is denied")
			return
		}
	}
	if r.URL.RawQuery != "" {
		q := r.URL.Query()
		for _, k := range []string{"token", "authorization", "code", "ticket", "password"} {
			if q.Has(k) {
				s.deny(w, r, 400, "credential_in_url", "credentials are not accepted in URLs")
				return
			}
		}
	}
	path := r.URL.Path
	if r.Method == "GET" {
		switch path {
		case "/api/v1/auth/methods":
			jsonReply(w, 200, map[string]any{"password": s.st.WebPasswordStatus().Enabled, "ticket": true, "passwordIdentity": "owner"})
			return
		case "/healthz":
			jsonReply(w, 200, map[string]any{"ok": true})
			return
		case "/readyz":
			if e := s.st.Ready(); e != nil {
				s.deny(w, r, 503, "not_ready", "metadata storage is unavailable")
			} else {
				jsonReply(w, 200, map[string]any{"ready": true})
			}
			return
		case "/api/v1/info":
			jsonReply(w, 200, map[string]any{"name": "Seven-Air-Link", "version": model.Version, "releaseChannel": "rc", "proxyPort": s.proxyPort, "bootId": s.bootID, "protocol": model.Protocol, "fingerprint": s.pin})
			return
		case "/", "/index.html", "/app.js", "/collaboration.js", "/maintenance.js", "/notes.js", "/auth.js", "/style.css":
			name := strings.TrimPrefix(path, "/")
			if name == "" {
				name = "index.html"
			}
			b, e := web.Assets.ReadFile(name)
			if e != nil {
				s.deny(w, r, 404, "not_found", "asset not found")
				return
			}
			ct := "text/html; charset=utf-8"
			if strings.HasSuffix(name, ".js") {
				ct = "text/javascript; charset=utf-8"
			}
			if strings.HasSuffix(name, ".css") {
				ct = "text/css; charset=utf-8"
			}
			w.Header().Set("Content-Type", ct)
			_, _ = w.Write(b)
			return
		case "/favicon.ico":
			w.WriteHeader(204)
			return
		}
	}
	if r.Method != "GET" && path != "/api/v1/note" && path != "/api/v1/tunnels" && path != "/api/v1/messages" && path != "/api/v1/files" && !strings.HasPrefix(path, "/api/v1/forwards") && !s.allowedRate(remoteIP(r)) {
		w.Header().Set("Retry-After", "60")
		s.deny(w, r, 429, "rate_limited", "too many mutations or login attempts from this address")
		return
	}
	if path == "/api/v1/identity/claim" && r.Method == "POST" {
		s.claim(w, r)
		return
	}
	if path == "/api/v1/join" && r.Method == "POST" {
		s.join(w, r)
		return
	}
	if path == "/api/v1/sessions/password" && r.Method == "POST" {
		s.loginPassword(w, r)
		return
	}
	if path == "/api/v1/sessions" && r.Method == "POST" {
		s.login(w, r)
		return
	}
	sub, csrf, e := s.auth(r)
	if e != nil {
		code := "unauthenticated"
		status := 401
		if errors.Is(e, store.ErrPrincipalInactive) {
			code = "principal_inactive"
			status = 403
		}
		if errors.Is(e, store.ErrRevoked) {
			code = "device_revoked"
			status = 403
		}
		if s.allowedRate("auth:" + remoteIP(r)) {
			s.audit(model.Event{ActorID: "anonymous", Action: "auth.denied", Result: "denied", Code: code, RequestID: reqID})
		}
		s.deny(w, r, status, code, "a valid credential or browser session is required")
		return
	}
	if csrf != "" && r.Method != "GET" && !secure.Equal(r.Header.Get("X-SLB-CSRF"), csrf) {
		s.audit(model.Event{ActorID: sub.ID, Action: "auth.csrf_denied", Result: "denied", RequestID: reqID})
		s.deny(w, r, 403, "csrf_denied", "missing or invalid CSRF header")
		return
	}
	if path == "/api/v1/me" && r.Method == "GET" {
		p, _ := s.st.Principal(sub.ID)
		jsonReply(w, 200, map[string]any{"id": sub.ID, "role": sub.Role, "csrf": csrf, "principal": p})
		return
	}
	if path == "/api/v1/status" && r.Method == "GET" {
		ds := s.devices(sub)
		online := 0
		for _, d := range ds {
			if d.Status == "online" {
				online++
			}
		}
		jsonReply(w, 200, map[string]any{"version": model.Version, "protocol": model.Protocol, "role": sub.Role, "subjectId": sub.ID, "onlineDevices": online, "devices": ds, "heartbeatMs": s.opt.Heartbeat.Milliseconds(), "timeoutMs": s.opt.Timeout.Milliseconds()})
		return
	}
	if path == "/api/v1/storage" && r.Method == "GET" {
		if sub.Role != "owner" {
			s.deny(w, r, 403, "access_denied", "Owner required for storage diagnostics")
			return
		}
		stats, err := s.st.Stats()
		if err != nil {
			s.deny(w, r, 503, "storage_unavailable", "database check failed")
			return
		}
		jsonReply(w, 200, stats)
		return
	}
	if path == "/api/v1/devices" && r.Method == "GET" {
		jsonReply(w, 200, map[string]any{"devices": s.devices(sub)})
		return
	}
	if path == "/api/v1/node/control" && r.Method == "GET" {
		if sub.Role != "device" || csrf != "" {
			s.deny(w, r, 403, "access_denied", "the control channel requires an independent device credential")
			return
		}
		s.control(w, r, sub)
		return
	}
	if s.authSettingsRoute(w, r, sub) {
		return
	}
	if s.noteRoute(w, r, sub) {
		return
	}
	if s.collaborationRoute(w, r, sub) {
		return
	}
	if s.networkRoute(w, r, sub) {
		return
	}
	if sub.Role != "owner" {
		s.audit(model.Event{ActorID: sub.ID, DeviceID: sub.ID, Action: "authorization.denied", Result: "denied", RequestID: reqID})
		s.deny(w, r, 403, "access_denied", "owner authorization required")
		return
	}
	switch {
	case path == "/api/v1/pairings" && r.Method == "POST":
		var q model.PairingRequest
		if !s.body(w, r, &q) {
			return
		}
		if q.TTLSeconds < 1 || q.TTLSeconds > 900 {
			s.deny(w, r, 400, "invalid_ttl", "ttlSeconds must be 1..900")
			return
		}
		p, e := s.st.Pair(time.Duration(q.TTLSeconds)*time.Second, reqID)
		if e != nil {
			s.deny(w, r, 503, "storage_unavailable", "could not commit pairing")
			return
		}
		jsonReply(w, 201, p)
	case strings.HasPrefix(path, "/api/v1/devices/") && r.Method == "DELETE":
		id := strings.TrimPrefix(path, "/api/v1/devices/")
		s.mu.Lock()
		e := s.st.Revoke(id, reqID)
		var conn *controlws.Conn
		if e == nil {
			if n := s.nodes[id]; n != nil {
				conn = n.conn
				delete(s.nodes, id)
			}
		}
		s.mu.Unlock()
		if errors.Is(e, os.ErrNotExist) {
			s.deny(w, r, 404, "device_not_found", "unknown device")
			return
		}
		if e != nil {
			s.deny(w, r, 503, "storage_unavailable", "revocation was NOT committed")
			return
		}
		s.closeDeviceFlows(id, "device_revoked")
		for _, p := range s.st.Principals() {
			if p.DeviceID == id {
				s.invalidatePrincipal(p.ID)
			}
		}
		if conn != nil {
			conn.Shutdown(1008, "device_revoked")
		}
		jsonReply(w, 200, map[string]bool{"revoked": true})
	case strings.HasPrefix(path, "/api/v1/devices/") && r.Method == "PATCH":
		var q struct {
			Name string `json:"name"`
		}
		if !s.body(w, r, &q) {
			return
		}
		if !store.ValidName(q.Name) {
			s.deny(w, r, 400, "invalid_name", "name must be 1..80 characters without control characters")
			return
		}
		e := s.st.Rename(strings.TrimPrefix(path, "/api/v1/devices/"), q.Name, reqID)
		if errors.Is(e, os.ErrNotExist) {
			s.deny(w, r, 404, "device_not_found", "unknown device")
			return
		}
		if e != nil {
			s.deny(w, r, 503, "storage_unavailable", "rename not committed")
			return
		}
		jsonReply(w, 200, map[string]bool{"ok": true})
	case path == "/api/v1/browser-tickets" && r.Method == "POST":
		s.createTicket(w, r, sub)
	case path == "/api/v1/sessions" && r.Method == "DELETE":
		if c, e := r.Cookie("slb_session"); e == nil {
			s.authMu.Lock()
			delete(s.sessions, secure.Hash(c.Value))
			s.authMu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "slb_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
		jsonReply(w, 200, map[string]bool{"ok": true})
	case path == "/api/v1/activity" && r.Method == "GET":
		s.activityFor(w, r, sub)
	case path == "/api/v1/events" && r.Method == "GET":
		s.eventsFor(w, r, sub)
	case strings.HasPrefix(path, "/api/v1/services") || strings.HasPrefix(path, "/api/v1/forwards") || strings.HasPrefix(path, "/api/v1/messages") || strings.HasPrefix(path, "/api/v1/bots") || strings.HasPrefix(path, "/api/v1/files"):
		s.deny(w, r, 501, "milestone_not_implemented", "this operation is not implemented")
	default:
		s.deny(w, r, 404, "not_found", "unknown endpoint or unsupported method")
	}
}
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	var q model.JoinRequest
	if !s.body(w, r, &q) {
		return
	}
	if q.Protocol != model.Protocol {
		s.deny(w, r, 409, "version_unsupported", "supported control protocol: 1")
		return
	}
	if !store.ValidName(q.Name) || len(q.OS) > 32 || len(q.Arch) > 32 || len(q.NodeVersion) > 64 {
		s.deny(w, r, 400, "invalid_request", "invalid device metadata")
		return
	}
	out, e := s.st.Join(q, s.opt.MaxDevices, w.Header().Get("X-Request-ID"))
	if e != nil {
		if errors.Is(e, store.ErrPairing) {
			s.audit(model.Event{ActorID: "anonymous", Action: "pairing.denied", Result: "denied", Code: "pairing_invalid_or_expired", RequestID: w.Header().Get("X-Request-ID")})
			s.deny(w, r, 403, "pairing_invalid_or_expired", "pairing code is invalid, consumed or expired")
		} else {
			s.deny(w, r, 503, "join_unavailable", "device quota reached or metadata commit failed")
		}
		return
	}
	jsonReply(w, 201, out)
}
func (s *Server) devices(sub store.Subject) []model.Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	ds := s.st.Devices()
	out := []model.Device{}
	for _, d := range ds {
		if sub.Role == "member" {
			continue
		}
		if sub.Role == "device" && d.ID != sub.ID {
			continue
		}
		d.Status = "offline"
		d.SessionID = ""
		if d.RevokedAt != nil {
			d.Status = "revoked"
		} else if n := s.nodes[d.ID]; n != nil {
			d.Status = "online"
			t := n.seen
			d.LastSeen = &t
			d.IP = n.ip
			d.SessionID = n.session
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
func (s *Server) control(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	if r.Header.Get("Origin") != "" {
		s.deny(w, r, 403, "origin_denied", "browser-origin control sockets are forbidden")
		return
	}
	if r.Header.Get("X-Bridge-Protocol") != "1" {
		s.deny(w, r, 409, "version_unsupported", "X-Bridge-Protocol: 1 required")
		return
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		s.deny(w, r, 503, "shutting_down", "server is shutting down")
		return
	}
	// Re-check under the same lock used by revocation: no authorization/attach race.
	d, ok := s.st.Device(sub.ID)
	if !ok || d.RevokedAt != nil {
		s.mu.Unlock()
		s.deny(w, r, 403, "device_revoked", "device is no longer trusted")
		return
	}
	if s.nodes[sub.ID] != nil {
		s.mu.Unlock()
		s.deny(w, r, 409, "device_already_connected", "a node using this credential is already online")
		return
	}
	c, e := controlws.Upgrade(w, r)
	if e != nil {
		s.mu.Unlock()
		return
	}
	n := &online{services: map[string]bool{}, conn: c, seen: time.Now().UTC(), ip: remoteIP(r), session: secure.Random("ses_")[:28]}
	if e = s.st.Presence(sub.ID, "device.connected", n.seen, n.ip); e != nil {
		s.mu.Unlock()
		c.Shutdown(1011, "storage_unavailable")
		return
	}
	s.nodes[sub.ID] = n
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	defer c.Close()
	defer func() {
		s.mu.Lock()
		if s.nodes[sub.ID] == n {
			delete(s.nodes, sub.ID)
			if e := s.st.Presence(sub.ID, "device.disconnected", n.seen, n.ip); e != nil {
				log.Printf("disconnect audit failed: %v", e)
			}
		}
		s.mu.Unlock()
		s.closeMatching(func(t *activeTunnel) bool { return t.service.OwnerDeviceID == sub.ID && t.targetSession == n.session }, "device_offline")
	}()
	if e = c.WriteJSON(model.Control{Type: "welcome", Protocol: model.Protocol, NetworkProtocol: 1, SessionID: n.session, HeartbeatMS: s.opt.Heartbeat.Milliseconds(), TimeoutMS: s.opt.Timeout.Milliseconds()}); e != nil {
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(s.opt.Timeout))
	lastSeq := int64(0)
	for {
		var m model.Control
		if e = c.ReadJSON(&m); e != nil {
			return
		}
		if m.Type == "services.ready" {
			if len(m.ServiceIDs) > 64 {
				c.Shutdown(1008, "service_limit")
				return
			}
			s.mu.Lock()
			n.services = map[string]bool{}
			for _, id := range m.ServiceIDs {
				if v, ok := s.st.Service(id); ok && v.OwnerDeviceID == sub.ID && !v.Disabled {
					n.services[id] = true
				}
			}
			s.mu.Unlock()
			continue
		}
		if m.Type == "tunnel.failed" {
			s.mu.Lock()
			t := s.tunnels[m.TunnelID]
			valid := t != nil && t.service.OwnerDeviceID == sub.ID && t.targetSession == n.session
			s.mu.Unlock()
			if valid {
				code := m.Code
				switch code {
				case "target_refused", "timeout", "local_policy_denied", "target_unreachable", "quota_exceeded":
				default:
					code = "target_unreachable"
				}
				s.endTunnel(m.TunnelID, code)
			}
			continue
		}
		if m.Type != "heartbeat" || m.Seq <= lastSeq {
			c.Shutdown(1008, "invalid_control_message")
			return
		}
		lastSeq = m.Seq
		s.mu.Lock()
		if s.nodes[sub.ID] != n {
			s.mu.Unlock()
			return
		}
		n.seen = time.Now().UTC()
		seen := n.seen
		s.mu.Unlock()
		_ = c.SetReadDeadline(time.Now().Add(s.opt.Timeout))
		if e = c.WriteJSON(model.Control{Type: "heartbeat.ack", Seq: m.Seq, ServerTime: seen}); e != nil {
			return
		}
	}
}
func (s *Server) createTicket(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	if !s.st.Active(sub) {
		s.collaborationError(w, r, store.ErrPrincipalInactive)
		return
	}
	ticket := secure.Random("web_")
	exp := time.Now().Add(2 * time.Minute)
	s.authMu.Lock()
	for k, t := range s.tickets {
		if time.Now().After(t.expires) {
			delete(s.tickets, k)
		}
	}
	if len(s.tickets) >= 64 {
		s.authMu.Unlock()
		s.deny(w, r, 429, "ticket_quota", "too many pending browser tickets")
		return
	}
	s.tickets[secure.Hash(ticket)] = browserTicket{expires: exp, subject: sub, webRevision: s.ownerWebRevision(sub)}
	s.authMu.Unlock()
	s.audit(model.Event{ActorID: sub.ID, Action: "browser.ticket_created", Result: "success", RequestID: w.Header().Get("X-Request-ID")})
	jsonReply(w, 201, map[string]any{"ticket": ticket, "expiresAt": exp, "note": "single-use browser login; do not place it in a URL"})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Ticket string `json:"ticket"`
	}
	if !s.body(w, r, &q) {
		return
	}
	s.authMu.Lock()
	t, ok := s.tickets[secure.Hash(q.Ticket)]
	if !ok || time.Now().After(t.expires) || !s.st.Active(t.subject) || t.webRevision != s.ownerWebRevision(t.subject) {
		s.authMu.Unlock()
		s.deny(w, r, 403, "ticket_invalid_or_expired", "create a new one-time ticket using login create")
		return
	}
	token, csrf, ok := s.issueSessionLocked(t.subject, t.webRevision, "ticket", r)
	if ok {
		delete(s.tickets, secure.Hash(q.Ticket))
	}
	s.authMu.Unlock()
	if !ok {
		s.deny(w, r, 429, "session_quota", "too many active browser sessions")
		return
	}
	s.replySession(w, r, t.subject, token, csrf)
}
func (s *Server) activityFor(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	limit := 100
	if x := r.URL.Query().Get("limit"); x != "" {
		n, e := strconv.Atoi(x)
		if e != nil || n < 1 || n > 500 {
			s.deny(w, r, 400, "invalid_limit", "limit must be 1..500")
			return
		}
		limit = n
	}
	if x := r.URL.Query().Get("after"); x != "" {
		after, e := strconv.ParseInt(x, 10, 64)
		if e != nil {
			s.deny(w, r, 400, "invalid_cursor", "after must be a sequence number")
			return
		}
		ev, seq, e := s.st.Events(after, limit)
		if e != nil {
			s.deny(w, r, 409, "cursor_expired", "refresh the activity snapshot")
			return
		}
		next := after
		if len(ev) > 0 {
			next = ev[len(ev)-1].Seq
		}
		jsonReply(w, 200, map[string]any{"events": visibleActivity(ev, sub, r), "nextCursor": next, "snapshotSeq": seq})
		return
	}
	ev := visibleActivity(s.st.Recent(store.MaxEvents), sub, r)
	if len(ev) > limit {
		ev = ev[len(ev)-limit:]
	}
	next := int64(0)
	if len(ev) > 0 {
		next = ev[len(ev)-1].Seq
	}
	jsonReply(w, 200, map[string]any{"events": visibleActivity(ev, sub, r), "nextCursor": next})
}
func (s *Server) eventsFor(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	cursor := int64(0)
	x := r.Header.Get("Last-Event-ID")
	if x == "" {
		x = r.URL.Query().Get("cursor")
	}
	if x != "" {
		v, e := strconv.ParseInt(x, 10, 64)
		if e != nil || v < 0 {
			s.deny(w, r, 400, "invalid_cursor", "invalid event cursor")
			return
		}
		cursor = v
	}
	if _, _, e := s.st.Events(cursor, 1); e != nil {
		s.deny(w, r, 409, "cursor_expired", "refresh the activity snapshot")
		return
	}
	s.authMu.Lock()
	if s.sseCount >= 64 || s.activityCount[sub.ID] >= 4 {
		s.authMu.Unlock()
		s.deny(w, r, 429, "stream_quota", "too many event streams")
		return
	}
	s.sseCount++
	s.activityCount[sub.ID]++
	s.authMu.Unlock()
	defer func() {
		s.authMu.Lock()
		s.sseCount--
		s.activityCount[sub.ID]--
		if s.activityCount[sub.ID] == 0 {
			delete(s.activityCount, sub.ID)
		}
		s.authMu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	flush := func(b string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, e := io.WriteString(w, b); e != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !flush("retry: 1000\nevent: ready\ndata: {}\n\n") {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	lastBeat := time.Now()
	lastSent := cursor
	for {
		if _, _, e := s.auth(r); e != nil {
			flush("event: session.expired\ndata: {}\n\n")
			return
		}
		ev, _, e := s.st.Events(cursor, 500)
		if e != nil {
			flush("event: resync\ndata: {}\n\n")
			return
		}
		for _, item := range ev {
			cursor = item.Seq
			if len(visibleActivity([]model.Event{item}, sub, r)) == 0 {
				continue
			}
			b, _ := json.Marshal(item)
			if !flush(fmt.Sprintf("id: %d\nevent: activity\ndata: %s\n\n", item.Seq, b)) {
				return
			}
			cursor = item.Seq
			lastSent = cursor
		}
		if cursor > lastSent {
			if !flush(fmt.Sprintf("id: %d\nevent: checkpoint\ndata: {}\n\n", cursor)) {
				return
			}
			lastSent = cursor
		}
		if time.Since(lastBeat) > 10*time.Second {
			if !flush(": heartbeat\n\n") {
				return
			}
			lastBeat = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
