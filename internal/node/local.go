package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"seven-air-link/internal/client"
	"seven-air-link/internal/controlws"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"strings"
	"sync"
	"time"
)

type LocalShareRequest struct {
	Name            string   `json:"name"`
	Protocol        string   `json:"protocol"`
	Target          string   `json:"target"`
	AllowDevices    []string `json:"allowDevices"`
	AllowOwner      bool     `json:"allowOwner"`
	AllowPrincipals []string `json:"allowPrincipals"`
}
type LocalEndpoint struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	DeviceID string `json:"deviceId"`
}
type Approval struct {
	Service  model.ServiceCreate `json:"service"`
	Disabled bool                `json:"disabled"`
}
type Policy struct {
	Version  int                 `json:"version"`
	DeviceID string              `json:"deviceId"`
	Services map[string]Approval `json:"services"`
}
type localRuntime struct {
	cfg     model.Config
	opt     Options
	api     *client.Client
	ops     sync.Mutex
	policy  Policy
	mu      sync.Mutex
	conn    *controlws.Conn
	session string
	network bool
	flows   map[string]localFlow
}
type localFlow struct {
	serviceID string
	cancel    context.CancelFunc
}

func newRuntime(cfg model.Config, opt Options) (*localRuntime, error) {
	api, e := client.New(cfg)
	if e != nil {
		return nil, e
	}
	rt := &localRuntime{cfg: cfg, opt: opt, api: api, policy: Policy{Version: 1, DeviceID: cfg.SubjectID, Services: map[string]Approval{}}, flows: map[string]localFlow{}}
	if opt.PolicyPath != "" {
		b, e := os.ReadFile(opt.PolicyPath)
		if e != nil && !os.IsNotExist(e) {
			api.Close()
			return nil, e
		}
		if e == nil {
			if e = json.Unmarshal(b, &rt.policy); e != nil || rt.policy.Version != 1 || rt.policy.DeviceID != cfg.SubjectID || rt.policy.Services == nil {
				api.Close()
				return nil, errors.New("invalid local service approval policy; refusing to replace it")
			}
			for id, v := range rt.policy.Services {
				if id != v.Service.ID || store.ValidateService(v.Service) != nil {
					api.Close()
					return nil, errors.New("corrupt local service approval")
				}
			}
		}
	}
	return rt, nil
}
func (rt *localRuntime) saveLocked() error {
	if rt.opt.PolicyPath == "" {
		return nil
	}
	return secure.WriteJSON(rt.opt.PolicyPath, rt.policy)
}
func (rt *localRuntime) announce() {
	rt.ops.Lock()
	ids := []string{}
	for id, v := range rt.policy.Services {
		if !v.Disabled {
			ids = append(ids, id)
		}
	}
	rt.ops.Unlock()
	rt.mu.Lock()
	c := rt.conn
	if !rt.network {
		c = nil
	}
	rt.mu.Unlock()
	if c != nil {
		_ = c.WriteJSON(model.Control{Type: "services.ready", ServiceIDs: ids})
	}
}
func (rt *localRuntime) restore(ctx context.Context) {
	rt.ops.Lock()
	for id, v := range rt.policy.Services {
		if ctx.Err() != nil {
			break
		}
		if v.Disabled {
			continue
		}
		var out model.Service
		e := rt.api.Do(ctx, "POST", "/api/v1/services", v.Service, &out)
		if e != nil {
			var ae *model.APIError
			if errors.As(e, &ae) && ae.Code == "service_stopped" {
				v.Disabled = true
				rt.policy.Services[id] = v
				_ = rt.saveLocked()
			}
			logEvent(rt.opt.Log, "service.restore_failed", map[string]any{"serviceId": id, "error": e.Error()})
		}
	}
	rt.ops.Unlock()
	rt.announce()
}
func (rt *localRuntime) approved(id, target string) bool {
	rt.ops.Lock()
	defer rt.ops.Unlock()
	v, ok := rt.policy.Services[id]
	return ok && !v.Disabled && v.Service.Target == target
}
func (rt *localRuntime) create(ctx context.Context, q LocalShareRequest) (model.Service, error) {
	target, e := store.Loopback(q.Target, false)
	if e != nil {
		return model.Service{}, e
	}
	rt.mu.Lock()
	online := rt.conn != nil && rt.network
	rt.mu.Unlock()
	if !online {
		return model.Service{}, &model.APIError{Code: "node_offline", Message: "wait for the local Node to connect to an M2-capable Server", Retryable: true}
	}
	rt.ops.Lock()
	defer rt.ops.Unlock()
	req := model.ServiceCreate{ID: secure.Random("svc_")[:28], Name: q.Name, Protocol: q.Protocol, Target: target, AllowDevices: q.AllowDevices, AllowPrincipals: append([]string{}, q.AllowPrincipals...)}
	if q.AllowOwner {
		var ds struct {
			Devices []model.Device `json:"devices"`
		}
		if e = rt.api.Do(ctx, "GET", "/api/v1/devices", nil, &ds); e != nil {
			return model.Service{}, e
		}
		if len(ds.Devices) != 1 {
			return model.Service{}, errors.New("could not resolve the device's owner principal")
		}
		req.AllowPrincipals = append(req.AllowPrincipals, ds.Devices[0].OwnerID)
	}
	if e = store.ValidateService(req); e != nil {
		return model.Service{}, e
	}
	for _, v := range rt.policy.Services {
		if !v.Disabled && v.Service.Name == req.Name {
			if v.Service.Target != req.Target || v.Service.Protocol != req.Protocol {
				return model.Service{}, errors.New("service already approved with a different target; stop it before changing target")
			}
			req = v.Service // Idempotent retries do not broaden an existing ACL.
		}
	}
	old, existed := rt.policy.Services[req.ID]
	rt.policy.Services[req.ID] = Approval{Service: req}
	if e = rt.saveLocked(); e != nil {
		if existed {
			rt.policy.Services[req.ID] = old
		} else {
			delete(rt.policy.Services, req.ID)
		}
		return model.Service{}, e
	}
	var out model.Service
	e = rt.api.Do(ctx, "POST", "/api/v1/services", req, &out)
	// Local intent remains durable after a transient error, so reconnect can retry.
	if e != nil {
		return out, e
	}
	return out, nil
}
func (rt *localRuntime) stop(ctx context.Context, key string) error {
	rt.ops.Lock()
	id := key
	v, ok := rt.policy.Services[id]
	if !ok {
		for k, item := range rt.policy.Services {
			if item.Service.Name == key && !item.Disabled {
				id = k
				v = item
				ok = true
				break
			}
		}
	}
	if !ok {
		// Stopping an already disabled named service is idempotent.
		for k, item := range rt.policy.Services {
			if item.Service.Name == key {
				id, v, ok = k, item, true
				break
			}
		}
	}
	if !ok {
		rt.ops.Unlock()
		return os.ErrNotExist
	}
	before := v
	v.Disabled = true
	rt.policy.Services[id] = v
	if e := rt.saveLocked(); e != nil {
		rt.policy.Services[id] = before
		rt.ops.Unlock()
		return e
	}
	rt.ops.Unlock()
	rt.mu.Lock()
	for _, f := range rt.flows {
		if f.serviceID == id {
			f.cancel()
		}
	}
	rt.mu.Unlock()
	rt.announce()
	return rt.api.Do(ctx, "DELETE", "/api/v1/services/"+id, nil, nil)
}
func (rt *localRuntime) startLocal(ctx context.Context) (func(), error) {
	if rt.opt.ControlFile == "" {
		return func() {}, nil
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	token := secure.Random("local_")
	addr := listener.Addr().String()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fail := func(status int, e error) {
			var ae *model.APIError
			if !errors.As(e, &ae) {
				ae = &model.APIError{Code: "local_operation_failed", Message: e.Error()}
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": ae})
		}
		if r.Host != addr || r.Header.Get("Origin") != "" || !secure.Equal(r.Header.Get("Authorization"), "Bearer "+token) {
			fail(403, errors.New("local CLI credential required; browser origins forbidden"))
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/share":
			var q LocalShareRequest
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
			d.DisallowUnknownFields()
			if e := d.Decode(&q); e != nil {
				fail(400, e)
				return
			}
			var extra any
			if e := d.Decode(&extra); e != io.EOF {
				fail(400, errors.New("one JSON object required"))
				return
			}
			v, e := rt.create(r.Context(), q)
			if e != nil {
				fail(400, e)
				return
			}
			rt.announce()
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(v)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/services/"):
			e := rt.stop(r.Context(), strings.TrimPrefix(r.URL.Path, "/services/"))
			if e != nil {
				fail(503, e)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"stopped": true})
		case r.Method == "GET" && r.URL.Path == "/status":
			rt.mu.Lock()
			online := rt.conn != nil && rt.network
			flows := len(rt.flows)
			rt.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"deviceId": rt.cfg.SubjectID, "online": online, "activeConnections": flows})
		default:
			fail(404, errors.New("unknown local endpoint"))
		}
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, MaxHeaderBytes: 8192}
	if e = secure.WriteJSON(rt.opt.ControlFile, LocalEndpoint{URL: "http://" + addr, Token: token, DeviceID: rt.cfg.SubjectID}); e != nil {
		listener.Close()
		return nil, e
	}
	go func() { _ = srv.Serve(listener) }()
	stop := context.AfterFunc(ctx, func() { _ = srv.Close() })
	return func() { stop(); _ = srv.Close(); _ = os.Remove(rt.opt.ControlFile) }, nil
}

// Paths are tied to a specific device profile. This is not a remotely callable shell.
func LocalPaths(config string) (string, string) {
	p, _ := filepath.Abs(config)
	return p + ".services.json", p + ".node.json"
}
