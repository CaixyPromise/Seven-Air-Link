package store

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/sqlite"
	"sort"
	"strings"
	"time"
)

var fileKeyRE = regexp.MustCompile(`^file_[A-Za-z0-9_-]{24}$`)

type FileIssue struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}
type StorageReport struct {
	OK            bool        `json:"ok"`
	Engine        string      `json:"engine"`
	SQLiteVersion string      `json:"sqliteVersion"`
	Schema        int         `json:"schema"`
	Integrity     string      `json:"integrity"`
	Messages      int64       `json:"messages"`
	MessageBytes  int64       `json:"messageBytes"`
	EventSeq      int64       `json:"eventSeq"`
	Files         int         `json:"files"`
	FileBytes     int64       `json:"fileBytes"`
	FileIssues    []FileIssue `json:"fileIssues"`
	HashesChecked bool        `json:"hashesChecked"`
	DatabaseBytes int64       `json:"databaseBytes"`
	WALBytes      int64       `json:"walBytes"`
}

func (st *Store) Stats() (StorageReport, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.statsLocked()
}
func (st *Store) statsLocked() (StorageReport, error) {
	r := StorageReport{OK: true, Engine: "sqlite", SQLiteVersion: sqlite.Version(), Schema: 4, Integrity: "not_run", Messages: st.s.MessageSeq, MessageBytes: st.s.MessageBytes, EventSeq: st.s.Seq, FileIssues: []FileIssue{}}
	if _, e := st.db.Query("SELECT 1 FROM server_state"); e != nil {
		return r, e
	}
	for _, f := range st.s.Files {
		if f.DeletedAt == nil {
			r.Files++
			r.FileBytes += f.Size
		}
	}
	if fi, e := os.Stat(st.path); e == nil {
		r.DatabaseBytes = fi.Size()
	}
	if fi, e := os.Stat(st.path + "-wal"); e == nil {
		r.WALBytes = fi.Size()
	}
	return r, nil
}
func hashFile(path string) (string, int64, error) {
	fi, e := os.Lstat(path)
	if e != nil {
		return "", 0, e
	}
	if !fi.Mode().IsRegular() {
		return "", 0, errors.New("not a regular file")
	}
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}
func attachmentIssues(dir string, files map[string]model.File, hashes bool) []FileIssue {
	issues := []FileIssue{}
	if fi, e := os.Lstat(dir); e == nil && !fi.IsDir() {
		return append(issues, FileIssue{ID: "uploads", Code: "unsafe_directory"})
	}
	for id, f := range files {
		if f.DeletedAt != nil {
			continue
		}
		if !fileKeyRE.MatchString(id) {
			issues = append(issues, FileIssue{id, "invalid_storage_key"})
			continue
		}
		fi, e := os.Lstat(filepath.Join(dir, id))
		if e != nil {
			issues = append(issues, FileIssue{id, "missing"})
			continue
		}
		if !fi.Mode().IsRegular() {
			issues = append(issues, FileIssue{id, "non_regular"})
			continue
		}
		if fi.Size() != f.Size {
			issues = append(issues, FileIssue{id, "size_mismatch"})
			continue
		}
		if hashes {
			h, _, e := hashFile(filepath.Join(dir, id))
			if e != nil || h != f.SHA256 {
				issues = append(issues, FileIssue{id, "hash_mismatch"})
			}
		}
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].ID < issues[j].ID })
	return issues
}
func (st *Store) Check(hashes bool) (StorageReport, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	r, e := st.statsLocked()
	if e != nil {
		return r, e
	}
	if e = checkSchema(st.db); e != nil {
		return r, e
	}
	if e = checkCounts(st.db, st.s); e != nil {
		return r, e
	}
	rows, e := st.db.Query("PRAGMA foreign_key_check")
	if e != nil {
		return r, e
	}
	if len(rows) != 0 {
		return r, errors.New("foreign key check failed")
	}
	r.Integrity = "ok"
	r.HashesChecked = hashes
	r.FileIssues = attachmentIssues(st.FileDir(), st.s.Files, hashes)
	r.OK = len(r.FileIssues) == 0
	return r, nil
}

type GCCandidate struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Bytes  int64  `json:"bytes"`
}
type GCReport struct {
	Applied        bool          `json:"applied"`
	Candidates     []GCCandidate `json:"candidates"`
	Removed        int           `json:"removed"`
	ReclaimedBytes int64         `json:"reclaimedBytes"`
	Skipped        []string      `json:"skipped"`
	Failures       []string      `json:"failures"`
}

