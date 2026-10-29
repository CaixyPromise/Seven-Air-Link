package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"seven-air-link/internal/model"
	"seven-air-link/internal/sqlite"
	"time"
)

var schema = []string{
	`CREATE TABLE server_state (id INTEGER PRIMARY KEY CHECK(id=1), schema_version INTEGER NOT NULL CHECK(schema_version=4), owner_id TEXT NOT NULL, owner_hash TEXT NOT NULL, seq INTEGER NOT NULL, message_seq INTEGER NOT NULL, message_bytes INTEGER NOT NULL, revision INTEGER NOT NULL)`,
	`CREATE TABLE principals (id TEXT PRIMARY KEY, data TEXT NOT NULL)`,
	`CREATE TABLE devices (id TEXT PRIMARY KEY, data TEXT NOT NULL)`,
	`CREATE TABLE pairings (id TEXT PRIMARY KEY, data TEXT NOT NULL)`,
	`CREATE TABLE services (id TEXT PRIMARY KEY, data TEXT NOT NULL)`,
	`CREATE TABLE files (id TEXT PRIMARY KEY, data TEXT NOT NULL)`,
	`CREATE TABLE messages (seq INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, client_message_id TEXT NOT NULL, sender_id TEXT NOT NULL, recipient_id TEXT NOT NULL, kind TEXT NOT NULL, body TEXT NOT NULL, attachments TEXT NOT NULL, reply_to TEXT NOT NULL, correlation_id TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(sender_id,client_message_id))`,
	`CREATE INDEX messages_recipient_seq ON messages(recipient_id,seq)`,
	`CREATE INDEX messages_sender_seq ON messages(sender_id,seq)`,
	`CREATE INDEX messages_correlation_seq ON messages(correlation_id,seq)`,
	`CREATE TABLE events (seq INTEGER PRIMARY KEY, actor_id TEXT NOT NULL, correlation_id TEXT NOT NULL, data TEXT NOT NULL)`,
	`CREATE INDEX events_actor_seq ON events(actor_id,seq)`,
	`CREATE TABLE migrations (source_sha256 TEXT PRIMARY KEY, source_version INTEGER NOT NULL, migrated_at TEXT NOT NULL)`,
	`PRAGMA user_version=4`,
}

