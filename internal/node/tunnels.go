package node

import (
	"context"
	"errors"
	"net"
	"seven-air-link/internal/controlws"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/tunnel"
	"sync"
	"syscall"
	"time"
)

func (rt *localRuntime) handleTunnel(ctx context.Context, c *controlws.Conn, session string, m model.Control, wg *sync.WaitGroup) {
	reject := func(code string) {
		_ = c.WriteJSON(model.Control{Type: "tunnel.failed", TunnelID: m.TunnelID, Code: code})
	}
	if m.SessionID != session || m.TunnelID == "" || len(m.Ticket) < 32 || m.ExpiresAt.IsZero() || time.Now().After(m.ExpiresAt) {
		reject("local_policy_denied")
		return
	}
	rt.mu.Lock()
	if len(rt.flows) >= 128 {
		rt.mu.Unlock()
		reject("quota_exceeded")
		return
	}
	if _, exists := rt.flows[m.TunnelID]; exists {
		rt.mu.Unlock()
		reject("local_policy_denied")
		return
	}
	flowCtx, cancel := context.WithCancel(ctx)
	rt.flows[m.TunnelID] = localFlow{serviceID: m.ServiceID, cancel: cancel}
	wg.Add(1)
	rt.mu.Unlock()
	go func() {
		defer wg.Done()
		defer cancel()
		defer func() { rt.mu.Lock(); delete(rt.flows, m.TunnelID); rt.mu.Unlock() }()
		// Policy lookup can wait for durable registration; never hold up the
		// control reader or heartbeat acknowledgement while it does so.
		if !rt.approved(m.ServiceID, m.Target) || time.Now().After(m.ExpiresAt) {
			reject("local_policy_denied")
			return
		}
		until := m.ExpiresAt
		if max := time.Now().Add(5 * time.Second); max.Before(until) {
			until = max
		}
		dialCtx, stop := context.WithDeadline(flowCtx, until)
		defer stop()
		raw, e := (&net.Dialer{}).DialContext(dialCtx, "tcp", m.Target)
		if e != nil {
			code := "target_unreachable"
			var ne net.Error
			if errors.Is(e, syscall.ECONNREFUSED) {
				code = "target_refused"
			} else if errors.As(e, &ne) && ne.Timeout() {
				code = "timeout"
			}
			reject(code)
			return
		}
		defer raw.Close()
		// Stop/revoke may have arrived during the local connection attempt.
		if !rt.approved(m.ServiceID, m.Target) {
			reject("local_policy_denied")
			return
		}
		tc, e := secure.PinnedTLS(rt.cfg.Fingerprint)
		if e != nil {
			reject("local_policy_denied")
			return
		}
		ws, _, e := controlws.DialData(dialCtx, rt.cfg.Server, rt.cfg.Token, tc, m.TunnelID, m.Ticket)
		if e != nil {
			reject("target_unreachable")
			return
		}
		defer ws.Close()
		onCancel := context.AfterFunc(flowCtx, func() { raw.Close(); ws.Close() })
		defer onCancel()
		if e = tunnel.AwaitReady(ws, until); e != nil {
			return
		}
		tunnel.Bridge(flowCtx, raw, tunnel.New(ws, nil))
	}()
}
