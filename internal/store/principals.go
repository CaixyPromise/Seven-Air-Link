package store

import (
	"errors"
	"os"
	"sort"
	"time"

	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
)

var ErrPrincipalInactive = errors.New("principal_inactive")
var ErrEnrollment = errors.New("enrollment_invalid_or_expired")
var ErrIdempotency = errors.New("idempotency_conflict")
var ErrQuota = errors.New("quota_exceeded")

type principalRecord struct {
	Principal         model.Principal `json:"principal"`
	TokenHash         string          `json:"tokenHash"`
	EnrollmentHash    string          `json:"enrollmentHash,omitempty"`
	EnrollmentExpires time.Time       `json:"enrollmentExpires,omitempty"`
}

func activePrincipal(s *snapshot, id string) bool {
	if id == s.OwnerID {
		return true
	}
	rec, ok := s.Principals[id]
	if !ok || rec.TokenHash == "" || rec.Principal.RevokedAt != nil {
		return false
	}
	if rec.Principal.ExpiresAt == nil || !time.Now().Before(*rec.Principal.ExpiresAt) {
		return false
	}
	if d := rec.Principal.DeviceID; d != "" {
		device, ok := s.Devices[d]
		if !ok || device.Device.RevokedAt != nil {
			return false
		}
	}
	return true
}
func activeSubject(s *snapshot, sub Subject) bool {
	switch sub.Role {
	case "owner":
		return sub.ID == s.OwnerID
	case "member":
		return sub.ID != s.OwnerID && activePrincipal(s, sub.ID) && sub.CredentialStamp != "" && secure.Equal(sub.CredentialStamp, s.Principals[sub.ID].TokenHash)
	case "device":
		d, ok := s.Devices[sub.ID]
		return ok && d.Device.RevokedAt == nil
	}
	return false
}