// GC is an offline operation under server.lock. Default preview never deletes.
// It never follows symlinks, removes a live referenced file, or recursively walks.
func (st *Store) GC(minAge time.Duration, apply bool) (GCReport, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	r := GCReport{Applied: apply, Candidates: []GCCandidate{}, Skipped: []string{}, Failures: []string{}}
	if minAge < 0 {
		return r, errors.New("min-age must be nonnegative")
	}
	dir := st.FileDir()
	fi, e := os.Lstat(dir)
	if os.IsNotExist(e) {
		return r, nil
	}
	if e != nil {
		return r, e
	}
	if !fi.IsDir() {
		return r, errors.New("unsafe upload directory")
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return r, e
	}
	now := time.Now()
	for _, ent := range entries {
		name := ent.Name()
		info, e := ent.Info()
		if e != nil {
			r.Failures = append(r.Failures, name)
			continue
		}
		if !info.Mode().IsRegular() {
			r.Skipped = append(r.Skipped, name)
			continue
		}
		reason := ""
		if f, ok := st.s.Files[name]; ok {
			if f.DeletedAt == nil {
				continue
			}
			reason = "deleted_record"
		} else if strings.HasPrefix(name, ".upload-") && now.Sub(info.ModTime()) >= minAge {
			reason = "stale_partial"
		} else if fileKeyRE.MatchString(name) && now.Sub(info.ModTime()) >= minAge {
			reason = "unreferenced"
		} else {
			r.Skipped = append(r.Skipped, name)
			continue
		}
		r.Candidates = append(r.Candidates, GCCandidate{name, reason, info.Size()})
		if apply {
			if e = os.Remove(filepath.Join(dir, name)); e != nil && !os.IsNotExist(e) {
				r.Failures = append(r.Failures, name)
			} else {
				r.Removed++
				r.ReclaimedBytes += info.Size()
			}
		}
	}
	if apply {
		if e = syncDir(dir); e != nil {
			return r, e
		}
	}
	return r, nil
}

type BackupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type BackupManifest struct {
	Format    int          `json:"format"`
	Schema    int          `json:"schema"`
	CreatedAt time.Time    `json:"createdAt"`
	Files     []BackupFile `json:"files"`
}
type BackupReport struct {
	OK        bool   `json:"ok"`
	Path      string `json:"path"`
	Schema    int    `json:"schema"`
	FileCount int    `json:"fileCount"`
	Bytes     int64  `json:"bytes"`
	Sensitive bool   `json:"containsCredentials"`
}

func privateDir(path string) error {
	fi, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !fi.IsDir() {
		return errors.New("directory must not be a symlink")
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0077 != 0 {
		return errors.New("data/backup directory must have mode 0700")
	}
	return nil
}
func safeRelative(p string) bool {
	if p == "" || strings.Contains(p, "\\") || strings.Contains(p, ":") || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	if strings.HasPrefix(p, "uploads/") {
		return fileKeyRE.MatchString(strings.TrimPrefix(p, "uploads/"))
	}
	switch p {
	case "bridge.db", "metadata.json", "server.crt", "server.key", "admin.json":
		return true
	}
	return false
}
func copyPrivate(src, dest string) (BackupFile, error) {
	var r BackupFile
	fi, e := os.Lstat(src)
	if e != nil {
		return r, e
	}
	if !fi.Mode().IsRegular() {
		return r, errors.New("backup refuses symlinks/non-regular files")
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return r, e
	}
	in, e := os.Open(src)
	if e != nil {
		return r, e
	}
	defer in.Close()
	out, e := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return r, e
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(out, h), in)
	if e == nil {
		e = out.Sync()
	}
	ce := out.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return r, e
	}
	r.Size = n
	r.SHA256 = hex.EncodeToString(h.Sum(nil))
	return r, nil
}
func ensureDestination(source, dest string) error {
	s, e := filepath.EvalSymlinks(source)
	if e != nil {
		return e
	}
	s, e = filepath.Abs(s)
	if e != nil {
		return e
	}
	d, e := filepath.Abs(dest)
	if e != nil {
		return e
	}
	// Resolve existing parent components before ancestry checks. A symlink alias
	// into the source must not turn an "outside" name into an in-place backup.
	parent := filepath.Dir(d)
	var missing []string
	for {
		if _, err := os.Lstat(parent); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, filepath.Base(parent))
		next := filepath.Dir(parent)
		if next == parent {
			return errors.New("no existing destination ancestor")
		}
		parent = next
	}
	resolved, e := filepath.EvalSymlinks(parent)
	if e != nil {
		return e
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	d = filepath.Join(resolved, filepath.Base(d))
	rel, e := filepath.Rel(s, d)
	if e != nil {
		return e
	}
	if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("destination must be outside source directory")
	}
	if _, e = os.Lstat(d); !os.IsNotExist(e) {
		return errors.New("destination must not exist (no overwrite)")
	}
	return nil
}

