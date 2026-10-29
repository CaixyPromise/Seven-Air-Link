// Package store persists identity, messages and audit in SQLite WAL transactions.
// Only small authority metadata is cached; history is queried using indexed sequences.
package store

import (
	"errors"
	"maps"
	"os"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/sqlite"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxEvents = 2000

var ErrAuth = errors.New("unauthenticated")
var ErrRevoked = errors.New("device_revoked")
var ErrPairing = errors.New("pairing_invalid_or_expired")
var ErrCursor = errors.New("cursor_expired")

type deviceRecord struct {
	Device    model.Device `json:"device"`
	TokenHash string       `json:"tokenHash"`
}
type pairingRecord struct {
	ExpiresAt time.Time `json:"expiresAt"`
}
type snapshot struct {
	Principals   map[string]principalRecord `json:"principals"`
	Messages     []model.Message            `json:"messages"`
	MessageSeq   int64                      `json:"messageSeq"`
	MessageBytes int64                      `json:"messageBytes"`
	Files        map[string]model.File      `json:"files"`
	Services     map[string]model.Service   `json:"services"`
	Version      int                        `json:"version"`
	Revision     int64                      `json:"revision,omitempty"`
	OwnerID      string                     `json:"ownerId"`
	OwnerHash    string                     `json:"ownerTokenHash"`
	Devices      map[string]deviceRecord    `json:"devices"`
	Pairings     map[string]pairingRecord   `json:"pairings"`
	Seq          int64                      `json:"seq"`
	Events       []model.Event              `json:"events"`
}
type Store struct {
	mu   sync.RWMutex
	path string
	s    snapshot
	db   *sqlite.DB
}

// CredentialStamp is an internal credential-generation binding. It never leaves
// the process in JSON and prevents capabilities from surviving token rotation.
type Subject struct {
	ID              string
	Role            string
	CredentialStamp string `json:"-"`
}

// New creates an actual SQLite WAL database. Callers own Close.
func New(path, ownerID, token string) (*Store, error) {
	if _, e := os.Lstat(path); e == nil {
		return nil, errors.New("database already exists")
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	s := snapshot{Version: 4, Principals: map[string]principalRecord{}, Messages: []model.Message{}, Files: map[string]model.File{}, Services: map[string]model.Service{}, OwnerID: ownerID, OwnerHash: secure.Hash(token), Devices: map[string]deviceRecord{}, Pairings: map[string]pairingRecord{}, Events: []model.Event{}}
	return createDB(path, s)
}
func Open(path string) (*Store, error) { return openDB(path) }
func (st *Store) Close() error         { st.mu.Lock(); defer st.mu.Unlock(); return st.db.Close() }
func cloneMetadata(s snapshot) snapshot {
	// Historical bodies/events live in indexed tables, not this cache. Record
	// mutations replace values; slices owned by Service are explicitly cloned.
	n := s
	n.Principals = maps.Clone(s.Principals)
	n.Devices = maps.Clone(s.Devices)
	n.Files = maps.Clone(s.Files)
	n.Pairings = maps.Clone(s.Pairings)
	n.Services = maps.Clone(s.Services)
	for id, v := range n.Services {
		n.Services[id] = cloneService(v)
	}
	n.Messages = []model.Message{}
	n.Events = []model.Event{}
	return n
}
func (st *Store) mutate(f func(*snapshot) error) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	next := cloneMetadata(st.s)
	if e := f(&next); e != nil {
		return e
	}
	next.Revision = st.s.Revision + 1
	if e := st.write(next); e != nil {
		return e
	}
	// Publish only after COMMIT succeeds. At most the current mutation's records
	// were held here; old message bodies are not cloned on heartbeats or writes.
	next.Messages = []model.Message{}
	next.Events = []model.Event{}
	st.s = next
	return nil
}
func addEvent(s *snapshot, ev model.Event) {
	s.Seq++
	ev.Seq = s.Seq
	ev.Time = time.Now().UTC()
	s.Events = append(s.Events, ev)
	if len(s.Events) > MaxEvents {
		s.Events = append([]model.Event(nil), s.Events[len(s.Events)-MaxEvents:]...)
	}
}
func (st *Store) OwnerID() string { st.mu.RLock(); defer st.mu.RUnlock(); return st.s.OwnerID }
func (st *Store) Auth(token string) (Subject, error) {
	if len(token) < 32 || len(token) > 160 {
		return Subject{}, ErrAuth
	}
	h := secure.Hash(token)
	st.mu.RLock()
	defer st.mu.RUnlock()
	if secure.Equal(h, st.s.OwnerHash) {
		return Subject{ID: st.s.OwnerID, Role: "owner"}, nil
	}
	for id, r := range st.s.Devices {
		if secure.Equal(h, r.TokenHash) {
			if r.Device.RevokedAt != nil {
				return Subject{}, ErrRevoked
			}
			return Subject{ID: id, Role: "device"}, nil
		}
	}
	for id, rec := range st.s.Principals {
		if rec.TokenHash != "" && secure.Equal(h, rec.TokenHash) {
			if !activePrincipal(&st.s, id) {
				return Subject{}, ErrPrincipalInactive
			}
			return Subject{ID: id, Role: "member", CredentialStamp: h}, nil
		}
	}
	return Subject{}, ErrAuth
}
func (st *Store) Pair(ttl time.Duration, requestID string) (model.PairingResponse, error) {
	if ttl < time.Second || ttl > 15*time.Minute {
		return model.PairingResponse{}, errors.New("TTL must be between 1s and 15m")
	}
	r := model.PairingResponse{Code: secure.Random("pair_"), ExpiresAt: time.Now().Add(ttl).UTC()}
	e := st.mutate(func(s *snapshot) error {
		for k, p := range s.Pairings {
			if time.Now().After(p.ExpiresAt) {
				delete(s.Pairings, k)
			}
		}
		if len(s.Pairings) >= 64 {
			return errors.New("too many active pairing codes")
		}
		s.Pairings[secure.Hash(r.Code)] = pairingRecord{r.ExpiresAt}
		addEvent(s, model.Event{ActorID: s.OwnerID, Action: "pairing.created", Result: "success", RequestID: requestID})
		return nil
	})
	return r, e
}
func ValidName(s string) bool {
	if strings.TrimSpace(s) != s || s == "" || utf8.RuneCountInString(s) > 80 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (st *Store) Join(q model.JoinRequest, maxDevices int, requestID string) (model.JoinResponse, error) {
	if !ValidName(q.Name) || len(q.OS) > 32 || len(q.Arch) > 32 || len(q.NodeVersion) > 64 || q.Protocol != model.Protocol {
		return model.JoinResponse{}, errors.New("invalid device metadata or protocol version")
	}
	r := model.JoinResponse{DeviceID: secure.Random("dev_")[:28], Token: secure.Random("node_"), Protocol: model.Protocol}
	e := st.mutate(func(s *snapshot) error {
		h := secure.Hash(q.Code)
		p, ok := s.Pairings[h]
		if !ok || time.Now().After(p.ExpiresAt) {
			return ErrPairing
		}
		active := 0
		for _, d := range s.Devices {
			if d.Device.RevokedAt == nil {
				active++
			}
		}
		if active >= maxDevices {
			return errors.New("device quota exceeded")
		}
		delete(s.Pairings, h) // Consume the invitation in the same durable commit as registration.
		s.Devices[r.DeviceID] = deviceRecord{Device: model.Device{ID: r.DeviceID, Name: q.Name, OS: q.OS, Arch: q.Arch, NodeVersion: q.NodeVersion, OwnerID: s.OwnerID, CreatedAt: time.Now().UTC(), Status: "offline"}, TokenHash: secure.Hash(r.Token)}
		addEvent(s, model.Event{ActorID: s.OwnerID, DeviceID: r.DeviceID, Action: "device.paired", ResourceID: r.DeviceID, Result: "success", RequestID: requestID})
		return nil
	})
	return r, e
}
func (st *Store) Devices() []model.Device {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]model.Device, 0, len(st.s.Devices))
	for _, r := range st.s.Devices {
		out = append(out, r.Device)
	}
	return out
}
func (st *Store) Device(id string) (model.Device, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r, ok := st.s.Devices[id]
	return r.Device, ok
}
func (st *Store) Revoke(id, requestID string) error {
	return st.mutate(func(s *snapshot) error {
		r, ok := s.Devices[id]
		if !ok {
			return os.ErrNotExist
		}
		if r.Device.RevokedAt != nil {
			return nil
		}
		now := time.Now().UTC()
		r.Device.RevokedAt = &now
		r.Device.Status = "revoked"
		s.Devices[id] = r
		addEvent(s, model.Event{ActorID: s.OwnerID, DeviceID: id, Action: "device.revoked", ResourceID: id, Result: "success", RequestID: requestID})
		return nil
	})
}
func (st *Store) Rename(id, name, requestID string) error {
	if !ValidName(name) {
		return errors.New("invalid name")
	}
	return st.mutate(func(s *snapshot) error {
		r, ok := s.Devices[id]
		if !ok {
			return os.ErrNotExist
		}
		r.Device.Name = name
		s.Devices[id] = r
		addEvent(s, model.Event{ActorID: s.OwnerID, DeviceID: id, Action: "device.renamed", ResourceID: id, Result: "success", RequestID: requestID})
		return nil
	})
}
func (st *Store) Presence(id, action string, seen time.Time, ip string) error {
	return st.mutate(func(s *snapshot) error {
		r, ok := s.Devices[id]
		if !ok {
			return os.ErrNotExist
		}
		seen = seen.UTC()
		r.Device.LastSeen = &seen
		r.Device.IP = ip
		s.Devices[id] = r
		addEvent(s, model.Event{ActorID: id, DeviceID: id, Action: action, ResourceID: id, Result: "success"})
		return nil
	})
}
func (st *Store) Audit(ev model.Event) error {
	return st.mutate(func(s *snapshot) error { addEvent(s, ev); return nil })
}
func (st *Store) Events(after int64, limit int) ([]model.Event, int64, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if limit < 1 || limit > 500 {
		limit = 100
	}
	if after < 0 || after > st.s.Seq {
		return nil, st.s.Seq, errors.New("invalid cursor")
	}
	if after > 0 && after < st.s.Seq-MaxEvents {
		return nil, st.s.Seq, ErrCursor
	}
	ev, e := st.queryEvents("SELECT data FROM events WHERE seq>? ORDER BY seq LIMIT ?", after, limit)
	return ev, st.s.Seq, e
}
func (st *Store) Recent(limit int) []model.Event {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if limit < 1 || limit > 500 {
		limit = 100
	}
	ev, e := st.queryEvents("SELECT data FROM events ORDER BY seq DESC LIMIT ?", limit)
	if e != nil {
		return []model.Event{}
	}
	slices.Reverse(ev)
	return ev
}
func (st *Store) Ready() error {
	st.mu.RLock()
	defer st.mu.RUnlock()
	rows, e := st.db.Query("SELECT schema_version FROM server_state WHERE id=1")
	if e != nil {
		return e
	}
	if len(rows) != 1 || rows[0][0] != int64(4) {
		return errors.New("database not ready")
	}
	return nil
}
