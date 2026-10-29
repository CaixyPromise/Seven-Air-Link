package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"testing"
	"time"
)

func TestLoopbackOnlyNoDNS(t *testing.T) {
	for _, v := range []string{"127.0.0.1:8080", "127.1.2.3:1", "[::1]:65535"} {
		if _, e := Loopback(v, false); e != nil {
			t.Fatal(v, e)
		}
	}
	for _, v := range []string{"localhost:80", "example.com:80", "0.0.0.0:80", "192.168.1.1:80", "[::]:80", "127.0.0.1:0", "127.0.0.1:65536", "http://127.0.0.1:80", "[::1%lo0]:80"} {
		if _, e := Loopback(v, false); e == nil {
			t.Fatal("accepted", v)
		}
	}
	if _, e := Loopback("127.0.0.1:0", true); e != nil {
		t.Fatal(e)
	}
}
func TestServiceAuthorityAndIdempotentRecovery(t *testing.T) {
	st, e := New(filepath.Join(t.TempDir(), "metadata.json"), "human_owner", secure.Random("owner_"))
	if e != nil {
		t.Fatal(e)
	}
	join := func(name string) model.JoinResponse {
		p, _ := st.Pair(time.Minute, "")
		j, e := st.Join(model.JoinRequest{Code: p.Code, Name: name, Protocol: 1}, 16, "")
		if e != nil {
			t.Fatal(e)
		}
		return j
	}
	target := join("target")
	source := join("source")
	sub := Subject{ID: target.DeviceID, Role: "device"}
	req := model.ServiceCreate{ID: "svc_123456789", Name: "api", Protocol: "http", Target: "127.0.0.1:8080", AllowDevices: []string{source.DeviceID}}
	svc, e := st.CreateService(sub, req, "")
	if e != nil {
		t.Fatal(e)
	}
	owner := Subject{ID: "human_owner", Role: "owner"}
	if svc.OwnerPrincipalID != owner.ID || svc.OwnerDeviceID != target.DeviceID {
		t.Fatal("identity conflation")
	}
	if CanUse(svc, owner) || !CanUse(svc, Subject{ID: source.DeviceID, Role: "device"}) {
		t.Fatal("ACL incorrect")
	}
	if _, e = st.CreateService(owner, req, ""); !errors.Is(e, ErrDenied) {
		t.Fatal("server owner bypassed local registration", e)
	}
	g := model.Grants{AllowPrincipals: []string{owner.ID}}
	if e = st.ChangeService(owner, svc.ID, &g, ""); e != nil {
		t.Fatal(e)
	}
	retry, e := st.CreateService(sub, req, "")
	if e != nil {
		t.Fatal(e)
	}
	if !CanUse(retry, owner) || CanUse(retry, Subject{ID: source.DeviceID, Role: "device"}) {
		t.Fatal("reconnect overwrote newer ACL")
	}
	retry.AllowPrincipals[0] = "tampered"
	current, _ := st.Service(svc.ID)
	if current.AllowPrincipals[0] != owner.ID {
		t.Fatal("mutable snapshot leaked")
	}
	if e = st.ChangeService(owner, svc.ID, nil, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = st.CreateService(sub, req, ""); !errors.Is(e, ErrServiceStopped) {
		t.Fatal("stopped service resurrected", e)
	}
	reopened, e := Open(st.path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.CreateService(sub, req, ""); !errors.Is(e, ErrServiceStopped) {
		t.Fatal("restart resurrected service")
	}
}
func TestM1MetadataMigrationBackup(t *testing.T) {
	st, _ := testStore(t)
	snap := st.s
	snap.Version = 1
	snap.Services = nil
	snap.Principals = nil
	snap.Files = nil
	snap.Messages = nil
	raw, _ := json.Marshal(snap)
	path := filepath.Join(t.TempDir(), "metadata.json")
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	up, report, e := MigrateJSON(path, path+".db")
	if e != nil {
		t.Fatal(e)
	}
	defer up.Close()
	if up.s.Version != 4 || up.s.Services == nil {
		t.Fatal("not upgraded")
	}
	old, e := os.ReadFile(report.Backup)
	if e != nil || string(old) != string(raw) {
		t.Fatal("backup not exact", e)
	}
}
