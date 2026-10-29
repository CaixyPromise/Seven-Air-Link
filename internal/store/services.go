package store

import (
	"errors"
	"net"
	"os"
	"regexp"
	"seven-air-link/internal/model"
	"sort"
	"strconv"
	"time"
)

var ErrDenied = errors.New("access_denied")
var ErrServiceStopped = errors.New("service_stopped")
var ErrConflict = errors.New("service_conflict")
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var idRE = regexp.MustCompile(`^svc_[A-Za-z0-9_-]{8,64}$`)

// Loopback rejects DNS names altogether; no resolver or rebinding window exists.
func Loopback(address string, zero bool) (string, error) {
	h, p, e := net.SplitHostPort(address)
	if e != nil {
		return "", errors.New("expected a literal loopback IP:port")
	}
	ip := net.ParseIP(h)
	port, err := strconv.Atoi(p)
	low := 1
	if zero {
		low = 0
	}
	if ip == nil || !ip.IsLoopback() || err != nil || port < low || port > 65535 {
		return "", errors.New("only numeric loopback IP addresses and valid ports are allowed")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}
func ValidateService(q model.ServiceCreate) error {
	if !idRE.MatchString(q.ID) || !nameRE.MatchString(q.Name) || (q.Protocol != "tcp" && q.Protocol != "http") {
		return errors.New("invalid service id, name or protocol")
	}
	target, e := Loopback(q.Target, false)
	if e != nil || target != q.Target {
		return errors.New("target must be a canonical numeric loopback IP:port")
	}
	return nil
}
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if x == v {
			return true
		}
	}
	return false
}
func CanUse(s model.Service, sub Subject) bool {
	if s.Disabled {
		return false
	}
	switch sub.Role {
	case "device":
		return sub.ID == s.OwnerDeviceID || contains(s.AllowDevices, sub.ID)
	case "owner", "member":
		return contains(s.AllowPrincipals, sub.ID)
	}
	return false
}
func CanManage(s model.Service, sub Subject) bool {
	return sub.Role == "owner" && sub.ID == s.OwnerPrincipalID || sub.Role == "device" && sub.ID == s.OwnerDeviceID
}
func cloneService(s model.Service) model.Service {
	s.AllowDevices = append([]string{}, s.AllowDevices...)
	s.AllowPrincipals = append([]string{}, s.AllowPrincipals...)
	return s
}
func (st *Store) Services() []model.Service {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]model.Service, 0, len(st.s.Services))
	for _, s := range st.s.Services {
		out = append(out, cloneService(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (st *Store) Service(id string) (model.Service, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if s, ok := st.s.Services[id]; ok {
		return cloneService(s), true
	}
	for _, s := range st.s.Services {
		if s.Name == id && !s.Disabled {
			return cloneService(s), true
		}
	}
	return model.Service{}, false
}
func validGrants(s *snapshot, g model.Grants) error {
	if len(g.AllowDevices) > 64 || len(g.AllowPrincipals) > 16 {
		return errors.New("too many grants")
	}
	for _, id := range g.AllowDevices {
		d, ok := s.Devices[id]
		if !ok || d.Device.RevokedAt != nil {
			return errors.New("unknown or revoked allowed device")
		}
	}
	for _, id := range g.AllowPrincipals {
		if !activePrincipal(s, id) {
			return errors.New("unknown, pending, expired or revoked principal")
		}
	}
	return nil
}
func (st *Store) CreateService(sub Subject, q model.ServiceCreate, req string) (model.Service, error) {
	if e := ValidateService(q); e != nil {
		return model.Service{}, e
	}
	if sub.Role != "device" {
		return model.Service{}, ErrDenied
	}
	var result model.Service
	e := st.mutate(func(s *snapshot) error {
		d, ok := s.Devices[sub.ID]
		if !ok || d.Device.RevokedAt != nil {
			return ErrRevoked
		}
		if old, ok := s.Services[q.ID]; ok {
			if old.OwnerDeviceID != sub.ID {
				return ErrDenied
			}
			if old.Disabled {
				return ErrServiceStopped
			}
			if old.Name != q.Name || old.Protocol != q.Protocol || old.Target != q.Target {
				return ErrConflict
			}
			// Retry/reconnect must never overwrite a newer authoritative ACL.
			result = cloneService(old)
			return nil
		}
		count := 0
		for _, old := range s.Services {
			if !old.Disabled {
				if old.Name == q.Name {
					return ErrConflict
				}
				if old.OwnerDeviceID == sub.ID {
					count++
				}
			}
		}
		if count >= 64 || len(s.Services) >= 4096 {
			return errors.New("service registry quota exceeded")
		}
		if e := validGrants(s, model.Grants{AllowDevices: q.AllowDevices, AllowPrincipals: q.AllowPrincipals}); e != nil {
			return e
		}
		now := time.Now().UTC()
		result = model.Service{ID: q.ID, Name: q.Name, Protocol: q.Protocol, Target: q.Target, OwnerDeviceID: sub.ID, OwnerPrincipalID: s.OwnerID, AllowDevices: append([]string{}, q.AllowDevices...), AllowPrincipals: append([]string{}, q.AllowPrincipals...), CreatedAt: now, UpdatedAt: now, Revision: 1, Status: "offline"}
		s.Services[q.ID] = result
		addEvent(s, model.Event{ActorID: sub.ID, DeviceID: sub.ID, Action: "service.created", ResourceID: q.ID, Result: "success", RequestID: req})
		return nil
	})
	return result, e
}
func (st *Store) ChangeService(sub Subject, id string, g *model.Grants, req string) error {
	return st.mutate(func(s *snapshot) error {
		v, ok := s.Services[id]
		if !ok {
			return os.ErrNotExist
		}
		if !CanManage(v, sub) {
			return ErrDenied
		}
		if v.Disabled {
			if g == nil {
				return nil
			}
			return ErrServiceStopped
		}
		action := "service.stopped"
		if g == nil {
			v.Disabled = true
		} else {
			if e := validGrants(s, *g); e != nil {
				return e
			}
			v.AllowDevices = append([]string{}, g.AllowDevices...)
			v.AllowPrincipals = append([]string{}, g.AllowPrincipals...)
			action = "service.grants_changed"
		}
		v.UpdatedAt = time.Now().UTC()
		v.Revision++
		s.Services[id] = v
		addEvent(s, model.Event{ActorID: sub.ID, Action: action, ResourceID: id, Result: "success", RequestID: req})
		return nil
	})
}
