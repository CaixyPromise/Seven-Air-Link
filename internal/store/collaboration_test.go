package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"seven-air-link/internal/model"
)

func member(t *testing.T, s *Store, name, kind, device string) (Subject, model.Claim) {
	t.Helper()
	invite, e := s.CreatePrincipal(Subject{ID: s.OwnerID(), Role: "owner"}, model.PrincipalCreate{Name: name, Type: kind, DeviceID: device}, "test")
	if e != nil {
		t.Fatal(e)
	}
	claimed, e := s.Claim(invite.Code, "test")
	if e != nil {
		t.Fatal(e)
	}
	sub, e := s.Auth(claimed.Token)
	if e != nil {
		t.Fatal(e)
	}
	return sub, claimed
}
func textSend(t *testing.T, s *Store, sub Subject, key, to, text string) model.Message {
	t.Helper()
	m, _, e := s.Send(sub, model.MessageSend{ClientMessageID: key, To: to, Text: text}, "test")
	if e != nil {
		t.Fatal(e)
	}
	return m
}

func TestPrincipalEnrollmentConcurrentRotationAndPersistence(t *testing.T) {
	s, ownerToken := testStore(t)
	owner, _ := s.Auth(ownerToken)
	inv, e := s.CreatePrincipal(owner, model.PrincipalCreate{Name: "Coding Bot", Type: "bot"}, "r1")
	if e != nil {
		t.Fatal(e)
	}
	var successes atomic.Int32
	var claim model.Claim
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.Claim(inv.Code, "claim")
			if e == nil {
				successes.Add(1)
				mu.Lock()
				claim = v
				mu.Unlock()
			} else if !errors.Is(e, ErrEnrollment) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("one use code accepted %d times", successes.Load())
	}
	old, e := s.Auth(claim.Token)
	if e != nil || old.Role != "member" {
		t.Fatal(old, e)
	}
	if _, e = s.CreatePrincipal(old, model.PrincipalCreate{Name: "escape", Type: "human"}, ""); !errors.Is(e, ErrDenied) {
		t.Fatal("member created identity", e)
	}
	if _, e = s.CreatePrincipal(owner, model.PrincipalCreate{Name: "Coding Bot", Type: "human"}, ""); !errors.Is(e, ErrConflict) {
		t.Fatal("duplicate name", e)
	}
	next, e := s.Reenroll(owner, old.ID, "rotate")
	if e != nil {
		t.Fatal(e)
	}
	if s.Active(old) {
		t.Fatal("old generation still active before claim")
	}
	newer, e := s.Claim(next.Code, "")
	if e != nil {
		t.Fatal(e)
	}
	if s.Active(old) {
		t.Fatal("old generation revived when new token claimed")
	}
	if _, e = s.Auth(claim.Token); !errors.Is(e, ErrAuth) {
		t.Fatal("old token accepted", e)
	}
	sub, e := s.Auth(newer.Token)
	if e != nil || !s.Active(sub) {
		t.Fatal(e)
	}
	onDisk, e := os.ReadFile(s.path)
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{ownerToken, inv.Code, claim.Token, next.Code, newer.Token} {
		if strings.Contains(string(onDisk), secret) {
			t.Fatal("plaintext credential persisted")
		}
	}
	reopened, e := Open(s.path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, e = reopened.Auth(newer.Token); e != nil {
		t.Fatal(e)
	}
	if e = reopened.RevokePrincipal(owner, sub.ID, "revoke"); e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.Auth(newer.Token); !errors.Is(e, ErrPrincipalInactive) {
		t.Fatal("revoked token accepted", e)
	}
	if _, e = reopened.Reenroll(owner, sub.ID, ""); !errors.Is(e, ErrPrincipalInactive) {
		t.Fatal("revocation undone", e)
	}
}
func TestPrincipalExpiryAndDeviceBinding(t *testing.T) {
	s, _ := testStore(t)
	owner := Subject{ID: s.OwnerID(), Role: "owner"}
	p, _ := s.Pair(time.Minute, "")
	dev, e := s.Join(joinRequest(p.Code), 10, "")
	if e != nil {
		t.Fatal(e)
	}
	bot, claim := member(t, s, "bound", "bot", dev.DeviceID)
	unbound, _ := member(t, s, "unbound", "human", "")
	if !s.Active(bot) {
		t.Fatal("not active")
	}
	if e = s.Revoke(dev.DeviceID, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Auth(claim.Token); !errors.Is(e, ErrPrincipalInactive) {
		t.Fatal("bound identity survived device revocation", e)
	}
	if !s.Active(unbound) {
		t.Fatal("unrelated identity lost access")
	}
	if e = s.mutate(func(s *snapshot) error {
		v := s.Principals[unbound.ID]
		past := time.Now().Add(-time.Second)
		v.Principal.ExpiresAt = &past
		s.Principals[unbound.ID] = v
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if s.Active(unbound) {
		t.Fatal("expired identity active")
	}
	inv, e := s.CreatePrincipal(owner, model.PrincipalCreate{Name: "expired invite", Type: "bot"}, "")
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	v := s.s.Principals[inv.Principal.ID]
	v.EnrollmentExpires = time.Now().Add(-time.Second)
	s.s.Principals[v.Principal.ID] = v
	s.mu.Unlock()
	if _, e = s.Claim(inv.Code, ""); !errors.Is(e, ErrEnrollment) {
		t.Fatal("expired enrollment accepted", e)
	}
}
func TestPrivateMessagesIdempotencyAndAudit(t *testing.T) {
	s, _ := testStore(t)
	a, _ := member(t, s, "A", "bot", "")
	b, _ := member(t, s, "B", "bot", "")
	c, _ := member(t, s, "C", "human", "")
	owner := Subject{ID: s.OwnerID(), Role: "owner"}
	q := model.MessageSend{ClientMessageID: "unit:idem", To: b.ID, Text: "secret SQL SELECT 机密", CorrelationID: "work-42"}
	first, dupe, e := s.Send(a, q, "req-msg")
	if e != nil || dupe {
		t.Fatal(e, dupe)
	}
	second, dupe, e := s.Send(a, q, "")
	if e != nil || !dupe || second.ID != first.ID {
		t.Fatal("non-idempotent", e)
	}
	q.Text += "altered"
	if _, _, e = s.Send(a, q, ""); !errors.Is(e, ErrIdempotency) {
		t.Fatal("changed payload replay", e)
	}
	for _, sub := range []Subject{a, b} {
		m, e := s.Message(sub, first.ID)
		if e != nil || m.Text != first.Text {
			t.Fatal(e)
		}
	}
	for _, sub := range []Subject{c, owner} {
		if _, e := s.Message(sub, first.ID); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("private content leaked", sub.ID, e)
		}
	}
	textSend(t, s, c, "shared", "shared", "all members")
	page, e := s.Messages(owner, "", 0, 0, 10)
	if e != nil || len(page.Messages) != 1 || page.Messages[0].RecipientID != "shared" || page.NextCursor != 2 {
		t.Fatal(page, e)
	}
	ev, _, err := s.Events(0, 500)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ev)
	if strings.Contains(string(raw), first.Text) {
		t.Fatal("private body in audit")
	}
	if !strings.Contains(string(raw), "work-42") {
		t.Fatal("correlation missing from audit")
	}
	q.Text = first.Text
	if e = s.RevokePrincipal(owner, b.ID, ""); e != nil {
		t.Fatal(e)
	}
	m, dupe, e := s.Send(a, q, "")
	if e != nil || !dupe || m.ID != first.ID {
		t.Fatal("lost durable result after recipient revoke", e)
	}
	q.ClientMessageID = "new"
	if _, _, e = s.Send(a, q, ""); !errors.Is(e, ErrPrincipalInactive) {
		t.Fatal("new message to revoked recipient", e)
	}
}
func TestConcurrentMessageDeduplication(t *testing.T) {
	s, _ := testStore(t)
	a, _ := member(t, s, "A", "bot", "")
	var wg sync.WaitGroup
	var created atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, duplicate, e := s.Send(a, model.MessageSend{ClientMessageID: "concurrent", To: "shared", Text: "exactly one record"}, "")
			if e != nil {
				t.Error(e)
			}
			if !duplicate {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	page, e := s.Messages(a, "", 0, 0, 10)
	if e != nil || created.Load() != 1 || len(page.Messages) != 1 || s.s.MessageSeq != 1 {
		t.Fatal(created.Load(), page, e)
	}
	if len(s.s.Messages) != 0 {
		t.Fatal("history retained in authority cache")
	}
	rows, e := s.db.Query("SELECT count(*) FROM messages")
	if e != nil || rows[0][0] != int64(1) {
		t.Fatal(rows, e)
	}
}
func TestMessagePagingRepliesAndResume(t *testing.T) {
	s, _ := testStore(t)
	a, _ := member(t, s, "A", "bot", "")
	b, _ := member(t, s, "B", "bot", "")
	c, _ := member(t, s, "C", "bot", "")
	direct := textSend(t, s, a, "d", b.ID, "private")
	for i := 0; i < 7; i++ {
		textSend(t, s, a, fmt.Sprint(i), "shared", fmt.Sprint(i))
	}
	newest, e := s.Messages(b, "shared", -1, 0, 3)
	if e != nil || !newest.HasMore || len(newest.Messages) != 3 || newest.Messages[0].Seq != 8 || newest.NextCursor != 6 {
		t.Fatal(newest, e)
	}
	older, e := s.Messages(b, "shared", -1, newest.NextCursor, 3)
	if e != nil || older.Messages[0].Seq != 5 {
		t.Fatal(older, e)
	}
	next, e := s.Messages(c, "", 0, 0, 3)
	if e != nil || next.Messages[0].Seq != 2 || next.NextCursor != 4 || !next.HasMore {
		t.Fatal(next, e)
	}
	if _, e = s.Messages(a, "", 9, 0, 50); e == nil {
		t.Fatal("future cursor accepted")
	}
	_, _, e = s.Send(b, model.MessageSend{ClientMessageID: "reply", To: a.ID, Text: "reply", ReplyTo: direct.ID}, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, dest := range []string{"shared", c.ID} {
		if _, _, e = s.Send(b, model.MessageSend{ClientMessageID: dest, To: dest, Text: "leak", ReplyTo: direct.ID}, ""); e == nil {
			t.Fatal("reply escaped conversation")
		}
	}
	reopened, e := Open(s.path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	page, e := reopened.Messages(b, "", 8, 0, 100)
	if e != nil || len(page.Messages) != 1 || page.Messages[0].ReplyTo != direct.ID {
		t.Fatal(page, e)
	}
}
func TestAttachmentACLAndFileLifecycle(t *testing.T) {
	s, _ := testStore(t)
	a, _ := member(t, s, "A", "human", "")
	b, _ := member(t, s, "B", "bot", "")
	c, _ := member(t, s, "C", "bot", "")
	owner := Subject{ID: s.OwnerID(), Role: "owner"}
	f := model.File{ID: "file_test", Name: "中文日志.txt", SHA256: strings.Repeat("a", 64), Size: 19, RecipientID: b.ID}
	if e := s.PutFile(a, f, ""); e != nil {
		t.Fatal(e)
	}
	for _, sub := range []Subject{a, b} {
		if _, e := s.File(sub, f.ID); e != nil {
			t.Fatal(e)
		}
	}
	for _, sub := range []Subject{c, owner} {
		if _, e := s.File(sub, f.ID); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("file ACL bypass", e)
		}
	}
	if _, _, e := s.Send(a, model.MessageSend{ClientMessageID: "attach", To: b.ID, Attachments: []string{f.ID}}, ""); e != nil {
		t.Fatal(e)
	}
	for _, dest := range []string{"shared", c.ID} {
		if _, _, e := s.Send(b, model.MessageSend{ClientMessageID: dest, To: dest, Text: "forward", Attachments: []string{f.ID}}, ""); !errors.Is(e, ErrDenied) {
			t.Fatal("private file grant widened", e)
		}
	}
	if e := s.DeleteFile(b, f.ID, ""); !errors.Is(e, ErrDenied) {
		t.Fatal("recipient deleted owner's file", e)
	}
	if e := s.DeleteFile(a, f.ID, ""); e != nil {
		t.Fatal(e)
	}
	if _, e := s.File(b, f.ID); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("deleted file still exposed", e)
	}
	if _, _, e := s.Send(a, model.MessageSend{ClientMessageID: "deadref", To: b.ID, Attachments: []string{f.ID}}, ""); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	for _, bad := range []string{"../secret", "a/b", "a\\b", ".", "nul\x00", strings.Repeat("a", 241)} {
		if ValidFileName(bad) {
			t.Fatal("unsafe filename", bad)
		}
	}
}
func TestCollaborationFailedCommitAndQuotas(t *testing.T) {
	s, _ := testStore(t)
	a, _ := member(t, s, "A", "bot", "")
	original := s.path
	s.path = t.TempDir()
	if _, _, e := s.Send(a, model.MessageSend{ClientMessageID: "failed", To: "shared", Text: "not accepted"}, ""); e == nil {
		t.Fatal("expected durable failure")
	}
	if len(s.s.Messages) != 0 || s.s.MessageSeq != 0 {
		t.Fatal("uncommitted message visible")
	}
	s.path = original
	if e := s.mutate(func(s *snapshot) error { s.MessageBytes = MaxMessageBytes; return nil }); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.Send(a, model.MessageSend{ClientMessageID: "quota", To: "shared", Text: "x"}, ""); !errors.Is(e, ErrQuota) {
		t.Fatal("quota bypass", e)
	}
	if len(s.s.Messages) != 0 {
		t.Fatal("quota silently appended/dropped history")
	}
}
func TestM2MigrationAndCorruptStoreFailClosed(t *testing.T) {
	s, token := testStore(t)
	snap := s.s
	snap.Version = 2
	snap.Principals = nil
	snap.Messages = nil
	snap.Files = nil
	raw, _ := json.Marshal(snap)
	legacy := filepath.Join(t.TempDir(), "metadata.json")
	os.WriteFile(legacy, raw, 0600)
	dest := filepath.Join(filepath.Dir(legacy), "bridge.db")
	reopened, report, e := MigrateJSON(legacy, dest)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if reopened.s.Version != SchemaVersion || report.SourceVersion != 2 {
		t.Fatal("not migrated", report)
	}
	if _, e = reopened.Auth(token); e != nil {
		t.Fatal("owner lost", e)
	}
	backup, e := os.ReadFile(report.Backup)
	if e != nil || string(backup) != string(raw) {
		t.Fatal("backup differs", e)
	}
	// Logical corruption in a structurally valid SQLite file must also fail closed.
	if _, e = reopened.db.Exec("UPDATE server_state SET message_seq=7"); e != nil {
		t.Fatal(e)
	}
	reopened.Close()
	if _, e = Open(dest); e == nil {
		t.Fatal("corrupt replay state accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	snap.Services = nil
	raw, _ = json.Marshal(snap)
	os.WriteFile(bad, raw, 0600)
	if _, _, e = MigrateJSON(bad, bad+".db"); e == nil {
		t.Fatal("corrupt registry migrated")
	}
	after, _ := os.ReadFile(bad)
	if string(after) != string(raw) {
		t.Fatal("corrupt source modified")
	}
}
func FuzzMessageValidation(f *testing.F) {
	f.Add("safe-id", "shared", "你好", "")
	f.Add("", "", "", "")
	f.Add("x", "shared", "<script>alert(1)</script>", "work-1")
	f.Fuzz(func(t *testing.T, key, to, text, corr string) {
		q := model.MessageSend{ClientMessageID: key, To: to, Text: text, CorrelationID: corr}
		e := ValidateMessage(q)
		if e == nil && (len(text) > MaxTextBytes || key == "" || to == "") {
			t.Fatal("invalid message accepted")
		}
	})
}
