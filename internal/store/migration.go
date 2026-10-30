package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"time"
)

type MigrationReport struct {
	SourceVersion int    `json:"sourceVersion"`
	SourceSHA256  string `json:"sourceSHA256"`
	Messages      int    `json:"messages"`
	Files         int    `json:"files"`
	Database      string `json:"database"`
	Backup        string `json:"backup"`
	Resumed       bool   `json:"resumed"`
}

func digest(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func readLegacy(path string) (snapshot, []byte, error) {
	var s snapshot
	fi, e := os.Lstat(path)
	if e != nil {
		return s, nil, e
	}
	if !fi.Mode().IsRegular() || fi.Size() > 96<<20 {
		return s, nil, errors.New("legacy metadata must be a regular file under 96 MiB")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return s, nil, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, nil, fmt.Errorf("invalid legacy JSON: %w", e)
	}
	if s.Version < 1 || s.Version > 3 || s.OwnerID == "" || len(s.OwnerHash) != 64 || s.Devices == nil || s.Pairings == nil {
		return s, nil, errors.New("unsupported/corrupt legacy authority; never reset it")
	}
	if s.Version == 1 {
		s.Services = map[string]model.Service{}
	}
	if s.Version < 3 {
		s.Principals = map[string]principalRecord{}
		s.Messages = []model.Message{}
		s.Files = map[string]model.File{}
	}
	if s.Services == nil || s.Principals == nil || s.Messages == nil || s.Files == nil {
		return s, nil, errors.New("missing legacy collections")
	}
	if e = validateCollaboration(s); e != nil {
		return s, nil, e
	}
	for id, d := range s.Devices {
		if d.Device.ID != id || len(d.TokenHash) != 64 {
			return s, nil, errors.New("corrupt legacy device")
		}
	}
	for id, f := range s.Files {
		if id != f.ID || f.Size < 0 || f.Size > MaxFileBytes || !ValidFileName(f.Name) || len(f.SHA256) != 64 {
			return s, nil, errors.New("corrupt legacy attachment")
		}
	}
	last := int64(0)
	for _, ev := range s.Events {
		if ev.Seq <= last || ev.Seq > s.Seq {
			return s, nil, errors.New("corrupt legacy audit sequence")
		}
		last = ev.Seq
	}
	if last != s.Seq || len(s.Events) > MaxEvents {
		return s, nil, errors.New("corrupt legacy audit head")
	}
	return s, b, nil
}
func syncDir(dir string) error {
	// Windows does not support fsync on directory handles via os.File.Sync.
	// Data files are synced before rename; directory sync is best-effort.
	if runtime.GOOS == "windows" {
		return nil
	}
	f, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func durableBackup(path string, b []byte) error {
	if existing, e := os.ReadFile(path); e == nil {
		if digest(existing) != digest(b) {
			return errors.New("existing migration backup differs; refusing overwrite")
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	return secure.AtomicWrite(path, b, 0600)
}
func writeMigrationMarker(path, hash string) error {
	return secure.WriteJSON(path, map[string]any{"version": 4, "storage": "bridge.db", "sourceSHA256": hash, "notice": "Migrated to SQLite. M3 must not write this directory. Restore a complete pre-upgrade backup for rollback."})
}

// MigrateJSON runs with the Server directory lock held. It retains the exact
// source bytes, validates a separate SQLite file, writes a downgrade guard, then
// atomically publishes the DB. A sealed pending DB is resumable after a crash.
func MigrateJSON(source, dest string) (*Store, MigrationReport, error) {
	report := MigrationReport{Database: dest, Backup: source + ".pre-sqlite.bak"}
	if filepath.Clean(source) == filepath.Clean(dest) {
		return nil, report, errors.New("migration paths must differ")
	}
	pending := dest + ".migrating"
	if _, e := os.Stat(dest); e == nil {
		return nil, report, errors.New("destination database already exists")
	}
	if _, e := os.Stat(pending); e == nil {
		st, err := Open(pending)
		if err != nil {
			return nil, report, fmt.Errorf("incomplete migration database; original JSON/backup is untouched: %w", err)
		}
		rows, err := st.db.Query("SELECT source_sha256,source_version FROM migrations")
		if err != nil || len(rows) != 1 {
			st.Close()
			return nil, report, errors.New("unsealed migration; inspect pending file and preserve original backup")
		}
		hash := rows[0][0].(string)
		b, err := os.ReadFile(report.Backup)
		if err != nil || digest(b) != hash {
			st.Close()
			return nil, report, errors.New("migration backup hash mismatch")
		}
		current, err := os.ReadFile(source)
		if err != nil {
			st.Close()
			return nil, report, err
		}
		var marker struct {
			Version      int    `json:"version"`
			SourceSHA256 string `json:"sourceSHA256"`
		}
		_ = json.Unmarshal(current, &marker)
		if digest(current) != hash && (marker.Version != 4 || marker.SourceSHA256 != hash) {
			st.Close()
			return nil, report, errors.New("legacy source changed since migration")
		}
		report.SourceVersion = int(rows[0][1].(int64))
		report.SourceSHA256 = hash
		report.Messages = int(st.s.MessageSeq)
		report.Files = len(st.s.Files)
		report.Resumed = true
		if err = writeMigrationMarker(source, hash); err != nil {
			st.Close()
			return nil, report, err
		}
		if _, err = st.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			st.Close()
			return nil, report, err
		}
		if err = st.Close(); err != nil {
			return nil, report, err
		}
		if err = os.Rename(pending, dest); err != nil {
			return nil, report, err
		}
		if err = syncDir(filepath.Dir(dest)); err != nil {
			return nil, report, err
		}
		st, err = Open(dest)
		return st, report, err
	} else if !os.IsNotExist(e) {
		return nil, report, e
	}
	s, b, e := readLegacy(source)
	if e != nil {
		return nil, report, e
	}
	report.SourceVersion = s.Version
	report.SourceSHA256 = digest(b)
	report.Messages = len(s.Messages)
	report.Files = len(s.Files)
	if e = durableBackup(report.Backup, b); e != nil {
		return nil, report, e
	}
	st, e := createDB(pending, s)
	if e != nil {
		return nil, report, e
	}
	fail := func(e error) (*Store, MigrationReport, error) { st.Close(); return nil, report, e }
	if e = checkCounts(st.db, st.s); e != nil {
		return fail(e)
	}
	// Compare every message, including Unicode, reply IDs and attachment refs.
	ms, e := st.queryMessages("ORDER BY seq")
	if e != nil {
		return fail(e)
	}
	original, _ := json.Marshal(s.Messages)
	actual, _ := json.Marshal(ms)
	if string(original) != string(actual) {
		return fail(errors.New("message round-trip verification failed"))
	}
	if _, e = st.db.Exec("INSERT INTO migrations VALUES(?,?,?)", report.SourceSHA256, report.SourceVersion, time.Now().UTC().Format(time.RFC3339Nano)); e != nil {
		return fail(e)
	}
	if _, e = st.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
		return fail(e)
	}
	if e = st.Close(); e != nil {
		return nil, report, e
	}
	if e = writeMigrationMarker(source, report.SourceSHA256); e != nil {
		return nil, report, e
	}
	if e = os.Rename(pending, dest); e != nil {
		return nil, report, e
	}
	if e = syncDir(filepath.Dir(dest)); e != nil {
		return nil, report, e
	}
	opened, e := Open(dest)
	return opened, report, e
}

// ExportLegacy exports current data for inspection/interchange. A full rollback
// also requires the certificate, credentials and attachment bytes, not just JSON.
func (st *Store) ExportLegacy() ([]byte, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	s := cloneMetadata(st.s)
	s.Version = 3
	s.Revision = 0
	ms, e := st.queryMessages("ORDER BY seq")
	if e != nil {
		return nil, e
	}
	s.Messages = ms
	ev, e := st.queryEvents("SELECT data FROM events ORDER BY seq")
	if e != nil {
		return nil, e
	}
	s.Events = ev
	if e = validateCollaboration(s); e != nil {
		return nil, e
	}
	return json.MarshalIndent(s, "", "  ")
}
