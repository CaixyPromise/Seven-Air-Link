// Package forward owns a foreground local listener and a renewable server lease.
// Server restart recreates the lease, never silently resumes old TCP byte streams.
package forward

import (
	"context"
	"errors"
	"io"
	"net"
	"seven-air-link/internal/client"
	"seven-air-link/internal/controlws"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"seven-air-link/internal/tunnel"
	"sync"
	"time"
)

type Options struct {
	Config            model.Config
	ServiceID, Listen string
	OnStart           func(model.Forward) error
	Log               func(string, map[string]any)
}
type runner struct {
	c         *client.Client
	opt       Options
	mu        sync.RWMutex
	lease     model.Forward
	boot      string
	available bool
}

func (r *runner) log(event string, fields map[string]any) {
	if r.opt.Log != nil {
		r.opt.Log(event, fields)
	}
}
func (r *runner) epoch(ctx context.Context) (string, error) {
	var info struct {
		BootID string `json:"bootId"`
	}
	e := r.c.Do(ctx, "GET", "/api/v1/info", nil, &info)
	return info.BootID, e
}
func (r *runner) create(ctx context.Context, listen string) error {
	boot, e := r.epoch(ctx)
	if e != nil {
		return e
	}
	var f model.Forward
	e = r.c.Do(ctx, "POST", "/api/v1/forwards", map[string]string{"serviceId": r.opt.ServiceID, "listen": listen}, &f)
	if e != nil {
		return e
	}
	r.mu.Lock()
	r.lease = f
	r.boot = boot
	r.available = true
	r.mu.Unlock()
	r.log("forward.ready", map[string]any{"forwardId": f.ID, "listen": f.Listen, "serviceId": f.ServiceID})
	return nil
}
func Open(ctx context.Context, opt Options) error {
	addr, e := store.Loopback(opt.Listen, true)
	if e != nil {
		return &model.APIError{Code: "unsafe_listen", Message: e.Error()}
	}
	listener, e := net.Listen("tcp", addr)
	if e != nil {
		return &model.APIError{Code: "listen_port_in_use", Message: e.Error()}
	}
	defer listener.Close()
	c, e := client.New(opt.Config)
	if e != nil {
		return e
	}
	defer c.Close()
	r := &runner{c: c, opt: opt}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if e = r.create(ctx, listener.Addr().String()); e != nil {
		return e
	}
	if opt.OnStart != nil {
		if e = opt.OnStart(r.lease); e != nil {
			return e
		}
	}
	defer func() {
		r.mu.RLock()
		id := r.lease.ID
		r.mu.RUnlock()
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = r.c.Do(cleanup, "DELETE", "/api/v1/forwards/"+id, nil, nil)
	}()
	stopped := make(chan error, 1)
	go func() { e := r.renew(ctx, listener.Addr().String()); stopped <- e; cancel(); listener.Close() }()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 128)
	closeOnCancel := context.AfterFunc(ctx, func() { listener.Close() })
	defer closeOnCancel()
	for {
		conn, e := listener.Accept()
		if e != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		default:
			conn.Close()
			r.log("forward.rejected", map[string]any{"code": "local_connection_limit"})
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer conn.Close()
			r.mu.RLock()
			f, available := r.lease, r.available
			r.mu.RUnlock()
			if !available {
				r.log("forward.connection_failed", map[string]any{"code": "server_unavailable"})
				return
			}
			dialCtx, stop := context.WithTimeout(ctx, 6*time.Second)
			stream, e := Dial(dialCtx, r.c, f.ID)
			stop()
			if e != nil {
				r.log("forward.connection_failed", map[string]any{"error": e.Error(), "forwardId": f.ID})
				return
			}
			result := tunnel.Bridge(ctx, conn, stream)
			if result.Err != nil && ctx.Err() == nil {
				r.log("forward.stream_closed", map[string]any{"forwardId": f.ID, "bytesSent": result.AtoB, "bytesReceived": result.BtoA, "code": "stream_error"})
			}
		}()
	}
	cancel()
	wg.Wait()
	e = <-stopped
	if errors.Is(e, context.Canceled) {
		return nil
	}
	return e
}
func (r *runner) renew(ctx context.Context, listen string) error {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		r.mu.RLock()
		f, boot := r.lease, r.boot
		r.mu.RUnlock()
		var result any
		e := r.c.Do(ctx, "PATCH", "/api/v1/forwards/"+f.ID, nil, &result)
		if e == nil {
			r.mu.Lock()
			r.available = true
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		r.available = false
		r.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(e, secure.ErrPin) || errors.Is(e, secure.ErrCertificate) {
			return e
		}
		var ae *model.APIError
		if errors.As(e, &ae) {
			if ae.Code == "connection_failed" && !ae.Retryable {
				return e
			}
			if ae.Code == "access_denied" || ae.Code == "device_revoked" || ae.Code == "principal_inactive" || ae.Code == "unauthenticated" {
				return e
			}
			if ae.Code == "forward_not_found" {
				current, err := r.epoch(ctx)
				if err != nil {
					continue
				}
				if current == boot {
					return &model.APIError{Code: "forward_closed", Message: "forward was stopped or its lease expired on the same server instance"}
				}
				if e = r.create(ctx, listen); e == nil {
					continue
				}
				// A target Node may reconnect slightly later than the Server.
				if errors.As(e, &ae) && (ae.Code == "access_denied" || ae.Code == "principal_inactive" || ae.Code == "device_revoked" || ae.Code == "unauthenticated" || ae.Code == "service_stopped") {
					return e
				}
			}
		}
		r.log("forward.reconnecting", map[string]any{"forwardId": f.ID, "error": e.Error()})
	}
}

// Dial consumes a short-lived, identity-bound one-use ticket on a dedicated WSS.
func Dial(ctx context.Context, c *client.Client, forwardID string) (net.Conn, error) {
	var t model.TunnelTicket
	if e := c.Do(ctx, "POST", "/api/v1/tunnels", model.TunnelRequest{ForwardID: forwardID}, &t); e != nil {
		return nil, e
	}
	tc, e := secure.PinnedTLS(c.Config.Fingerprint)
	if e != nil {
		return nil, e
	}
	ws, _, e := controlws.DialData(ctx, c.Config.Server, c.Config.Token, tc, t.TunnelID, t.Ticket)
	if e != nil {
		return nil, e
	}
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	e = tunnel.AwaitReady(ws, t.ExpiresAt)
	if !stop() && ctx.Err() != nil {
		ws.Close()
		return nil, ctx.Err()
	}
	if e != nil {
		ws.Close()
		return nil, e
	}
	return tunnel.New(ws, nil), nil
}

var _ io.ReadWriteCloser = (*tunnel.Conn)(nil)
