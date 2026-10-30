package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/sqlite"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func rev(n int64) *int64 { return &n }
func TestSharedNoteCASConcurrencyAndDurability(t *testing.T) {
	st, tok := testStore(t)
	owner, _ := st.Auth(tok)
	bot, _ := member(t, st, "Note Bot", "bot", "")
	n, e := st.SharedNote(bot)
	if e != nil || n.ID != "shared" || n.Revision != 0 {
		t.Fatal(n, e)
	}
	var winners, losers atomic.Int32
	var wg sync.WaitGroup
	for _, text := range []string{"first 中文\n  whitespace ", "second <script>not executable</script>"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			n, e := st.UpdateSharedNote(bot, model.NoteWrite{Text: text, BaseRevision: rev(0)}, "race")
			var conflict *NoteConflict
			if e == nil {
				if n.Revision != 1 {
					t.Error(n)
				}
				winners.Add(1)
			} else if errors.As(e, &conflict) && conflict.Current.Revision == 1 {
				losers.Add(1)
			} else {
				t.Error(e)
			}
		}(text)
	}
	wg.Wait()
	if winners.Load() != 1 || losers.Load() != 1 {
		t.Fatal("lost-update protection failed")
	}
	n, _ = st.SharedNote(owner)
	same, e := st.UpdateSharedNote(owner, model.NoteWrite{Text: n.Text, BaseRevision: rev(n.Revision)}, "unchanged")
	if e != nil || same.Revision != n.Revision {
		t.Fatal("unchanged save increments revision", same, e)
	}
	if _, e = st.UpdateSharedNote(bot, model.NoteWrite{Text: n.Text, BaseRevision: rev(0)}, "stale"); e == nil {
		t.Fatal("stale identical content bypassed revision")
	}
	path := st.path
	st.Close()
	again, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer again.Close()
	restored, e := again.SharedNote(owner)
	if e != nil || restored.Text != n.Text || restored.Revision != 1 {
		t.Fatal(restored, e)
	}
	rows, e := again.db.Query("SELECT text,revision FROM shared_notes WHERE id='shared'")
	if e != nil || rows[0][0] != n.Text {
		t.Fatal("note not in own SQLite table", rows, e)
	}
	events, _, e := again.Events(0, 500)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), n.Text) {
		t.Fatal("note body leaked into audit")
	}
}
func TestNoteAuthorizationValidationAndFailedCommit(t *testing.T) {
	st, tok := testStore(t)
	owner, _ := st.Auth(tok)
	bot, claim := member(t, st, "Revocable Bot", "bot", "")
	for _, sub := range []Subject{{Role: "device", ID: "dev_fake"}, {Role: "member", ID: "bot_fake"}, {Role: "owner", ID: "fake"}} {
		if _, e := st.SharedNote(sub); e == nil {
			t.Fatal("read without authority")
		}
		if _, e := st.UpdateSharedNote(sub, model.NoteWrite{Text: "secret", BaseRevision: rev(0)}, ""); e == nil {
			t.Fatal("write without authority")
		}
	}
	for _, q := range []model.NoteWrite{{Text: "blind"}, {Text: "negative", BaseRevision: rev(-1)}, {Text: strings.Repeat("x", MaxNoteBytes+1), BaseRevision: rev(0)}, {Text: string([]byte{0xff}), BaseRevision: rev(0)}} {
		if _, e := st.UpdateSharedNote(owner, q, ""); e == nil {
			t.Fatal("invalid note accepted")
		}
	}
	original := st.path
	st.path = original + "changed"
	if _, e := st.UpdateSharedNote(owner, model.NoteWrite{Text: "failed commit", BaseRevision: rev(0)}, ""); e == nil {
		t.Fatal("expected commit failure")
	}
	st.path = original
	n, _ := st.SharedNote(owner)
	if n.Revision != 0 || n.Text != "" {
		t.Fatal("published uncommitted note")
	}
	if e := st.RevokePrincipal(owner, claim.Principal.ID, ""); e != nil {
		t.Fatal(e)
	}
	if _, e := st.SharedNote(bot); !errors.Is(e, ErrPrincipalInactive) {
		t.Fatal(e)
	}
}
func TestWebPasswordPersistenceRevisionAndAuthority(t *testing.T) {
	st, tok := testStore(t)
	owner, _ := st.Auth(tok)
	bot, claim := member(t, st, "Unaffected Bot", "bot", "")
	pw := "correct battery horse staple"
	hash, e := secure.HashPassword(pw)
	if e != nil {
		t.Fatal(e)
	}
	if e = st.SetWebPassword(bot, hash, 0, ""); !errors.Is(e, ErrDenied) {
		t.Fatal("bot changed owner password", e)
	}
	if e = st.SetWebPassword(owner, hash, 0, "set"); e != nil {
		t.Fatal(e)
	}
	if e = st.SetWebPassword(owner, hash, 0, "stale"); !errors.Is(e, ErrAuthRevision) {
		t.Fatal("stale password settings accepted", e)
	}
	if ok, v := st.VerifyWebPassword(pw); !ok || v != 1 {
		t.Fatal(ok, v)
	}
	if _, e = st.Auth(claim.Token); e != nil {
		t.Fatal("password change revoked bot", e)
	}
	path := st.path
	st.Close()
	st, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if ok, _ := st.VerifyWebPassword(pw); !ok {
		t.Fatal("password not durable")
	}
	status := st.WebPasswordStatus()
	b, _ := json.Marshal(status)
	if !status.Enabled || strings.Contains(string(b), hash) || strings.Contains(string(b), pw) {
		t.Fatal("hash exposed")
	}
	events, _, _ := st.Events(0, 500)
	b, _ = json.Marshal(events)
	if strings.Contains(string(b), hash) || strings.Contains(string(b), pw) {
		t.Fatal("password/hash audit leak")
	}
	if e = st.SetWebPassword(owner, "", 1, "disable"); e != nil {
		t.Fatal(e)
	}
	if ok, _ := st.VerifyWebPassword(pw); ok {
		t.Fatal("disabled password accepted")
	}
}
func downgradeFixtureTo4(t *testing.T, st *Store) {
	t.Helper()
	e := st.db.Transaction(func(tx *sqlite.Tx) error {
		for _, q := range []string{`CREATE TABLE server_state_v4(id INTEGER PRIMARY KEY CHECK(id=1), schema_version INTEGER NOT NULL CHECK(schema_version=4),owner_id TEXT NOT NULL,owner_hash TEXT NOT NULL,seq INTEGER NOT NULL,message_seq INTEGER NOT NULL,message_bytes INTEGER NOT NULL,revision INTEGER NOT NULL)`, `INSERT INTO server_state_v4 SELECT id,4,owner_id,owner_hash,seq,message_seq,message_bytes,revision FROM server_state`, `DROP TABLE server_state`, `ALTER TABLE server_state_v4 RENAME TO server_state`, `DROP TABLE web_auth`, `DROP TABLE shared_notes`, `PRAGMA user_version=4`} {
			if _, e := tx.Exec(q); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestSchema4UpgradeAndBackupRestoreNotesPassword(t *testing.T) {
	st, dir, owner := serverFixture(t)
	textSend(t, st, owner, "before-upgrade", "shared", "persistent 中文 message")
	path := st.path
	downgradeFixtureTo4(t, st)
	st.Close()
	oldBackup := filepath.Join(t.TempDir(), "old-backup")
	before, e := Backup(dir, oldBackup)
	if e != nil || before.Schema != 4 {
		t.Fatal("backup must not migrate original", before, e)
	}
	raw, e := sqlite.Open(path, true)
	if e != nil {
		t.Fatal(e)
	}
	v, _ := raw.Query("PRAGMA user_version")
	raw.Close()
	if v[0][0] != int64(4) {
		t.Fatal("backup modified source schema")
	}
	st, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if st.s.Version != SchemaVersion || st.WebPasswordStatus().Enabled {
		t.Fatal("upgrade defaults")
	}
	if _, e = st.UpdateSharedNote(owner, model.NoteWrite{Text: "Upgrade note 中文", BaseRevision: rev(0)}, "note"); e != nil {
		t.Fatal(e)
	}
	hash, _ := secure.HashPassword("upgrade-test-password-123")
	if e = st.SetWebPassword(owner, hash, 0, "password"); e != nil {
		t.Fatal(e)
	}
	st.Close()
	backup := filepath.Join(t.TempDir(), "backup5")
	r, e := Backup(dir, backup)
	if e != nil || r.Schema != 5 {
		t.Fatal(r, e)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	r, e = RestoreBackup(backup, dest)
	if e != nil || !r.OK {
		t.Fatal(r, e)
	}
	restored, e := Open(filepath.Join(dest, "bridge.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	n, e := restored.SharedNote(owner)
	if e != nil || n.Text != "Upgrade note 中文" || n.Revision != 1 {
		t.Fatal(n, e)
	}
	if ok, _ := restored.VerifyWebPassword("upgrade-test-password-123"); !ok {
		t.Fatal("restored password invalid")
	}
	if restored.s.MessageSeq != 1 {
		t.Fatal("old message lost during upgrade")
	}
}
func TestNoteAndPasswordAuditAtomicFailure(t *testing.T) {
	st, tok := testStore(t)
	owner, _ := st.Auth(tok)
	if _, e := st.db.Exec("CREATE TRIGGER reject_rc3_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'injected audit failure'); END"); e != nil {
		t.Fatal(e)
	}
	if _, e := st.UpdateSharedNote(owner, model.NoteWrite{Text: "must roll back", BaseRevision: rev(0)}, ""); e == nil {
		t.Fatal("missing note transaction rollback")
	}
	hash, _ := secure.HashPassword("atomic-password-change-123")
	if e := st.SetWebPassword(owner, hash, 0, ""); e == nil {
		t.Fatal("missing password transaction rollback")
	}
	n, _ := st.SharedNote(owner)
	if n.Revision != 0 || st.WebPasswordStatus().Enabled {
		t.Fatal("published a failed transaction")
	}
	rows, _ := st.db.Query("SELECT text FROM shared_notes")
	if rows[0][0] != "" {
		t.Fatal(rows)
	}
	rows, _ = st.db.Query("SELECT password_hash FROM web_auth")
	if rows[0][0] != "" {
		t.Fatal(rows)
	}
}
func TestCorruptPasswordHashFailsClosed(t *testing.T) {
	st, _ := testStore(t)
	path := st.path
	if _, e := st.db.Exec("UPDATE web_auth SET password_hash='not-a-supported-hash'"); e != nil {
		t.Fatal(e)
	}
	st.Close()
	reopened, e := Open(path)
	if e == nil {
		reopened.Close()
		t.Fatal("corrupt password hash silently reset")
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("database disappeared")
	}
}
