// Package node maintains one outbound authenticated control connection.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"seven-air-link/internal/controlws"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"sync"
	"time"
)

type Options struct {
	PolicyPath, ControlFile string
	MinBackoff, MaxBackoff  time.Duration
	Log                     io.Writer
}

func logEvent(w io.Writer, event string, fields map[string]any) {
	if w == nil {
		return
	}
	fields["event"] = event
	fields["time"] = time.Now().UTC()
	_ = json.NewEncoder(w).Encode(fields)
}
func Run(ctx context.Context, cfg model.Config, opt Options) error {
	if cfg.Role != "device" {
		return errors.New("node start requires a paired device profile, not owner credentials")
	}
	if opt.MinBackoff <= 0 {
		opt.MinBackoff = 500 * time.Millisecond
	}
	if opt.MaxBackoff < opt.MinBackoff {
		opt.MaxBackoff = 8 * time.Second
	}
	tlsConfig, e := secure.PinnedTLS(cfg.Fingerprint)
	if e != nil {
		return e
	}
	rt, e := newRuntime(cfg, opt)
	if e != nil {
		return e
	}
	defer rt.api.Close()
	closeLocal, e := rt.startLocal(ctx)
	if e != nil {
		return e
	}
	defer closeLocal()
	backoff := opt.MinBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}
		began := time.Now()
		c, status, e := controlws.Dial(ctx, cfg.Server, cfg.Token, tlsConfig)
		if e == nil {
			logEvent(opt.Log, "node.connected", map[string]any{"deviceId": cfg.SubjectID})
			e = runSession(ctx, c, rt)
		}
		if ctx.Err() != nil {
			logEvent(opt.Log, "node.stopped", map[string]any{"deviceId": cfg.SubjectID})
			return nil
		}
		var closeErr *controlws.CloseError
		if errors.Is(e, secure.ErrPin) || errors.Is(e, secure.ErrCertificate) {
			return e
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusConflict || errors.As(e, &closeErr) && closeErr.Code == 1008 {
			return &model.APIError{Code: "node_authorization_or_protocol_rejected", Message: fmt.Sprintf("node stopped; re-pair or resolve duplicate/version conflict: %v", e), Retryable: false}
		}
		// A verified stable session resets backoff; flapping connections do not.
		if time.Since(began) > 20*time.Second {
			backoff = opt.MinBackoff
		}
		delay := backoff + time.Duration(rand.Int63n(int64(backoff/4)+1))
		logEvent(opt.Log, "node.reconnecting", map[string]any{"deviceId": cfg.SubjectID, "retryInMs": delay.Milliseconds(), "reason": fmt.Sprint(e)})
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if backoff < opt.MaxBackoff {
			backoff *= 2
			if backoff > opt.MaxBackoff {
				backoff = opt.MaxBackoff
			}
		}
	}
}
func runSession(ctx context.Context, c *controlws.Conn, rt *localRuntime) error {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hello model.Control
	if e := c.ReadJSON(&hello); e != nil {
		return e
	}
	if hello.Type != "welcome" || hello.Protocol != model.Protocol || hello.HeartbeatMS < 50 || hello.TimeoutMS < hello.HeartbeatMS*3 || hello.TimeoutMS > 120000 {
		return &controlws.CloseError{Code: 1008, Reason: "invalid server protocol parameters"}
	}
	interval := time.Duration(hello.HeartbeatMS) * time.Millisecond
	timeout := time.Duration(hello.TimeoutMS) * time.Millisecond
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rt.mu.Lock()
	rt.conn = c
	rt.session = hello.SessionID
	rt.network = hello.NetworkProtocol == 1
	rt.mu.Unlock()
	defer func() {
		rt.mu.Lock()
		if rt.conn == c {
			rt.conn = nil
			rt.session = ""
			rt.network = false
		}
		rt.mu.Unlock()
	}()
	var workers sync.WaitGroup
	defer func() { cancel(); c.Close(); workers.Wait() }()
	if hello.NetworkProtocol == 1 {
		workers.Add(1)
		go func() { defer workers.Done(); rt.restore(ctx) }()
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	defer func() { close(done); c.Close(); wg.Wait() }()
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		seq := int64(1)
		for {
			if e := c.WriteJSON(model.Control{Type: "heartbeat", Seq: seq}); e != nil {
				c.Close()
				return
			}
			seq++
			select {
			case <-done:
				return
			case <-ctx.Done():
				c.Close()
				return
			case <-ticker.C:
			}
		}
	}()
	last := int64(0)
	for {
		_ = c.SetReadDeadline(time.Now().Add(timeout))
		var m model.Control
		if e := c.ReadJSON(&m); e != nil {
			return e
		}
		if m.Type == "tunnel.open" && hello.NetworkProtocol == 1 {
			rt.handleTunnel(ctx, c, hello.SessionID, m, &workers)
			continue
		}
		if m.Type != "heartbeat.ack" || m.Seq <= last {
			return &controlws.CloseError{Code: 1008, Reason: "invalid heartbeat acknowledgement"}
		}
		last = m.Seq
	}
}
