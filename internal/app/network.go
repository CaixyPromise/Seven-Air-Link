package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"seven-air-link/internal/client"
	"seven-air-link/internal/forward"
	"seven-air-link/internal/model"
	"seven-air-link/internal/node"
	"strings"
	"sync"
	"time"
)

func csv(s string) []string {
	out := []string{}
	for _, id := range strings.Split(s, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}
func safeID(s string) bool { return s != "" && !strings.ContainsAny(s, "/\\?# \t\n") }
func (a *application) local(ctx context.Context, method, path string, in, out any) error {
	cfg, e := client.Load(a.opt.config)
	if e != nil {
		return e
	}
	if cfg.Role != "device" {
		return errors.New("share requires a local paired device profile, not the Owner profile")
	}
	_, file := node.LocalPaths(a.opt.config)
	b, e := os.ReadFile(file)
	if e != nil {
		return &model.APIError{Code: "node_not_running", Message: "run node start with this same --config first"}
	}
	var ep node.LocalEndpoint
	if e = json.Unmarshal(b, &ep); e != nil || ep.DeviceID != cfg.SubjectID || !strings.HasPrefix(ep.URL, "http://127.0.0.1:") {
		return errors.New("invalid local Node endpoint")
	}
	var body io.Reader
	if in != nil {
		v, _ := json.Marshal(in)
		body = bytes.NewReader(v)
	}
	req, e := http.NewRequestWithContext(ctx, method, ep.URL+path, body)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Content-Type", "application/json")
	// Never consult HTTP_PROXY for a privileged loopback management request.
	tr := &http.Transport{Proxy: nil}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := hc.Do(req)
	if e != nil {
		return &model.APIError{Code: "node_not_running", Message: "local Node unreachable; start it with the same profile"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return client.DecodeError(resp)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return nil
}
func (a *application) share(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: share tcp|http --name NAME --target 127.0.0.1:PORT [--allow-devices ID,...] [--allow-owner], or share stop NAME")
	}
	if args[0] == "stop" {
		if len(args) != 2 || !safeID(args[1]) {
			return errors.New("usage: share stop SERVICE")
		}
		var out any
		if e := a.local(ctx, "DELETE", "/services/"+args[1], nil, &out); e != nil {
			return e
		}
		return a.print(out)
	}
	if args[0] != "tcp" && args[0] != "http" {
		return errors.New("share protocol must be tcp or http")
	}
	f := a.flags("share " + args[0])
	name := f.String("name", "", "unique service name: lowercase letters, digits, hyphens")
	target := f.String("target", "", "numeric loopback IP:port")
	allowed := f.String("allow-devices", "", "explicit device grants, comma-separated")
	owner := f.Bool("allow-owner", false, "allow the Owner principal to use HTTP proxy/local mapping")
	principals := f.String("allow-principals", "", "explicit Human/Bot use grants, comma-separated")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	var out model.Service
	e := a.local(ctx, "POST", "/share", node.LocalShareRequest{Name: *name, Target: *target, Protocol: args[0], AllowDevices: csv(*allowed), AllowOwner: *owner, AllowPrincipals: csv(*principals)}, &out)
	if e != nil {
		return e
	}
	return a.print(out)
}
func (a *application) services(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: services list|open|grants|stop")
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	if args[0] == "list" {
		if len(args) != 1 {
			return errors.New("services list takes no arguments")
		}
		var out any
		if e = c.Do(ctx, "GET", "/api/v1/services", nil, &out); e != nil {
			return e
		}
		return a.print(out)
	}
	if len(args) < 2 || !safeID(args[1]) {
		return errors.New("service name or ID required")
	}
	key := args[1]
	switch args[0] {
	case "open":
		if len(args) != 2 {
			return errors.New("usage: services open SERVICE")
		}
		var out any
		e = c.Do(ctx, "POST", "/api/v1/services/"+key+"/browser-ticket", map[string]any{}, &out)
		if e != nil {
			return e
		}
		return a.print(out)
	case "stop":
		if len(args) != 2 {
			return errors.New("usage: services stop SERVICE")
		}
		var out any
		e = c.Do(ctx, "DELETE", "/api/v1/services/"+key, nil, &out)
		if e != nil {
			return e
		}
		return a.print(out)
	case "grants":
		f := a.flags("services grants")
		devices := f.String("allow-devices", "", "replacement device grant list; omitted clears grants")
		owner := f.Bool("allow-owner", false, "grant Owner use permission")
		principals := f.String("allow-principals", "", "replacement Human/Bot grant list")
		if e = parse(f, args[2:]); e != nil {
			return e
		}
		var svc model.Service
		if e = c.Do(ctx, "GET", "/api/v1/services/"+key, nil, &svc); e != nil {
			return e
		}
		g := model.Grants{AllowDevices: csv(*devices), AllowPrincipals: csv(*principals)}
		if *owner {
			g.AllowPrincipals = append(g.AllowPrincipals, svc.OwnerPrincipalID)
		}
		var out any
		if e = c.Do(ctx, "PATCH", "/api/v1/services/"+svc.ID, g, &out); e != nil {
			return e
		}
		return a.print(out)
	}
	return errors.New("usage: services list|open|grants|stop")
}
func (a *application) proxy(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: proxy open SERVICE --listen 127.0.0.1:PORT | list | close FORWARD_ID")
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("proxy list takes no arguments")
		}
		var out any
		if e = c.Do(ctx, "GET", "/api/v1/forwards", nil, &out); e != nil {
			return e
		}
		return a.print(out)
	case "close":
		if len(args) != 2 || !safeID(args[1]) {
			return errors.New("forward ID required")
		}
		var out any
		if e = c.Do(ctx, "DELETE", "/api/v1/forwards/"+args[1], nil, &out); e != nil {
			return e
		}
		return a.print(out)
	case "open":
		if len(args) < 2 || !safeID(args[1]) {
			return errors.New("service name or ID required")
		}
		f := a.flags("proxy open")
		listen := f.String("listen", "127.0.0.1:0", "local loopback listener; port 0 auto-allocates")
		if e = parse(f, args[2:]); e != nil {
			return e
		}
		var mu sync.Mutex
		return forward.Open(ctx, forward.Options{Config: c.Config, ServiceID: args[1], Listen: *listen,
			OnStart: func(v model.Forward) error { return a.print(v) },
			Log: func(event string, fields map[string]any) {
				mu.Lock()
				defer mu.Unlock()
				fields["event"] = event
				fields["time"] = time.Now().UTC()
				_ = json.NewEncoder(a.err).Encode(fields)
			},
		})
	}
	return fmt.Errorf("unknown proxy operation %q", args[0])
}