func configureDB(db *sqlite.DB) error {
	rows, e := db.Query("PRAGMA journal_mode=WAL")
	if e != nil {
		return e
	}
	if len(rows) != 1 || rows[0][0] != "wal" {
		return errors.New("SQLite WAL could not be enabled")
	}
	for _, q := range []string{"PRAGMA synchronous=FULL", "PRAGMA wal_autocheckpoint=1000", "PRAGMA temp_store=MEMORY"} {
		if _, e = db.Exec(q); e != nil {
			return e
		}
	}
	return nil
}
func createDB(path string, s snapshot) (*Store, error) {
	db, e := sqlite.Open(path, false)
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	if e = configureDB(db); e != nil {
		return fail(e)
	}
	st := &Store{path: db.Path(), db: db, s: s}
	st.s.Version = 4
	e = db.Transaction(func(tx *sqlite.Tx) error {
		for _, q := range schema {
			if _, e := tx.Exec(q); e != nil {
				return e
			}
		}
		if _, e := tx.Exec("INSERT INTO server_state VALUES(1,4,?,?,?,?,?,?)", s.OwnerID, s.OwnerHash, s.Seq, s.MessageSeq, s.MessageBytes, s.Revision); e != nil {
			return e
		}
		return syncRecords(tx, snapshot{}, st.s)
	})
	if e != nil {
		return fail(e)
	}
	st.s.Messages = []model.Message{}
	st.s.Events = []model.Event{}
	return st, nil
}
func openDB(path string) (*Store, error) {
	if _, e := os.Stat(path); e != nil {
		return nil, e
	} // Never create empty authority on a typo.
	db, e := sqlite.Open(path, false)
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	if e = checkSchema(db); e != nil {
		return fail(e)
	}
	if e = configureDB(db); e != nil {
		return fail(e)
	}
	s, e := readMetadata(db)
	if e != nil {
		return fail(e)
	}
	if e = checkCounts(db, s); e != nil {
		return fail(e)
	}
	return &Store{path: db.Path(), db: db, s: s}, nil
}
func checkSchema(db *sqlite.DB) error {
	rows, e := db.Query("PRAGMA quick_check")
	if e != nil {
		return e
	}
	if len(rows) != 1 || rows[0][0] != "ok" {
		return errors.New("SQLite integrity check failed; restore a verified backup")
	}
	rows, e = db.Query("PRAGMA user_version")
	if e != nil {
		return e
	}
	if len(rows) != 1 || rows[0][0] != int64(4) {
		return errors.New("unsupported database schema; refusing reset")
	}
	return nil
}
func readMetadata(db *sqlite.DB) (snapshot, error) {
	s := snapshot{Messages: []model.Message{}, Events: []model.Event{}}
	rows, e := db.Query("SELECT schema_version,owner_id,owner_hash,seq,message_seq,message_bytes,revision FROM server_state WHERE id=1")
	if e != nil {
		return s, e
	}
	if len(rows) != 1 {
		return s, errors.New("missing authority row")
	}
	r := rows[0]
	s.Version = int(r[0].(int64))
	s.OwnerID = r[1].(string)
	s.OwnerHash = r[2].(string)
	s.Seq = r[3].(int64)
	s.MessageSeq = r[4].(int64)
	s.MessageBytes = r[5].(int64)
	s.Revision = r[6].(int64)
	if s.OwnerID == "" || len(s.OwnerHash) != 64 || s.Version != 4 || s.Seq < 0 || s.MessageSeq < 0 {
		return s, errors.New("corrupt authority metadata")
	}
	if s.Principals, e = loadMap[principalRecord](db, "principals"); e != nil {
		return s, e
	}
	if s.Devices, e = loadMap[deviceRecord](db, "devices"); e != nil {
		return s, e
	}
	if s.Pairings, e = loadMap[pairingRecord](db, "pairings"); e != nil {
		return s, e
	}
	if s.Services, e = loadMap[model.Service](db, "services"); e != nil {
		return s, e
	}
	if s.Files, e = loadMap[model.File](db, "files"); e != nil {
		return s, e
	}
	// Validate authority without loading historical message bodies.
	for id, p := range s.Principals {
		if p.Principal.ID != id || p.Principal.Role != "member" || p.Principal.ExpiresAt == nil || (p.Principal.Type != "human" && p.Principal.Type != "bot") || (p.TokenHash != "" && len(p.TokenHash) != 64) {
			return s, errors.New("corrupt principal metadata")
		}
	}
	for id, d := range s.Devices {
		if id != d.Device.ID || len(d.TokenHash) != 64 {
			return s, errors.New("corrupt device metadata")
		}
	}
	for id, v := range s.Services {
		if id != v.ID {
			return s, errors.New("corrupt service metadata")
		}
	}
	for id, f := range s.Files {
		if id != f.ID || f.Size < 0 || len(f.SHA256) != 64 {
			return s, errors.New("corrupt file metadata")
		}
	}
	return s, nil
}
func checkCounts(db *sqlite.DB, s snapshot) error {
	rows, e := db.Query("SELECT count(*),coalesce(max(seq),0),coalesce(sum(length(CAST(body AS BLOB))),0) FROM messages")
	if e != nil {
		return e
	}
	if len(rows) != 1 || rows[0][0] != s.MessageSeq || rows[0][1] != s.MessageSeq || rows[0][2] != s.MessageBytes {
		return errors.New("message sequence/count/byte accounting mismatch")
	}
	rows, e = db.Query("SELECT coalesce(max(seq),0),count(*) FROM events")
	if e != nil {
		return e
	}
	if rows[0][0] != s.Seq || rows[0][1].(int64) > MaxEvents {
		return errors.New("audit sequence mismatch")
	}
	return nil
}
func loadMap[T any](db *sqlite.DB, table string) (map[string]T, error) {
	out := map[string]T{}
	rows, e := db.Query("SELECT id,data FROM " + table)
	if e != nil {
		return nil, e
	}
	for _, r := range rows {
		var v T
		if e = json.Unmarshal([]byte(r[1].(string)), &v); e != nil {
			return nil, fmt.Errorf("corrupt %s record: %w", table, e)
		}
		out[r[0].(string)] = v
	}
	return out, nil
}
func syncMap[T any](tx *sqlite.Tx, table string, old, next map[string]T) error {
	for id, v := range next {
		if prev, ok := old[id]; ok && reflect.DeepEqual(prev, v) {
			continue
		}
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO "+table+"(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", id, string(b)); e != nil {
			return e
		}
	}
	for id := range old {
		if _, ok := next[id]; !ok {
			if _, e := tx.Exec("DELETE FROM "+table+" WHERE id=?", id); e != nil {
				return e
			}
		}
	}
	return nil
}
func syncRecords(tx *sqlite.Tx, old, next snapshot) error {
	if e := syncMap(tx, "principals", old.Principals, next.Principals); e != nil {
		return e
	}
	if e := syncMap(tx, "devices", old.Devices, next.Devices); e != nil {
		return e
	}
	if e := syncMap(tx, "pairings", old.Pairings, next.Pairings); e != nil {
		return e
	}
	if e := syncMap(tx, "services", old.Services, next.Services); e != nil {
		return e
	}
	if e := syncMap(tx, "files", old.Files, next.Files); e != nil {
		return e
	}
	for _, m := range next.Messages {
		att, e := json.Marshal(m.Attachments)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO messages VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", m.Seq, m.ID, m.ClientMessageID, m.SenderID, m.RecipientID, m.Kind, m.Text, string(att), m.ReplyTo, m.CorrelationID, m.Status, m.CreatedAt.Format(time.RFC3339Nano)); e != nil {
			return e
		}
	}
	for _, ev := range next.Events {
		b, e := json.Marshal(ev)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO events VALUES(?,?,?,?)", ev.Seq, ev.ActorID, ev.CorrelationID, string(b)); e != nil {
			return e
		}
	}
	_, e := tx.Exec("DELETE FROM events WHERE seq<=?", next.Seq-MaxEvents)
	return e
}
func (st *Store) write(next snapshot) error {
	// The store path is immutable. This guard also prevents misdirected writes if a
	// caller accidentally reuses a Store after changing its data directory.
	abs, e := filepath.Abs(st.path)
	if e != nil {
		return e
	}
	if abs != st.db.Path() {
		return errors.New("store path changed")
	}
	return st.db.Transaction(func(tx *sqlite.Tx) error {
		n, e := tx.Exec("UPDATE server_state SET seq=?,message_seq=?,message_bytes=?,revision=? WHERE id=1 AND revision=?", next.Seq, next.MessageSeq, next.MessageBytes, next.Revision, st.s.Revision)
		if e != nil {
			return e
		}
		if n != 1 {
			return errors.New("stale store revision; another writer changed authority")
		}
		return syncRecords(tx, st.s, next)
	})
}