// Backup must be called with the source Server lock held. VACUUM INTO takes a
// consistent SQL snapshot, then attachments, cert and credentials are copied.
func Backup(data, dest string) (BackupReport, error) {
	r := BackupReport{Path: dest, Sensitive: true}
	if e := privateDir(data); e != nil {
		return r, e
	}
	if e := ensureDestination(data, dest); e != nil {
		return r, e
	}
	if e := os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return r, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(dest), ".slb-backup-")
	if e != nil {
		return r, e
	}
	defer os.RemoveAll(stage)
	manifest := BackupManifest{Format: 1, CreatedAt: time.Now().UTC(), Files: []BackupFile{}}
	var snap snapshot
	dbpath := filepath.Join(data, "bridge.db")
	if _, e = os.Stat(dbpath); e == nil {
		st, e := Open(dbpath)
		if e != nil {
			return r, e
		}
		check, e := st.Check(true)
		if e != nil || !check.OK {
			st.Close()
			return r, fmt.Errorf("source storage invalid: %v; issues=%v", e, check.FileIssues)
		}
		snap = st.s
		manifest.Schema = 4
		target := filepath.Join(stage, "bridge.db")
		f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			st.Close()
			return r, e
		}
		f.Close()
		_, e = st.db.Exec("VACUUM INTO ?", target)
		st.Close()
		if e != nil {
			return r, e
		}
		// A backup is a standalone database, not a DB file depending on a separate WAL.
		db, e := sqlite.Open(target, false)
		if e != nil {
			return r, e
		}
		_, e = db.Exec("PRAGMA journal_mode=DELETE")
		db.Close()
		if e != nil {
			return r, e
		}
		f, e = os.OpenFile(target, os.O_RDWR, 0600)
		if e != nil {
			return r, e
		}
		e = f.Sync()
		f.Close()
		if e != nil {
			return r, e
		}
		h, n, e := hashFile(target)
		if e != nil {
			return r, e
		}
		manifest.Files = append(manifest.Files, BackupFile{"bridge.db", n, h})
	} else if os.IsNotExist(e) {
		var raw []byte
		snap, raw, e = readLegacy(filepath.Join(data, "metadata.json"))
		if e != nil {
			return r, e
		}
		_ = raw
		manifest.Schema = snap.Version
		if issues := attachmentIssues(filepath.Join(data, "uploads"), snap.Files, true); len(issues) > 0 {
			return r, fmt.Errorf("source attachments invalid: %v", issues)
		}
	} else {
		return r, e
	}
	names := []string{"server.crt", "server.key", "admin.json"}
	if _, e = os.Stat(filepath.Join(data, "metadata.json")); e == nil {
		names = append(names, "metadata.json")
	}
	for id, f := range snap.Files {
		if f.DeletedAt == nil {
			names = append(names, "uploads/"+id)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		bf, e := copyPrivate(filepath.Join(data, name), filepath.Join(stage, name))
		if e != nil {
			return r, e
		}
		bf.Path = name
		manifest.Files = append(manifest.Files, bf)
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	if e = secure.WriteJSON(filepath.Join(stage, "manifest.json"), manifest); e != nil {
		return r, e
	}
	if _, e = VerifyBackup(stage); e != nil {
		return r, e
	}
	if fi, err := os.Lstat(filepath.Join(stage, "uploads")); err == nil && fi.IsDir() {
		if e = syncDir(filepath.Join(stage, "uploads")); e != nil {
			return r, e
		}
	}
	if e = syncDir(stage); e != nil {
		return r, e
	}
	if e = ensureDestination(data, dest); e != nil {
		return r, e
	}
	if e = os.Rename(stage, dest); e != nil {
		return r, e
	}
	if e = syncDir(filepath.Dir(dest)); e != nil {
		return r, e
	}
	r.OK = true
	r.Schema = manifest.Schema
	r.FileCount = len(manifest.Files)
	for _, bf := range manifest.Files {
		r.Bytes += bf.Size
	}
	return r, nil
}
func VerifyBackup(dir string) (BackupReport, error) {
	r := BackupReport{Path: dir, Sensitive: true}
	if e := privateDir(dir); e != nil {
		return r, e
	}
	mf := filepath.Join(dir, "manifest.json")
	fi, e := os.Lstat(mf)
	if e != nil {
		return r, e
	}
	if !fi.Mode().IsRegular() || fi.Size() > 2<<20 {
		return r, errors.New("invalid backup manifest")
	}
	raw, e := os.ReadFile(mf)
	if e != nil {
		return r, e
	}
	var m BackupManifest
	if e = json.Unmarshal(raw, &m); e != nil {
		return r, e
	}
	if m.Format != 1 || m.Schema < 1 || m.Schema > 4 || len(m.Files) > 5000 {
		return r, errors.New("unsupported backup manifest")
	}
	listed := map[string]bool{"manifest.json": true}
	for _, bf := range m.Files {
		if !safeRelative(bf.Path) || listed[bf.Path] || bf.Size < 0 {
			return r, errors.New("invalid/duplicate backup path")
		}
		listed[bf.Path] = true
		h, n, e := hashFile(filepath.Join(dir, bf.Path))
		if e != nil || h != bf.SHA256 || n != bf.Size {
			return r, fmt.Errorf("backup hash/size verification failed: %s", bf.Path)
		}
		r.Bytes += n
	}
	e = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			if rel != "." && rel != "uploads" {
				return errors.New("unexpected backup directory")
			}
			return nil
		}
		if !listed[filepath.ToSlash(rel)] || d.Type()&os.ModeSymlink != 0 {
			return errors.New("unexpected/symlink backup file")
		}
		return nil
	})
	if e != nil {
		return r, e
	}
	var snap snapshot
	if m.Schema == 4 {
		db, e := sqlite.Open(filepath.Join(dir, "bridge.db"), true)
		if e != nil {
			return r, e
		}
		defer db.Close()
		if e = checkSchema(db); e != nil {
			return r, e
		}
		snap, e = readMetadata(db)
		if e != nil {
			return r, e
		}
		if e = checkCounts(db, snap); e != nil {
			return r, e
		}
	} else {
		snap, _, e = readLegacy(filepath.Join(dir, "metadata.json"))
		if e != nil {
			return r, e
		}
	}
	if issues := attachmentIssues(filepath.Join(dir, "uploads"), snap.Files, true); len(issues) > 0 {
		return r, fmt.Errorf("backup attachments invalid: %v", issues)
	}
	var cfg model.Config
	b, e := os.ReadFile(filepath.Join(dir, "admin.json"))
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &cfg); e != nil {
		return r, e
	}
	if cfg.SubjectID != snap.OwnerID || cfg.Role != "owner" || !secure.Equal(secure.Hash(cfg.Token), snap.OwnerHash) {
		return r, errors.New("backup Owner credential does not match database")
	}
	cert, e := secure.ReadCertificate(filepath.Join(dir, "server.crt"))
	if e != nil {
		return r, e
	}
	if cfg.Fingerprint != secure.Fingerprint(cert.Raw) {
		return r, errors.New("backup certificate does not match pinned identity")
	}
	if _, e = tls.LoadX509KeyPair(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")); e != nil {
		return r, e
	}
	r.OK = true
	r.Schema = m.Schema
	r.FileCount = len(m.Files)
	return r, nil
}

