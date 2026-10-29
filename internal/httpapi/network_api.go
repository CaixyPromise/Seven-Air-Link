package httpapi

import (
	"errors"
	"net/http"
	"os"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"sort"
	"strings"
	"time"
)

func (s *Server) networkError(w http.ResponseWriter, r *http.Request, e error) {
	var ae *model.APIError
	if errors.As(e, &ae) {
		status := 503
		switch ae.Code {
		case "access_denied":
			status = 403
		case "service_not_found", "forward_not_found":
			status = 404
		case "service_stopped", "forward_closed":
			status = 410
		case "quota_exceeded":
			status = 429
		case "target_refused", "local_policy_denied":
			status = 502
		case "timeout":
			status = 504
		}
		s.deny(w, r, status, ae.Code, ae.Message)
		return
	}
	switch {
	case errors.Is(e, store.ErrDenied):
		s.deny(w, r, 403, "access_denied", "service authorization denied")
	case errors.Is(e, os.ErrNotExist):
		s.deny(w, r, 404, "service_not_found", "service not found")
	case errors.Is(e, store.ErrServiceStopped):
		s.deny(w, r, 410, "service_stopped", "stopped services cannot be resurrected; explicitly create a new share")
	case errors.Is(e, store.ErrConflict):
		s.deny(w, r, 409, "service_conflict", "name or registration conflicts with existing service")
	case errors.Is(e, store.ErrRevoked):
		s.deny(w, r, 403, "device_revoked", "device revoked")
	default:
		s.deny(w, r, 400, "invalid_service_request", e.Error())
	}
}
func networkErr(code, message string) error {
	return &model.APIError{Code: code, Message: message, Retryable: code == "device_offline" || code == "service_unavailable" || code == "timeout"}
}

