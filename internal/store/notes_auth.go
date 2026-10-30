package store

import (
	"errors"
	"fmt"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/sqlite"
	"time"
	"unicode/utf8"
)

const SchemaVersion = 5
const MaxNoteBytes = 64 * 1024
const webAuthDDL = `CREATE TABLE web_auth (id INTEGER PRIMARY KEY CHECK(id=1), password_hash TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>=0), updated_at TEXT NOT NULL)`
const noteDDL = `CREATE TABLE shared_notes (id TEXT PRIMARY KEY CHECK(id='shared'), text TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>=0), updated_by TEXT NOT NULL, updated_at TEXT NOT NULL)`

type webAuthRecord struct {
	Hash      string
	Revision  int64
	UpdatedAt *time.Time
}
type NoteConflict struct {
	Current model.SharedNote `json:"note"`
}

func (e *NoteConflict) Error() string {
	return fmt.Sprintf("note revision conflict; current revision is %d", e.Current.Revision)
}

var ErrAuthRevision = errors.New("web authentication settings changed; retry explicitly")
var errNoteUnchanged = errors.New("note content unchanged")

// DDL, version and resource defaults commit atomically. Old rc.2 refuses v5.
func upgradeSchema4(db *sqlite.DB) error {
	rows, e := db.Query("PRAGMA user_version")
	if e != nil {
		return e
	}
	if len(rows) != 1 {
		return errors.New("missing database schema")
	}
	if rows[0][0] == int64(SchemaVersion) {
		return nil
	}
	if rows[0][0] != int64(4) {
		return errors.New("unsupported database schema; refusing upgrade")
	}
	return db.Transaction(func(tx *sqlite.Tx) error {
		for _, q := range []string{
			`CREATE TABLE server_state_next (id INTEGER PRIMARY KEY CHECK(id=1), schema_version INTEGER NOT NULL CHECK(schema_version=5), owner_id TEXT NOT NULL, owner_hash TEXT NOT NULL, seq INTEGER NOT NULL, message_seq INTEGER NOT NULL, message_bytes INTEGER NOT NULL, revision INTEGER NOT NULL)`,
			`INSERT INTO server_state_next SELECT id,5,owner_id,owner_hash,seq,message_seq,message_bytes,revision FROM server_state`,
			`DROP TABLE server_state`, `ALTER TABLE server_state_next RENAME TO server_state`,
			webAuthDDL, `INSERT INTO web_auth VALUES(1,'',0,'')`, noteDDL, `INSERT INTO shared_notes VALUES('shared','',0,'','')`, `PRAGMA user_version=5`,
		} {
			if _, e := tx.Exec(q); e != nil {
				return e
			}
		}
		return nil
	})
}
func readNotesAuth(db *sqlite.DB, s *snapshot) error {
	if s.Version == 4 {
		s.Note = model.SharedNote{ID: "shared"}
		return nil
	}
	rows, e := db.Query("SELECT password_hash,revision,updated_at FROM web_auth WHERE id=1")
	if e != nil {
		return e
	}
	if len(rows) != 1 {
		return errors.New("missing web authentication row")
	}
	a := webAuthRecord{Hash: rows[0][0].(string), Revision: rows[0][1].(int64)}
	if a.Revision < 0 {
		return errors.New("invalid web authentication revision")
	}
	if a.Hash != "" {
		if e = secure.ValidatePasswordHash(a.Hash); e != nil {
			return e
		}
	}
	if stamp := rows[0][2].(string); stamp != "" {
		t, e := time.Parse(time.RFC3339Nano, stamp)
		if e != nil {
			return e
		}
		a.UpdatedAt = &t
	}
	rows, e = db.Query("SELECT id,text,revision,updated_by,updated_at FROM shared_notes WHERE id='shared'")
	if e != nil {
		return e
	}
	if len(rows) != 1 {
		return errors.New("missing shared note")
	}
	n := model.SharedNote{ID: rows[0][0].(string), Text: rows[0][1].(string), Revision: rows[0][2].(int64), UpdatedBy: rows[0][3].(string)}
	if n.Revision < 0 || len(n.Text) > MaxNoteBytes || !utf8.ValidString(n.Text) {
		return errors.New("invalid stored shared note")
	}
	if stamp := rows[0][4].(string); stamp != "" {
		t, e := time.Parse(time.RFC3339Nano, stamp)
		if e != nil {
			return e
		}
		n.UpdatedAt = &t
	}
	s.WebAuth = a
	s.Note = n
	return nil
}
func timeValue(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}
func syncNotesAuth(tx *sqlite.Tx, old, next snapshot) error {
	if old.Version == 0 || old.WebAuth.Revision != next.WebAuth.Revision {
		if _, e := tx.Exec("INSERT INTO web_auth VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET password_hash=excluded.password_hash,revision=excluded.revision,updated_at=excluded.updated_at", next.WebAuth.Hash, next.WebAuth.Revision, timeValue(next.WebAuth.UpdatedAt)); e != nil {
			return e
		}
	}
	if old.Version == 0 || old.Note.Revision != next.Note.Revision {
		if _, e := tx.Exec("INSERT INTO shared_notes VALUES('shared',?,?,?,?) ON CONFLICT(id) DO UPDATE SET text=excluded.text,revision=excluded.revision,updated_by=excluded.updated_by,updated_at=excluded.updated_at", next.Note.Text, next.Note.Revision, next.Note.UpdatedBy, timeValue(next.Note.UpdatedAt)); e != nil {
			return e
		}
	}
	return nil
}
func (st *Store) WebPasswordStatus() model.WebPasswordStatus {
	st.mu.RLock()
	defer st.mu.RUnlock()
	a := st.s.WebAuth
	r := model.WebPasswordStatus{Enabled: a.Hash != "", Revision: a.Revision, UpdatedAt: a.UpdatedAt}
	if r.Enabled {
		r.Algorithm = "pbkdf2-hmac-sha256/600000"
	}
	return r
}
func (st *Store) VerifyWebPassword(password string) (bool, int64) {
	st.mu.RLock()
	a := st.s.WebAuth
	st.mu.RUnlock()
	if a.Hash == "" {
		return false, a.Revision
	}
	return secure.VerifyPassword(a.Hash, password), a.Revision
}
func (st *Store) SetWebPassword(sub Subject, encoded string, expected int64, requestID string) error {
	if sub.Role != "owner" {
		return ErrDenied
	}
	if encoded != "" {
		if e := secure.ValidatePasswordHash(encoded); e != nil {
			return e
		}
	}
	return st.mutate(func(s *snapshot) error {
		if sub.ID != s.OwnerID {
			return ErrDenied
		}
		if s.WebAuth.Revision != expected {
			return ErrAuthRevision
		}
		t := time.Now().UTC()
		s.WebAuth = webAuthRecord{encoded, s.WebAuth.Revision + 1, &t}
		action := "auth.password_changed"
		if encoded == "" {
			action = "auth.password_disabled"
		}
		addEvent(s, model.Event{ActorID: sub.ID, Action: action, ResourceID: sub.ID, Result: "success", RequestID: requestID})
		return nil
	})
}
func (st *Store) SharedNote(sub Subject) (model.SharedNote, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if sub.Role != "owner" && sub.Role != "member" {
		return model.SharedNote{}, ErrDenied
	}
	if !activeSubject(&st.s, sub) {
		return model.SharedNote{}, ErrPrincipalInactive
	}
	return st.s.Note, nil
}
func (st *Store) UpdateSharedNote(sub Subject, q model.NoteWrite, requestID string) (model.SharedNote, error) {
	var out model.SharedNote
	if q.BaseRevision == nil || *q.BaseRevision < 0 || len(q.Text) > MaxNoteBytes || !utf8.ValidString(q.Text) || len(q.CorrelationID) > 128 || !utf8.ValidString(q.CorrelationID) {
		return out, errors.New("baseRevision is required and nonnegative; note must be valid UTF-8, at most 65536 bytes; correlationId at most 128 bytes")
	}
	e := st.mutate(func(s *snapshot) error {
		if sub.Role != "owner" && sub.Role != "member" {
			return ErrDenied
		}
		if !activeSubject(s, sub) {
			return ErrPrincipalInactive
		}
		out = s.Note
		if *q.BaseRevision != s.Note.Revision {
			return &NoteConflict{Current: s.Note}
		}
		if q.Text == s.Note.Text {
			return errNoteUnchanged
		}
		t := time.Now().UTC()
		out = model.SharedNote{ID: "shared", Text: q.Text, Revision: s.Note.Revision + 1, UpdatedBy: sub.ID, UpdatedAt: &t}
		s.Note = out
		addEvent(s, model.Event{ActorID: sub.ID, Action: "note.updated", ResourceID: "shared", Result: "success", CorrelationID: q.CorrelationID, RequestID: requestID})
		return nil
	})
	if errors.Is(e, errNoteUnchanged) {
		e = nil
	}
	return out, e
}
