package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"strings"
	"testing"
	"time"
)

func serverFixture(t *testing.T) (*Store, string, Subject) {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	token := secure.Random("owner_")
	st, e := New(filepath.Join(dir, "bridge.db"), "human_owner", token)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	c, e := secure.MakeCertificate(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"), nil)
	if e != nil {
		t.Fatal(e)
	}
	e = secure.WriteJSON(filepath.Join(dir, "admin.json"), model.Config{Version: 1, Role: "owner", SubjectID: "human_owner", Token: token, Fingerprint: secure.Fingerprint(c.Raw), Server: "https://localhost:7777"})
	if e != nil {
		t.Fatal(e)
	}
	a, _ := st.Auth(token)
	return st, dir, a
}
func fixtureFile(t *testing.T, st *Store, sub Subject, text string) model.File {
	t.Helper()
	f := model.File{ID: secure.Random("file_")[:29], Name: "报告.txt", Size: int64(len(text)), SHA256: digest([]byte(text)), RecipientID: "shared"}
	if e := os.MkdirAll(st.FileDir(), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(st.FileDir(), f.ID), []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
	if e := st.PutFile(sub, f, ""); e != nil {
		t.Fatal(e)
	}
	out, e := st.File(sub, f.ID)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestSQLiteAtomicMessageAuditFailure(t *testing.T) {
	st, _ := testStore(t)
	a, _ := member(t, st, "a", "bot", "")
	before := st.s.Seq
	_, e := st.db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'injected audit failure'); END")
	if e != nil {
		t.Fatal(e)
	}
	q := model.MessageSend{ClientMessageID: "same-key", To: "shared", Text: "不可丢失"}
	if _, _, e = st.Send(a, q, ""); e == nil {
		t.Fatal("expected transaction abort")
	}
	rows, e := st.db.Query("SELECT count(*) FROM messages")
	if e != nil || rows[0][0] != int64(0) || st.s.MessageSeq != 0 || st.s.Seq != before {
		t.Fatal("partial commit", rows, e)
	}
	if _, e = st.db.Exec("DROP TRIGGER reject_audit"); e != nil {
		t.Fatal(e)
	}
	if _, dup, e := st.Send(a, q, ""); e != nil || dup {
		t.Fatal(dup, e)
	}
	if _, dup, e := st.Send(a, q, ""); e != nil || !dup {
		t.Fatal(dup, e)
	}
	if len(st.s.Messages) > 0 || len(st.s.Events) > 0 {
		t.Fatal("history cached in authority snapshot")
	}
}
func TestSQLiteUniqueIndexAndIndexedPaging(t *testing.T) {
	st, _ := testStore(t)
	a, _ := member(t, st, "a", "bot", "")
	for i := 0; i < 100; i++ {
		textSend(t, st, a, strings.Repeat("k", i+1), "shared", "汉字")
	}
	rows, e := st.db.Query("EXPLAIN QUERY PLAN SELECT seq FROM messages WHERE sender_id=? AND client_message_id=?", a.ID, "k")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(rows)
	if !strings.Contains(string(raw), "INDEX") {
		t.Fatal("no idempotency index", string(raw))
	}
	page, e := st.Messages(a, "shared", 50, 0, 10)
	if e != nil || len(page.Messages) != 10 || page.Messages[0].Seq != 51 || !page.HasMore {
		t.Fatal(page, e)
	}
	if e = checkCounts(st.db, st.s); e != nil {
		t.Fatal(e)
	}
}
func TestMigrationV3RoundTripAndDowngradeGuard(t *testing.T) {
	st, _, owner := serverFixture(t)
	a, _ := member(t, st, "测试Bot", "bot", "")
	m := textSend(t, st, a, "key", "shared", "UTF-8\n消息")
	_ = owner
	raw, e := st.ExportLegacy()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "metadata.json")
	os.WriteFile(src, raw, 0600)
	next, report, e := MigrateJSON(src, filepath.Join(dir, "bridge.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if report.SourceSHA256 != digest(raw) || report.SourceVersion != 3 {
		t.Fatal(report)
	}
	msg, e := next.Message(a, m.ID)
	if e != nil || msg.Text != m.Text {
		t.Fatal(msg, e)
	}
	var guard map[string]any
	b, _ := os.ReadFile(src)
	json.Unmarshal(b, &guard)
	if guard["version"] != float64(4) {
		t.Fatal("old writer not guarded")
	}
	if _, _, e = readLegacy(src); e == nil {
		t.Fatal("M3 source reader accepted downgrade guard")
	}
	b, _ = os.ReadFile(report.Backup)
	if string(b) != string(raw) {
		t.Fatal("backup changed")
	}
}
func TestMigrationResumesSealedDatabase(t *testing.T) {
	st, _ := testStore(t)
	raw, e := st.ExportLegacy()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "metadata.json")
	dest := filepath.Join(dir, "bridge.db")
	os.WriteFile(src, raw, 0600)
	os.WriteFile(src+".pre-sqlite.bak", raw, 0600)
	snap, _, e := readLegacy(src)
	if e != nil {
		t.Fatal(e)
	}
	pending, e := createDB(dest+".migrating", snap)
	if e != nil {
		t.Fatal(e)
	}
	_, e = pending.db.Exec("INSERT INTO migrations VALUES(?,?,?)", digest(raw), 3, time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		t.Fatal(e)
	}
	pending.Close()
	if e = writeMigrationMarker(src, digest(raw)); e != nil {
		t.Fatal(e)
	}
	migrated, r, e := MigrateJSON(src, dest)
	if e != nil {
		t.Fatal(e)
	}
	defer migrated.Close()
	if !r.Resumed {
		t.Fatal("didn't resume")
	}
}
func TestSQLiteBackupRestoreAndTamper(t *testing.T) {
	st, dir, owner := serverFixture(t)
	a, _ := member(t, st, "bot", "bot", "")
	m := textSend(t, st, a, "m", owner.ID, "private Chinese 中文")
	file := fixtureFile(t, st, a, "payload汉字")
	backup := filepath.Join(t.TempDir(), "backup")
	r, e := Backup(dir, backup)
	if e != nil || !r.OK {
		t.Fatal(r, e)
	}
	if _, e = VerifyBackup(backup); e != nil {
		t.Fatal(e)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, e = RestoreBackup(backup, restored); e != nil {
		t.Fatal(e)
	}
	next, e := Open(filepath.Join(restored, "bridge.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	got, e := next.Message(a, m.ID)
	if e != nil || got.Text != m.Text {
		t.Fatal(got, e)
	}
	check, e := next.Check(true)
	if e != nil || !check.OK {
		t.Fatal(check, e)
	}
	if _, e = RestoreBackup(backup, restored); e == nil {
		t.Fatal("overwrote destination")
	}
	p := filepath.Join(backup, "uploads", file.ID)
	if e = os.WriteFile(p, []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = VerifyBackup(backup); e == nil {
		t.Fatal("tampering accepted")
	}
	if _, e = RestoreBackup(backup, filepath.Join(t.TempDir(), "bad")); e == nil {
		t.Fatal("tampered restore accepted")
	}
}
func TestBackupRejectsPathTraversalAndSymlink(t *testing.T) {
	st, dir, _ := serverFixture(t)
	_ = st
	backup := filepath.Join(t.TempDir(), "b")
	if _, e := Backup(dir, backup); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(filepath.Join(backup, "manifest.json"))
	var m BackupManifest
	json.Unmarshal(raw, &m)
	m.Files[0].Path = "../outside"
	secure.WriteJSON(filepath.Join(backup, "manifest.json"), m)
	if _, e := VerifyBackup(backup); e == nil {
		t.Fatal("traversal accepted")
	}
	secure.AtomicWrite(filepath.Join(backup, "manifest.json"), raw, 0600)
	rogue := filepath.Join(backup, "surprise")
	if e := os.Symlink(filepath.Join(dir, "admin.json"), rogue); e != nil {
		t.Logf("symlink unavailable, testing unlisted regular file: %v", e)
		if e = os.WriteFile(rogue, []byte("unexpected"), 0600); e != nil { t.Fatal(e) }
	}
	if _, e := VerifyBackup(backup); e == nil {
		t.Fatal("unexpected symlink accepted")
	}
}
func TestGCPreviewApplyAndReferenceSafety(t *testing.T) {
	st, _, owner := serverFixture(t)
	live := fixtureFile(t, st, owner, "live")
	dead := fixtureFile(t, st, owner, "dead")
	if e := st.DeleteFile(owner, dead.ID, ""); e != nil {
		t.Fatal(e)
	}
	orphan := secure.Random("file_")[:29]
	partial := ".upload-old"
	young := ".upload-young"
	for _, name := range []string{orphan, partial, young} {
		os.WriteFile(filepath.Join(st.FileDir(), name), []byte("junk"), 0600)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{orphan, partial} {
		os.Chtimes(filepath.Join(st.FileDir(), name), old, old)
	}
	outside := filepath.Join(t.TempDir(), "precious")
	os.WriteFile(outside, []byte("keep"), 0600)
	link := filepath.Join(st.FileDir(), ".upload-link")
	if e := os.Symlink(outside, link); e != nil {
		t.Logf("symlink unavailable, testing unknown file preservation: %v", e)
		if e = os.WriteFile(link, []byte("unknown"), 0600); e != nil { t.Fatal(e) }
	}
	r, e := st.GC(24*time.Hour, false)
	if e != nil || len(r.Candidates) != 3 || r.Removed != 0 {
		t.Fatal(r, e)
	}
	for _, name := range []string{live.ID, dead.ID, orphan, partial} {
		if _, e = os.Stat(filepath.Join(st.FileDir(), name)); e != nil {
			t.Fatal("preview deleted", e)
		}
	}
	r, e = st.GC(24*time.Hour, true)
	if e != nil || r.Removed != 3 {
		t.Fatal(r, e)
	}
	for _, name := range []string{live.ID, young, ".upload-link"} {
		if _, e = os.Lstat(filepath.Join(st.FileDir(), name)); e != nil {
			t.Fatal("unsafe deletion", e)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Fatal("followed symlink")
	}
	r, e = st.GC(24*time.Hour, true)
	if e != nil || r.Removed != 0 {
		t.Fatal("non-idempotent GC", r, e)
	}
}
func TestStorageIntegrityDetectsMissingAndCorruptFiles(t *testing.T) {
	st, _, owner := serverFixture(t)
	f := fixtureFile(t, st, owner, "ABCD")
	p := filepath.Join(st.FileDir(), f.ID)
	os.WriteFile(p, []byte("WXYZ"), 0600)
	r, e := st.Check(true)
	if e != nil || r.OK || len(r.FileIssues) != 1 || r.FileIssues[0].Code != "hash_mismatch" {
		t.Fatal(r, e)
	}
	os.Remove(p)
	r, e = st.Check(true)
	if e != nil || r.OK || r.FileIssues[0].Code != "missing" {
		t.Fatal(r, e)
	}
}
func TestSQLiteStaleWriterRejected(t *testing.T) {
	st, _ := testStore(t)
	other, e := Open(st.path)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	if e = st.Audit(model.Event{Action: "one"}); e != nil {
		t.Fatal(e)
	}
	if e = other.Audit(model.Event{Action: "stale"}); e == nil {
		t.Fatal("stale writer accepted")
	}
	if other.s.Seq != 0 {
		t.Fatal("failed writer mutated memory")
	}
}
func TestMigrationCorruptInputLeavesOriginal(t *testing.T) {
	src := filepath.Join(t.TempDir(), "metadata.json")
	raw := []byte(`{"version":3,"ownerId":"missing"}`)
	os.WriteFile(src, raw, 0600)
	if _, _, e := MigrateJSON(src, src+".db"); e == nil {
		t.Fatal("bad source accepted")
	}
	b, _ := os.ReadFile(src)
	if string(b) != string(raw) {
		t.Fatal("input overwritten")
	}
	if _, e := os.Stat(src + ".db"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("destination published")
	}
}

func TestBackupDestinationSymlinkCannotEscapeAncestryCheck(t *testing.T) {
	st, dir, _ := serverFixture(t)
	_ = st
	parent := t.TempDir()
	alias := filepath.Join(parent, "alias")
	if e := os.Symlink(dir, alias); e != nil {
		t.Skip(e)
	}
	if _, e := Backup(dir, filepath.Join(alias, "nested", "backup")); e == nil {
		t.Fatal("symlink parent bypassed source/destination separation")
	}
	if _, e := os.Stat(filepath.Join(dir, "nested")); !os.IsNotExist(e) {
		t.Fatal("failed backup modified source")
	}
}

// Portable backup names must be slash-separated, even on Windows.
func TestBackupManifestPortablePaths(t *testing.T) {
 for _, name := range []string{"bridge.db","metadata.json","server.key","uploads/file_ABCDEFGHIJKLMNOPQRSTUVWX"} {
  if !safeRelative(name) { t.Fatalf("rejected valid backup name %q",name) }
 }
 for _, name := range []string{"",".","..","../server.key","/etc/passwd","C:/Windows/system.ini","uploads\\file_ABCDEFGHIJKLMNOPQRSTUVWX","uploads/../server.key","uploads//file_ABCDEFGHIJKLMNOPQRSTUVWX","unlisted.txt"} {
  if safeRelative(name) { t.Fatalf("accepted unsafe backup name %q",name) }
 }
}