// Called with s.mu held. Stored intent never alone proves a local service ready.
func (s *Server) serviceStateLocked(v model.Service) string {
	if v.Disabled {
		return "stopped"
	}
	d, ok := s.st.Device(v.OwnerDeviceID)
	if !ok || d.RevokedAt != nil {
		return "revoked"
	}
	n := s.nodes[v.OwnerDeviceID]
	if n == nil {
		return "offline"
	}
	if !n.services[v.ID] {
		return "unapproved"
	}
	return "online"
}
func (s *Server) serviceForUseLocked(sub store.Subject, id string) (model.Service, error) {
	if !s.st.Active(sub) {
		return model.Service{}, networkErr("access_denied", "identity is no longer active")
	}
	v, ok := s.st.Service(id)
	if !ok {
		return v, networkErr("service_not_found", "unknown service")
	}
	if !store.CanUse(v, sub) {
		return v, networkErr("access_denied", "this principal/device is not granted access to the service")
	}
	switch s.serviceStateLocked(v) {
	case "online":
		return v, nil
	case "offline", "revoked":
		return v, networkErr("device_offline", "the serving device is offline or revoked")
	default:
		return v, networkErr("service_unavailable", "the local Node has not approved this service for its current connection")
	}
}
func (s *Server) serviceViews(sub store.Subject) []model.Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.Service{}
	for _, v := range s.st.Services() {
		if !store.CanManage(v, sub) && !store.CanUse(v, sub) {
			continue
		}
		v.Status = s.serviceStateLocked(v)
		for _, t := range s.tunnels {
			if t.service.ID == v.ID && t.started {
				v.ActiveConnections++
			}
		}
		out = append(out, v)
	}
	return out
}
func (s *Server) networkRoute(w http.ResponseWriter, r *http.Request, sub store.Subject) bool {
	path := r.URL.Path
	switch {
	case path == "/api/v1/services" && r.Method == "GET":
		jsonReply(w, 200, map[string]any{"services": s.serviceViews(sub), "proxyPort": s.proxyPort})
		return true
	case path == "/api/v1/services" && r.Method == "POST":
		var q model.ServiceCreate
		if !s.body(w, r, &q) {
			return true
		}
		s.mu.Lock()
		v, e := s.st.CreateService(sub, q, w.Header().Get("X-Request-ID"))
		s.mu.Unlock()
		if e != nil {
			s.networkError(w, r, e)
		} else {
			jsonReply(w, 201, v)
		}
		return true
	case strings.HasPrefix(path, "/api/v1/services/"):
		parts := strings.Split(strings.TrimPrefix(path, "/api/v1/services/"), "/")
		if len(parts) > 2 || parts[0] == "" {
			s.deny(w, r, 404, "not_found", "unknown service route")
			return true
		}
		v, ok := s.st.Service(parts[0])
		if !ok {
			s.deny(w, r, 404, "service_not_found", "unknown service")
			return true
		}
		if len(parts) == 2 && parts[1] == "browser-ticket" && r.Method == "POST" {
			s.proxyTicket(w, r, sub, v)
			return true
		}
		if len(parts) == 1 && r.Method == "GET" {
			if !store.CanManage(v, sub) && !store.CanUse(v, sub) {
				s.networkError(w, r, store.ErrDenied)
				return true
			}
			s.mu.Lock()
			v.Status = s.serviceStateLocked(v)
			s.mu.Unlock()
			jsonReply(w, 200, v)
			return true
		}
		if len(parts) == 1 && (r.Method == "DELETE" || r.Method == "PATCH") {
			var g *model.Grants
			if r.Method == "PATCH" {
				g = &model.Grants{}
				if !s.body(w, r, g) {
					return true
				}
			}
			s.mu.Lock()
			e := s.st.ChangeService(sub, v.ID, g, w.Header().Get("X-Request-ID"))
			s.mu.Unlock()
			if e != nil {
				s.networkError(w, r, e)
			} else {
				// Close all existing flows on ACL edits, even if some grants remain.
				// This makes permission changes immediate and simple to reason about.
				s.closeServiceFlows(v.ID, "authorization_changed")
				jsonReply(w, 200, map[string]bool{"ok": true})
			}
			return true
		}
		s.deny(w, r, 404, "not_found", "unsupported service operation")
		return true
	case path == "/api/v1/forwards" && r.Method == "POST":
		var q struct {
			ServiceID string `json:"serviceId"`
			Listen    string `json:"listen"`
		}
		if !s.body(w, r, &q) {
			return true
		}
		addr, e := store.Loopback(q.Listen, false)
		if e != nil {
			s.networkError(w, r, e)
			return true
		}
		s.mu.Lock()
		v, e := s.serviceForUseLocked(sub, q.ServiceID)
		count := 0
		for _, f := range s.forwards {
			if f.ActorID == sub.ID {
				count++
			}
		}
		if e == nil && (count >= 32 || len(s.forwards) >= 256) {
			e = networkErr("quota_exceeded", "forward lease limit reached")
		}
		var f model.Forward
		if e == nil {
			now := time.Now().UTC()
			f = model.Forward{ID: secure.Random("fwd_")[:28], ServiceID: v.ID, ActorID: sub.ID, ActorRole: sub.Role, CredentialStamp: sub.CredentialStamp, Listen: addr, StartedAt: now, ExpiresAt: now.Add(s.opt.ForwardLease)}
			s.forwards[f.ID] = f
		}
		s.mu.Unlock()
		if e != nil {
			s.networkError(w, r, e)
		} else {
			s.audit(model.Event{ActorID: sub.ID, Action: "forward.opened", ResourceID: f.ID, Result: "success"})
			jsonReply(w, 201, f)
		}
		return true
	case path == "/api/v1/forwards" && r.Method == "GET":
		s.mu.Lock()
		out := []model.Forward{}
		for _, f := range s.forwards {
			if sub.Role == "owner" || f.ActorID == sub.ID {
				out = append(out, f)
			}
		}
		s.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
		jsonReply(w, 200, map[string]any{"forwards": out})
		return true
	case strings.HasPrefix(path, "/api/v1/forwards/"):
		id := strings.TrimPrefix(path, "/api/v1/forwards/")
		s.mu.Lock()
		f, ok := s.forwards[id]
		if !ok {
			s.mu.Unlock()
			s.deny(w, r, 404, "forward_not_found", "forward lease not found or expired")
			return true
		}
		if f.ActorID != sub.ID && sub.Role != "owner" {
			s.mu.Unlock()
			s.networkError(w, r, store.ErrDenied)
			return true
		}
		if r.Method == "DELETE" {
			delete(s.forwards, id)
			s.mu.Unlock()
			s.closeForwardFlows(id, "forward_closed")
			s.audit(model.Event{ActorID: sub.ID, Action: "forward.closed", ResourceID: id, Result: "success"})
			jsonReply(w, 200, map[string]bool{"ok": true})
			return true
		}
		if r.Method == "PATCH" && f.ActorID == sub.ID {
			if !s.st.Active(sub) || f.CredentialStamp != sub.CredentialStamp {
				s.mu.Unlock()
				s.networkError(w, r, store.ErrDenied)
				return true
			}
			v, found := s.st.Service(f.ServiceID)
			if !found || !store.CanUse(v, sub) {
				s.mu.Unlock()
				s.networkError(w, r, store.ErrDenied)
				return true
			}
			if time.Now().After(f.ExpiresAt) {
				delete(s.forwards, id)
				s.mu.Unlock()
				s.closeForwardFlows(id, "forward_lease_expired")
				s.deny(w, r, 404, "forward_not_found", "lease expired")
				return true
			}
			f.ExpiresAt = time.Now().Add(s.opt.ForwardLease)
			s.forwards[id] = f
			state := s.serviceStateLocked(v)
			s.mu.Unlock()
			jsonReply(w, 200, map[string]any{"forward": f, "serviceStatus": state})
			return true
		}
		s.mu.Unlock()
		s.deny(w, r, 405, "method_not_allowed", "use PATCH to renew or DELETE to stop")
		return true
	case path == "/api/v1/tunnels" && r.Method == "POST":
		var q model.TunnelRequest
		if !s.body(w, r, &q) {
			return true
		}
		s.mu.Lock()
		f, ok := s.forwards[q.ForwardID]
		s.mu.Unlock()
		if !ok || time.Now().After(f.ExpiresAt) {
			s.deny(w, r, 404, "forward_not_found", "forward lease is absent or expired")
			return true
		}
		if f.ActorID != sub.ID || f.ActorRole != sub.Role || f.CredentialStamp != sub.CredentialStamp {
			s.networkError(w, r, store.ErrDenied)
			return true
		}
		t, ticket, e := s.newTunnel(sub, f.ServiceID, f.ID, false)
		if e != nil {
			s.audit(model.Event{ActorID: sub.ID, Action: "tunnel.denied", ResourceID: f.ServiceID, Result: "denied", Code: errorCode(e)})
			s.networkError(w, r, e)
		} else {
			jsonReply(w, 201, model.TunnelTicket{TunnelID: t.id, Ticket: ticket, ExpiresAt: t.expires})
		}
		return true
	case path == "/api/v1/tunnel/data" && r.Method == "GET":
		s.dataSocket(w, r, sub)
		return true
	}
	return false
}
func errorCode(e error) string {
	var ae *model.APIError
	if errors.As(e, &ae) {
		return ae.Code
	}
	return "stream_error"
}
