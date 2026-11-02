package store

import (
	"errors"
	"os"
	"sort"
	"time"

	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/sqlite"
)

type clientRecord struct {
	Client    model.Client `json:"client"`
	TokenHash string       `json:"tokenHash"`
}

// Upgrade deliberately disables rc.3's administrator password. It must not be
// silently repurposed as a LAN admission secret. Owner token/notes/ACLs survive.
func upgradeIdentitySchema(db *sqlite.DB) error {
	rows, e := db.Query("PRAGMA user_version")
	if e != nil {
		return e
	}
	if len(rows) != 1 {
		return errors.New("missing schema")
	}
	if rows[0][0] == int64(SchemaVersion) {
		return nil
	}
	if rows[0][0] != int64(5) {
		return errors.New("unsupported identity schema")
	}
	return db.Transaction(func(tx *sqlite.Tx) error {
		for _, q := range []string{
			`CREATE TABLE server_state_next (id INTEGER PRIMARY KEY CHECK(id=1), schema_version INTEGER NOT NULL CHECK(schema_version=6), owner_id TEXT NOT NULL, owner_hash TEXT NOT NULL, seq INTEGER NOT NULL, message_seq INTEGER NOT NULL, message_bytes INTEGER NOT NULL, revision INTEGER NOT NULL)`,
			`INSERT INTO server_state_next SELECT id,6,owner_id,owner_hash,seq,message_seq,message_bytes,revision FROM server_state`,
			`DROP TABLE server_state`, `ALTER TABLE server_state_next RENAME TO server_state`,
			`CREATE TABLE clients (id TEXT PRIMARY KEY, data TEXT NOT NULL)`,
			`ALTER TABLE shared_notes ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
			`UPDATE web_auth SET password_hash='',revision=revision+1`,
			`PRAGMA user_version=6`,
		} {
			if _, e := tx.Exec(q); e != nil {
				return e
			}
		}
		return nil
	})
}

func clientActive(s *snapshot, c clientRecord) bool {
	return c.Client.RevokedAt == nil && time.Now().Before(c.Client.ExpiresAt) && activePrincipal(s, c.Client.PrincipalID)
}
func canManagePrincipal(s *snapshot, sub Subject, id string) bool {
	if !activeSubject(s, sub) {
		return false
	}
	if sub.Role == "owner" {
		return sub.ID == s.OwnerID
	}
	p, ok := s.Principals[id]
	return ok && sub.Role == "member" && s.Principals[sub.ID].Principal.Type == "human" && p.Principal.Type == "bot" && p.Principal.OwnerID == sub.ID
}
func (st *Store) CanManagePrincipal(sub Subject, id string) bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return canManagePrincipal(&st.s, sub, id)
}

func allocateClient(s *snapshot, principal model.Principal, name, kind, ip string) (model.ClientClaim, error) {
	if !ValidName(name) || (kind != "browser" && kind != "cli") {
		return model.ClientClaim{}, errors.New("valid client name and kind required")
	}
	if len(s.Clients) >= 2048 {
		return model.ClientClaim{}, ErrQuota
	}
	active := 0
	for _, v := range s.Clients {
		if v.Client.PrincipalID == principal.ID && clientActive(s, v) {
			active++
		}
	}
	if active >= 16 {
		return model.ClientClaim{}, ErrQuota
	}
	now := time.Now().UTC()
	expiry := now.Add(30 * 24 * time.Hour)
	if principal.ExpiresAt != nil && principal.ExpiresAt.Before(expiry) {
		expiry = *principal.ExpiresAt
	}
	c := model.Client{ID: secure.Random("client_")[:32], PrincipalID: principal.ID, Name: name, Kind: kind, CreatedAt: now, ExpiresAt: expiry, CreatedIP: ip}
	token := secure.Random("client_")
	s.Clients[c.ID] = clientRecord{Client: c, TokenHash: secure.Hash(token)}
	return model.ClientClaim{Principal: principal, Client: c, Token: token}, nil
}

// JoinClient is used only after KDF verification. The revision is rechecked in
// the same transaction, so a rotated/disabled password cannot enroll a client.
func (st *Store) JoinClient(name, kind, ip string, passwordRevision int64, req string) (model.ClientClaim, error) {
	var out model.ClientClaim
	if !ValidName(name) {
		return out, errors.New("client name is required (1..80 characters)")
	}
	e := st.mutate(func(s *snapshot) error {
		if s.WebAuth.Hash == "" || s.WebAuth.Revision != passwordRevision {
			return ErrAuthRevision
		}
		if len(s.Principals) >= 256 {
			return ErrQuota
		}
		now := time.Now().UTC()
		expiry := now.Add(365 * 24 * time.Hour)
		p := model.Principal{ID: secure.Random("human_")[:28], Name: name, Type: "human", Role: "member", OwnerID: s.OwnerID, Status: "active", CreatedAt: now, ExpiresAt: &expiry}
		s.Principals[p.ID] = principalRecord{Principal: p, ManagedClients: true}
		var err error
		out, err = allocateClient(s, p, name, kind, ip)
		if err != nil {
			return err
		}
		source := model.Source{ClientID: out.Client.ID, Kind: kind, IP: ip}
		addEvent(s, model.Event{ActorID: p.ID, Action: "client.joined", ResourceID: out.Client.ID, Source: &source, Result: "success", RequestID: req})
		return nil
	})
	return out, e
}

func (st *Store) ResumeBrowser(token string, revision int64) (Subject, error) {
	sub, e := st.Auth(token)
	if e != nil {
		return Subject{}, e
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	if st.s.WebAuth.Hash == "" || st.s.WebAuth.Revision != revision {
		return Subject{}, ErrAuthRevision
	}
	c, ok := st.s.Clients[sub.Source.ClientID]
	if !ok || c.Client.Kind != "browser" || !clientActive(&st.s, c) {
		return Subject{}, ErrDenied
	}
	return sub, nil
}

// BindClient creates a fresh credential, never exposes or copies browser tokens.
func (st *Store) BindClient(sub Subject, name, ip, req string) (model.ClientClaim, error) {
	var out model.ClientClaim
	e := st.mutate(func(s *snapshot) error {
		p, ok := s.Principals[sub.ID]
		if !ok || !activeSubject(s, sub) || sub.Role != "member" || p.Principal.Type != "human" {
			return ErrDenied
		}
		var e error
		out, e = allocateClient(s, p.Principal, name, "cli", ip)
		if e != nil {
			return e
		}
		source := model.Source{ClientID: out.Client.ID, Kind: "cli", IP: ip}
		addEvent(s, model.Event{ActorID: sub.ID, Action: "client.bound", ResourceID: out.Client.ID, Result: "success", Source: &source, RequestID: req})
		return nil
	})
	return out, e
}
func (st *Store) Clients(sub Subject) []model.Client {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := []model.Client{}
	if !activeSubject(&st.s, sub) {
		return out
	}
	for _, v := range st.s.Clients {
		if sub.Role == "owner" || v.Client.PrincipalID == sub.ID {
			out = append(out, v.Client)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
func (st *Store) RevokeClient(sub Subject, id, req string) error {
	return st.mutate(func(s *snapshot) error {
		if !activeSubject(s, sub) {
			return ErrDenied
		}
		c, ok := s.Clients[id]
		if !ok {
			return os.ErrNotExist
		}
		if sub.Role != "owner" && c.Client.PrincipalID != sub.ID {
			return ErrDenied
		}
		if c.Client.RevokedAt != nil {
			return nil
		}
		now := time.Now().UTC()
		c.Client.RevokedAt = &now
		s.Clients[id] = c
		source := sub.Source
		addEvent(s, model.Event{ActorID: sub.ID, Action: "client.revoked", ResourceID: id, Result: "success", Source: &source, RequestID: req})
		return nil
	})
}

// Participants may enroll their own optional Node. No administrative authority
// is conferred on the independent device token; local service approval remains.
func (st *Store) PairClientDevice(sub Subject, req string) (model.PairingResponse, error) {
	out := model.PairingResponse{Code: secure.Random("pair_"), ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}
	err := st.mutate(func(s *snapshot) error {
		if sub.Role != "member" || !activeSubject(s, sub) || s.Principals[sub.ID].Principal.Type != "human" {
			return ErrDenied
		}
		count := 0
		for _, d := range s.Devices {
			if d.Device.OwnerID == sub.ID && d.Device.RevokedAt == nil {
				count++
			}
		}
		if count >= 8 {
			return ErrQuota
		}
		for k, p := range s.Pairings {
			if time.Now().After(p.ExpiresAt) {
				delete(s.Pairings, k)
			}
		}
		if len(s.Pairings) >= 64 {
			return ErrQuota
		}
		s.Pairings[secure.Hash(out.Code)] = pairingRecord{OwnerID: sub.ID, IssuerStamp: sub.CredentialStamp, ExpiresAt: out.ExpiresAt}
		source := sub.Source
		addEvent(s, model.Event{ActorID: sub.ID, Action: "client.node_invited", Result: "success", Source: &source, RequestID: req})
		return nil
	})
	return out, err
}
