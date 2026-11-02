package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/sqlite"
)

// Construct the exact schema-5 tables; do not label a schema-6 DB "old".
func TestIdentityUpgradeFromSchema5DisablesAdminPasswordPreservesAuthority(t *testing.T) {
	st, dir, owner := serverFixture(t)

	pw := "Old administrator password 2026"
	hash, err := secure.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetWebPassword(owner, hash, 0, "setup"); err != nil {
		t.Fatal(err)
	}
	bot, claim := member(t, st, "Preserved Bot", "bot", "")
	textSend(t, st, owner, "migration-check", "shared", "消息不能丢失")
	before, err := st.UpdateSharedNote(bot, model.NoteWrite{Text: "保留便签和身份", BaseRevision: rev(0)}, "old-note")
	if err != nil {
		t.Fatal(err)
	}
	databasePath := st.path
	err = st.db.Transaction(func(tx *sqlite.Tx) error {
		for _, q := range []string{
			`CREATE TABLE state5(id INTEGER PRIMARY KEY CHECK(id=1),schema_version INTEGER NOT NULL CHECK(schema_version=5),owner_id TEXT NOT NULL,owner_hash TEXT NOT NULL,seq INTEGER NOT NULL,message_seq INTEGER NOT NULL,message_bytes INTEGER NOT NULL,revision INTEGER NOT NULL)`,
			`INSERT INTO state5 SELECT id,5,owner_id,owner_hash,seq,message_seq,message_bytes,revision FROM server_state`,
			`DROP TABLE server_state`, `ALTER TABLE state5 RENAME TO server_state`,
			`DROP TABLE clients`, `ALTER TABLE shared_notes DROP COLUMN source`, `PRAGMA user_version=5`,
		} {
			if _, e := tx.Exec(q); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	// Backing up schema 5 must not first change its authentication semantics.
	backup := filepath.Join(t.TempDir(), "before-rc4")
	b, err := Backup(dir, backup)
	if err != nil || b.Schema != 5 {
		t.Fatal(b, err)
	}
	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if upgraded.WebPasswordStatus().Enabled {
		t.Fatal("old Owner password became a shared admission secret")
	}
	if ok, _ := upgraded.VerifyWebPassword(pw); ok {
		t.Fatal("old admin password accepted after migration")
	}
	after, err := upgraded.SharedNote(owner)
	if err != nil || after.Text != before.Text || after.Revision != before.Revision || after.UpdatedBy != claim.Principal.ID {
		t.Fatal(after, err)
	}
	if _, err := upgraded.Auth(claim.Token); err != nil {
		t.Fatal("Bot token lost", err)
	}
	if !upgraded.Active(owner) {
		t.Fatal("Owner credential lost")
	}
	rows, err := upgraded.db.Query("SELECT count(*) FROM messages")
	if err != nil || rows[0][0] != int64(1) {
		t.Fatal(rows, err)
	}
	if _, err := upgraded.JoinClient("denied", "cli", "127.0.0.1", upgraded.WebPasswordStatus().Revision, ""); !errors.Is(err, ErrAuthRevision) {
		t.Fatal("joined before reset", err)
	}
	if err = upgraded.SetWebPassword(owner, hash, upgraded.WebPasswordStatus().Revision, "reset"); err != nil {
		t.Fatal(err)
	}
	c, err := upgraded.JoinClient("New browser", "browser", "192.0.2.10", upgraded.WebPasswordStatus().Revision, "join")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := upgraded.Auth(c.Token)
	if err != nil || sub.Role != "member" || sub.ID == owner.ID {
		t.Fatal(sub, err)
	}
	sub.Source.IP = "192.0.2.10"
	n, err := upgraded.UpdateSharedNote(sub, model.NoteWrite{Text: "new attributed note", BaseRevision: rev(after.Revision)}, "write")
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	backup6 := filepath.Join(t.TempDir(), "after-rc4")
	if _, err = Backup(dir, backup6); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if _, err = RestoreBackup(backup6, restoredDir); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(filepath.Join(restoredDir, "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	resumed, err := restored.ResumeBrowser(c.Token, restored.WebPasswordStatus().Revision)
	if err != nil || resumed.ID != c.Principal.ID {
		t.Fatal(resumed, err)
	}
	rn, err := restored.SharedNote(resumed)
	if err != nil || rn.Revision != n.Revision || rn.Source.ClientID != c.Client.ID {
		t.Fatal(rn, err)
	}

}

func TestMemberNodeQuotaEnforcedWhenInvitationConsumed(t *testing.T) {
	st, tok := testStore(t)
	owner, _ := st.Auth(tok)
	hash, e := secure.HashPassword("Joining password test 2026!")
	if e != nil {
		t.Fatal(e)
	}
	if e = st.SetWebPassword(owner, hash, 0, ""); e != nil {
		t.Fatal(e)
	}
	c, e := st.JoinClient("member", "cli", "127.0.0.1", 1, "")
	if e != nil {
		t.Fatal(e)
	}
	sub, _ := st.Auth(c.Token)
	invites := make([]model.PairingResponse, 9)
	for i := range invites {
		invites[i], e = st.PairClientDevice(sub, "")
		if e != nil {
			t.Fatal(e)
		}
	}
	for i, p := range invites {
		_, e = st.Join(model.JoinRequest{Code: p.Code, Name: "own-node", OS: "linux", Arch: "amd64", Protocol: model.Protocol}, 64, "")
		if i < 8 && e != nil {
			t.Fatal(e)
		}
		if i == 8 && !errors.Is(e, ErrQuota) {
			t.Fatal("invitation race bypassed per-member node quota", e)
		}
	}
}

func TestRevokedParentDisablesOwnedBots(t *testing.T) {
	st, tok := testStore(t)
	owner, _ := st.Auth(tok)
	h, e := secure.HashPassword("Joining password test 2026!")
	if e != nil {
		t.Fatal(e)
	}
	if e = st.SetWebPassword(owner, h, 0, ""); e != nil {
		t.Fatal(e)
	}
	c, e := st.JoinClient("parent", "cli", "127.0.0.1", 1, "")
	if e != nil {
		t.Fatal(e)
	}
	sub, _ := st.Auth(c.Token)
	invitation, e := st.CreatePrincipal(sub, model.PrincipalCreate{Name: "child", Type: "bot", TTLSeconds: int64((24 * time.Hour).Seconds())}, "")
	if e != nil {
		t.Fatal(e)
	}
	claim, e := st.Claim(invitation.Code, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = st.RevokePrincipal(owner, sub.ID, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = st.Auth(claim.Token); !errors.Is(e, ErrPrincipalInactive) {
		t.Fatal("child Bot survives whole-parent identity revoke", e)
	}
}

func TestRevokedClientCannotConsumeItsOutstandingNodeInvitation(t *testing.T) {
	st, tok := testStore(t)
	owner, _ := st.Auth(tok)
	hash, e := secure.HashPassword("Joining password test 2026!")
	if e != nil {
		t.Fatal(e)
	}
	if e = st.SetWebPassword(owner, hash, 0, ""); e != nil {
		t.Fatal(e)
	}
	c, e := st.JoinClient("client", "cli", "127.0.0.1", 1, "")
	if e != nil {
		t.Fatal(e)
	}
	sub, _ := st.Auth(c.Token)
	invite, e := st.PairClientDevice(sub, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = st.RevokeClient(owner, c.Client.ID, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = st.Join(model.JoinRequest{Code: invite.Code, Name: "blocked", Protocol: model.Protocol}, 64, ""); !errors.Is(e, ErrPairing) {
		t.Fatal("revoked credential still enrolled a Node", e)
	}
}