// Restore only publishes a new destination; it never overwrites a running or
// existing Server. Re-verification of the staged copy detects changes in source.
func RestoreBackup(source, dest string) (BackupReport, error) {
	r, e := VerifyBackup(source)
	if e != nil {
		return r, e
	}
	if e = ensureDestination(source, dest); e != nil {
		return r, e
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return r, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(dest), ".slb-restore-")
	if e != nil {
		return r, e
	}
	defer os.RemoveAll(stage)
	raw, e := os.ReadFile(filepath.Join(source, "manifest.json"))
	if e != nil {
		return r, e
	}
	var m BackupManifest
	if e = json.Unmarshal(raw, &m); e != nil {
		return r, e
	}
	for _, bf := range m.Files {
		if _, e = copyPrivate(filepath.Join(source, bf.Path), filepath.Join(stage, bf.Path)); e != nil {
			return r, e
		}
	}
	if e = secure.AtomicWrite(filepath.Join(stage, "manifest.json"), raw, 0600); e != nil {
		return r, e
	}
	if _, e = VerifyBackup(stage); e != nil {
		return r, e
	}
	// Manifest remains as restoration evidence; future backups only include live state.
	if fi, err := os.Lstat(filepath.Join(stage, "uploads")); err == nil && fi.IsDir() {
		if e = syncDir(filepath.Join(stage, "uploads")); e != nil {
			return r, e
		}
	}
	if e = syncDir(stage); e != nil {
		return r, e
	}
	if e = ensureDestination(source, dest); e != nil {
		return r, e
	}
	if e = os.Rename(stage, dest); e != nil {
		return r, e
	}
	if e = syncDir(filepath.Dir(dest)); e != nil {
		return r, e
	}
	r.Path = dest
	return r, nil
}