const messageColumns = "seq,id,client_message_id,sender_id,recipient_id,kind,body,attachments,reply_to,correlation_id,status,created_at"

func scanMessages(rows []sqlite.Row) ([]model.Message, error) {
	out := make([]model.Message, 0, len(rows))
	for _, r := range rows {
		m := model.Message{Seq: r[0].(int64), ID: r[1].(string), ClientMessageID: r[2].(string), SenderID: r[3].(string), RecipientID: r[4].(string), Kind: r[5].(string), Text: r[6].(string), ReplyTo: r[8].(string), CorrelationID: r[9].(string), Status: r[10].(string)}
		if e := json.Unmarshal([]byte(r[7].(string)), &m.Attachments); e != nil {
			return nil, e
		}
		t, e := time.Parse(time.RFC3339Nano, r[11].(string))
		if e != nil {
			return nil, e
		}
		m.CreatedAt = t
		out = append(out, m)
	}
	return out, nil
}
func (st *Store) queryMessages(where string, args ...any) ([]model.Message, error) {
	rows, e := st.db.Query("SELECT "+messageColumns+" FROM messages "+where, args...)
	if e != nil {
		return nil, e
	}
	return scanMessages(rows)
}
func (st *Store) queryEvents(query string, args ...any) ([]model.Event, error) {
	rows, e := st.db.Query(query, args...)
	if e != nil {
		return nil, e
	}
	out := make([]model.Event, 0, len(rows))
	for _, r := range rows {
		var ev model.Event
		if e = json.Unmarshal([]byte(r[0].(string)), &ev); e != nil {
			return nil, e
		}
		out = append(out, ev)
	}
	return out, nil
}
