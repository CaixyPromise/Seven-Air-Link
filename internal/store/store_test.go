package store

import (
	"errors"
	"os"
	"path/filepath"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	token := secure.Random("owner_")
	s, e := New(filepath.Join(t.TempDir(), "metadata.json"), "human_owner", token)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, token
}
func joinRequest(code string) model.JoinRequest {
	return model.JoinRequest{Code: code, Name: "测试开发机", OS: "linux", Arch: "amd64", NodeVersion: model.Version, Protocol: 1}
}
func TestPairPersistRevoke(t *testing.T) {
	s, owner := testStore(t)
	p, e := s.Pair(time.Minute, "req_1")
	if e != nil {
		t.Fatal(e)
	}
	j, e := s.Join(joinRequest(p.Code), 10, "req_2")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Join(joinRequest(p.Code), 10, ""); !errors.Is(e, ErrPairing) {
		t.Fatal("single use pairing", e)
	}
	reopened, e := Open(s.path)
	if e != nil {
		t.Fatal(e)
	}
	if sub, e := reopened.Auth(owner); e != nil || sub.Role != "owner" {
		t.Fatal(sub, e)
	}
	if sub, e := reopened.Auth(j.Token); e != nil || sub.Role != "device" || sub.ID != j.DeviceID {
		t.Fatal(sub, e)
	}
	if e = reopened.Revoke(j.DeviceID, "req_3"); e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.Auth(j.Token); !errors.Is(e, ErrRevoked) {
		t.Fatal(e)
	}
	if e = reopened.Revoke(j.DeviceID, ""); e != nil {
		t.Fatal("idempotent revoke", e)
	}
}
func TestPairSingleUseConcurrent(t *testing.T) {
	s, _ := testStore(t)
	p, _ := s.Pair(time.Minute, "")
	var n atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.Join(joinRequest(p.Code), 64, ""); e == nil {
				n.Add(1)
			}
		}()
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("consumed %d times", n.Load())
	}
}
func TestExpiredAndQuota(t *testing.T) {
	s, _ := testStore(t)
	p, _ := s.Pair(time.Second, "")
	s.s.Pairings[secure.Hash(p.Code)] = pairingRecord{ExpiresAt: time.Now().Add(-time.Second)}
	if _, e := s.Join(joinRequest(p.Code), 1, ""); !errors.Is(e, ErrPairing) {
		t.Fatal(e)
	}
	p, _ = s.Pair(time.Minute, "")
	if _, e := s.Join(joinRequest(p.Code), 1, ""); e != nil {
		t.Fatal(e)
	}
	p, _ = s.Pair(time.Minute, "")
	if _, e := s.Join(joinRequest(p.Code), 1, ""); e == nil {
		t.Fatal("quota bypass")
	}
	if _, e := s.Join(joinRequest(p.Code), 2, ""); e != nil {
		t.Fatal("quota failure consumed code", e)
	}
}
func TestFailedCommitDoesNotMutateAuthority(t *testing.T) {
	s, _ := testStore(t)
	old := s.s.Seq
	s.path = t.TempDir()
	if _, e := s.Pair(time.Minute, ""); e == nil {
		t.Fatal("expected atomic rename failure")
	}
	if s.s.Seq != old || len(s.s.Pairings) != 0 {
		t.Fatal("failed commit mutated memory")
	}
}
func TestCorruptMetadataFailsClosed(t *testing.T) {
	s, _ := testStore(t)
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(s.path, []byte(`{"version":2}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(s.path); e == nil {
		t.Fatal("silently reset corrupt state")
	}
	b, _ := os.ReadFile(s.path)
	if string(b) != `{"version":2}` {
		t.Fatal("corruption was overwritten")
	}
}
func TestCursorAndRetention(t *testing.T) {
	s, _ := testStore(t)
	e := s.mutate(func(v *snapshot) error {
		for i := 0; i < MaxEvents+5; i++ {
			addEvent(v, model.Event{Action: "test", Result: "success"})
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.Events(1, 10); !errors.Is(e, ErrCursor) {
		t.Fatal("old cursor not rejected", e)
	}
	events, seq, e := s.Events(0, 10)
	if e != nil || len(events) != 10 || events[0].Seq != 6 || seq != MaxEvents+5 {
		t.Fatal(events, seq, e)
	}
	events, _, _ = s.Events(seq, 10)
	if len(events) != 0 {
		t.Fatal(events)
	}
}
func TestNameValidation(t *testing.T) {
	for _, s := range []string{"", " name", "name\n", "\xff", string(make([]byte, 81))} {
		if ValidName(s) {
			t.Errorf("accepted invalid name %q", s)
		}
	}
	if !ValidName("Mac · 开发机") {
		t.Fatal("Unicode rejected")
	}
}