// Active checks identity lifetime in addition to static service ACL membership.
func (st *Store) Active(sub Subject) bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return activeSubject(&st.s, sub)
}
func principalView(s *snapshot, rec principalRecord) model.Principal {
	p := rec.Principal
	switch {
	case p.RevokedAt != nil:
		p.Status = "revoked"
	case p.ExpiresAt != nil && !time.Now().Before(*p.ExpiresAt):
		p.Status = "expired"
	case p.DeviceID != "" && (s.Devices[p.DeviceID].Device.ID == "" || s.Devices[p.DeviceID].Device.RevokedAt != nil):
		p.Status = "device_revoked"
	case rec.TokenHash == "":
		p.Status = "pending"
	default:
		p.Status = "active"
	}
	return p
}
func ownerView(s *snapshot) model.Principal {
	return model.Principal{ID: s.OwnerID, Name: "Owner", Type: "human", Role: "owner", OwnerID: s.OwnerID, Status: "active"}
}
func (st *Store) Principals() []model.Principal {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := []model.Principal{ownerView(&st.s)}
	for _, rec := range st.s.Principals {
		out = append(out, principalView(&st.s, rec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
func (st *Store) Principal(id string) (model.Principal, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if id == st.s.OwnerID {
		return ownerView(&st.s), true
	}
	rec, ok := st.s.Principals[id]
	return principalView(&st.s, rec), ok
}

// CreatePrincipal only issues an enrollment code. It never grants a Device token
// or the Owner's management rights to a participant.
func (st *Store) CreatePrincipal(sub Subject, q model.PrincipalCreate, req string) (model.Enrollment, error) {
	if !ValidName(q.Name) || (q.Type != "human" && q.Type != "bot") {
		return model.Enrollment{}, errors.New("name and type=human|bot are required")
	}
	if q.TTLSeconds == 0 {
		q.TTLSeconds = 30 * 24 * 3600
	}
	if q.TTLSeconds < 60 || q.TTLSeconds > 365*24*3600 {
		return model.Enrollment{}, errors.New("credential lifetime must be 60 seconds to 365 days")
	}
	now := time.Now().UTC()
	expires := now.Add(time.Duration(q.TTLSeconds) * time.Second)
	p := model.Principal{ID: secure.Random(q.Type + "_")[:28], Name: q.Name, Type: q.Type, Role: "member", DeviceID: q.DeviceID, CreatedAt: now, ExpiresAt: &expires, Status: "pending"}
	out := model.Enrollment{Principal: p, Code: secure.Random("bind_"), ExpiresAt: now.Add(5 * time.Minute)}
	err := st.mutate(func(s *snapshot) error {
		if sub.Role != "owner" || sub.ID != s.OwnerID {
			return ErrDenied
		}
		if len(s.Principals) >= 256 {
			return ErrQuota
		}
		if q.DeviceID != "" {
			d, ok := s.Devices[q.DeviceID]
			if !ok || d.Device.RevokedAt != nil {
				return errors.New("unknown or revoked bound device")
			}
		}
		for _, rec := range s.Principals {
			if rec.Principal.Name == q.Name && rec.Principal.RevokedAt == nil {
				return ErrConflict
			}
		}
		out.Principal.OwnerID = s.OwnerID
		s.Principals[p.ID] = principalRecord{Principal: out.Principal, EnrollmentHash: secure.Hash(out.Code), EnrollmentExpires: out.ExpiresAt}
		addEvent(s, model.Event{ActorID: sub.ID, Action: "principal.created", ResourceID: p.ID, Result: "success", RequestID: req})
		return nil
	})
	return out, err
}
func (st *Store) Claim(code, req string) (model.Claim, error) {
	if len(code) < 32 || len(code) > 160 {
		return model.Claim{}, ErrEnrollment
	}
	out := model.Claim{Token: secure.Random("principal_")}
	err := st.mutate(func(s *snapshot) error {
		for id, rec := range s.Principals {
			if rec.EnrollmentHash == "" || !secure.Equal(secure.Hash(code), rec.EnrollmentHash) {
				continue
			}
			if time.Now().After(rec.EnrollmentExpires) || rec.Principal.RevokedAt != nil || !time.Now().Before(*rec.Principal.ExpiresAt) {
				return ErrEnrollment
			}
			if d := rec.Principal.DeviceID; d != "" {
				if v, ok := s.Devices[d]; !ok || v.Device.RevokedAt != nil {
					return ErrEnrollment
				}
			}
			rec.EnrollmentHash = ""
			rec.EnrollmentExpires = time.Time{}
			rec.TokenHash = secure.Hash(out.Token)
			rec.Principal.Status = "active"
			s.Principals[id] = rec
			out.Principal = rec.Principal
			addEvent(s, model.Event{ActorID: id, DeviceID: rec.Principal.DeviceID, Action: "principal.bound", ResourceID: id, Result: "success", RequestID: req})
			return nil
		}
		return ErrEnrollment
	})
	return out, err
}
func (st *Store) RevokePrincipal(sub Subject, id, req string) error {
	return st.mutate(func(s *snapshot) error {
		if sub.Role != "owner" || sub.ID != s.OwnerID {
			return ErrDenied
		}
		rec, ok := s.Principals[id]
		if !ok {
			return os.ErrNotExist
		}
		if rec.Principal.RevokedAt != nil {
			return nil
		}
		now := time.Now().UTC()
		rec.Principal.RevokedAt = &now
		rec.Principal.Status = "revoked"
		rec.EnrollmentHash = ""
		s.Principals[id] = rec
		addEvent(s, model.Event{ActorID: sub.ID, Action: "principal.revoked", ResourceID: id, Result: "success", RequestID: req})
		return nil
	})
}

// Reenroll rotates through a fresh, one-use invitation; old secrets fail at once.
func (st *Store) Reenroll(sub Subject, id, req string) (model.Enrollment, error) {
	out := model.Enrollment{Code: secure.Random("bind_"), ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}
	err := st.mutate(func(s *snapshot) error {
		if sub.Role != "owner" || sub.ID != s.OwnerID {
			return ErrDenied
		}
		rec, ok := s.Principals[id]
		if !ok {
			return os.ErrNotExist
		}
		if rec.Principal.RevokedAt != nil {
			return ErrPrincipalInactive
		}
		if rec.Principal.DeviceID != "" {
			if d := s.Devices[rec.Principal.DeviceID]; d.Device.RevokedAt != nil {
				return ErrPrincipalInactive
			}
		}
		expires := time.Now().UTC().Add(30 * 24 * time.Hour)
		rec.Principal.ExpiresAt = &expires
		rec.TokenHash = ""
		rec.Principal.Status = "pending"
		rec.EnrollmentHash = secure.Hash(out.Code)
		rec.EnrollmentExpires = out.ExpiresAt
		s.Principals[id] = rec
		out.Principal = rec.Principal
		addEvent(s, model.Event{ActorID: sub.ID, Action: "principal.credential_rotated", ResourceID: id, Result: "success", RequestID: req})
		return nil
	})
	return out, err
}
