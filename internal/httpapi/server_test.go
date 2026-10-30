package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"seven-air-link/internal/client"
	"seven-air-link/internal/controlws"
	"seven-air-link/internal/model"
	"seven-air-link/internal/node"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"strings"
	"testing"
	"time"
)

type rig struct {
	s     *Server
	http  *httptest.Server
	st    *store.Store
	cfg   model.Config
	c     *client.Client
	token string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	certp := filepath.Join(dir, "cert")
	keyp := filepath.Join(dir, "key")
	cert, e := secure.MakeCertificate(certp, keyp, nil)
	if e != nil {
		t.Fatal(e)
	}
	token := secure.Random("owner_")
	st, e := store.New(filepath.Join(dir, "state"), "human_owner", token)
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(st, cert, Options{Heartbeat: 70 * time.Millisecond, Timeout: 280 * time.Millisecond, MaxDevices: 32})
	if e != nil {
		t.Fatal(e)
	}
	h := httptest.NewUnstartedServer(s)
	pair, e := tls.LoadX509KeyPair(certp, keyp)
	if e != nil {
		t.Fatal(e)
	}
	h.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	h.StartTLS()
	cfg := model.Config{Version: 1, Server: h.URL, Fingerprint: secure.Fingerprint(cert.Raw), Token: token, SubjectID: "human_owner", Role: "owner"}
	c, e := client.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close(); c.Close(); h.Close(); st.Close() })
	return &rig{s, h, st, cfg, c, token}
}
func (r *rig) join(t *testing.T, name string) model.Config {
	t.Helper()
	p, e := r.st.Pair(time.Minute, "")
	if e != nil {
		t.Fatal(e)
	}
	public, _ := client.New(model.Config{Server: r.cfg.Server, Fingerprint: r.cfg.Fingerprint})
	defer public.Close()
	var j model.JoinResponse
	e = public.Do(context.Background(), "POST", "/api/v1/join", model.JoinRequest{Code: p.Code, Name: name, OS: "linux", Arch: "amd64", NodeVersion: model.Version, Protocol: 1}, &j)
	if e != nil {
		t.Fatal(e)
	}
	cfg := r.cfg
	cfg.Token = j.Token
	cfg.SubjectID = j.DeviceID
	cfg.Role = "device"
	return cfg
}
func (r *rig) raw(t *testing.T, method, path, token string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var b io.Reader
	if body != nil {
		x, _ := json.Marshal(body)
		b = bytes.NewReader(x)
	}
	q, _ := http.NewRequest(method, r.http.URL+path, b)
	if token != "" {
		q.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		q.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if k == "Host" {
			q.Host = v
		} else {
			q.Header.Set(k, v)
		}
	}
	resp, e := r.http.Client().Do(q)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
func expect(t *testing.T, r *http.Response, status int) {
	t.Helper()
	if r.StatusCode != status {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("want %d got %d: %s", status, r.StatusCode, b)
	}
	if r.Header.Get("X-Request-ID") == "" {
		t.Error("missing request ID")
	}
}
func until(t *testing.T, f func() bool) {
	t.Helper()
	end := time.Now().Add(4 * time.Second)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("condition not reached within 4s")
}
func TestAuthenticationHostOriginAndBodyLimits(t *testing.T) {
	r := newRig(t)
	expect(t, r.raw(t, "GET", "/api/v1/devices", "", nil, nil), 401)
	expect(t, r.raw(t, "GET", "/api/v1/devices", r.token, nil, nil), 200)
	expect(t, r.raw(t, "GET", "/api/v1/devices", r.token, nil, map[string]string{"Host": "evil.example"}), 421)
	expect(t, r.raw(t, "POST", "/api/v1/pairings", r.token, map[string]any{"ttlSeconds": 60}, map[string]string{"Origin": "https://evil.example"}), 403)
	expect(t, r.raw(t, "GET", "/api/v1/status?token=secret", r.token, nil, nil), 400)
	expect(t, r.raw(t, "POST", "/api/v1/pairings", r.token, map[string]any{"ttlSeconds": 60, "unknown": 1}, nil), 400)
	expect(t, r.raw(t, "POST", "/api/v1/join", "", map[string]any{"name": strings.Repeat("x", 9000)}, nil), 413)
}
func TestDeviceCannotActAsOwnerOrSeeOtherDevices(t *testing.T) {
	r := newRig(t)
	a := r.join(t, "A")
	r.join(t, "B")
	expect(t, r.raw(t, "POST", "/api/v1/pairings", a.Token, map[string]any{"ttlSeconds": 60}, nil), 403)
	res := r.raw(t, "GET", "/api/v1/devices", a.Token, nil, nil)
	expect(t, res, 200)
	var got struct {
		Devices []model.Device `json:"devices"`
	}
	json.NewDecoder(res.Body).Decode(&got)
	if len(got.Devices) != 1 || got.Devices[0].ID != a.SubjectID {
		t.Fatal(got)
	}
	expect(t, r.raw(t, "GET", "/api/v1/node/control", r.token, nil, nil), 403)
	expect(t, r.raw(t, "GET", "/api/v1/activity", a.Token, nil, nil), 403)
}
func TestOneTimePairingAndProtocol(t *testing.T) {
	r := newRig(t)
	p, _ := r.st.Pair(time.Minute, "")
	q := model.JoinRequest{Code: p.Code, Name: "dev", Protocol: 2}
	expect(t, r.raw(t, "POST", "/api/v1/join", "", q, nil), 409)
	q.Protocol = 1
	expect(t, r.raw(t, "POST", "/api/v1/join", "", q, nil), 201)
	expect(t, r.raw(t, "POST", "/api/v1/join", "", q, nil), 403)
}
func TestBrowserSessionTicketCSRFAndLogout(t *testing.T) {
	r := newRig(t)
	res := r.raw(t, "POST", "/api/v1/browser-tickets", r.token, map[string]any{}, nil)
	expect(t, res, 201)
	var ticket map[string]string
	json.NewDecoder(res.Body).Decode(&ticket)
	res = r.raw(t, "POST", "/api/v1/sessions", "", map[string]string{"ticket": ticket["ticket"]}, nil)
	expect(t, res, 200)
	cookies := res.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal(cookies)
	}
	var auth map[string]any
	json.NewDecoder(res.Body).Decode(&auth)
	headers := map[string]string{"Cookie": cookies[0].String()}
	expect(t, r.raw(t, "POST", "/api/v1/sessions", "", map[string]string{"ticket": ticket["ticket"]}, nil), 403)
	expect(t, r.raw(t, "POST", "/api/v1/pairings", "", map[string]int{"ttlSeconds": 60}, headers), 403)
	headers["X-SLB-CSRF"] = auth["csrf"].(string)
	expect(t, r.raw(t, "POST", "/api/v1/pairings", "", map[string]int{"ttlSeconds": 60}, headers), 201)
	expect(t, r.raw(t, "DELETE", "/api/v1/sessions", "", nil, headers), 200)
	expect(t, r.raw(t, "GET", "/api/v1/status", "", nil, headers), 401)
}
func TestRealNodesHeartbeatRevocationAndDuplicateConnection(t *testing.T) {
	r := newRig(t)
	cfgA := r.join(t, "Node A")
	cfgB := r.join(t, "Node B")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	aDone := make(chan error, 1)
	bDone := make(chan error, 1)
	go func() { aDone <- node.Run(ctx, cfgA, node.Options{}) }()
	go func() { bDone <- node.Run(ctx, cfgB, node.Options{}) }()
	until(t, func() bool {
		ds := r.s.devices(store.Subject{ID: r.cfg.SubjectID, Role: "owner"})
		return len(ds) == 2 && ds[0].Status == "online" && ds[1].Status == "online"
	})
	time.Sleep(350 * time.Millisecond)
	tc, _ := secure.PinnedTLS(cfgA.Fingerprint)
	_, status, e := controlws.Dial(ctx, cfgA.Server, cfgA.Token, tc)
	if e == nil || status != 409 {
		t.Fatal(status, e)
	}
	expect(t, r.raw(t, "DELETE", "/api/v1/devices/"+cfgA.SubjectID, r.token, nil, nil), 200)
	select {
	case e := <-aDone:
		if e == nil {
			t.Fatal("revocation did not stop node with error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked node kept running")
	}
	expect(t, r.raw(t, "GET", "/api/v1/status", cfgA.Token, nil, nil), 403)
	ds := r.s.devices(store.Subject{Role: "owner"})
	if ds[0].Status != "revoked" || ds[1].Status != "online" {
		t.Fatal(ds)
	}
	cancel()
	select {
	case <-bDone:
	case <-time.After(2 * time.Second):
		t.Fatal("node did not cancel")
	}
}
func TestSilentNodeTimesOut(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, "silent")
	tc, _ := secure.PinnedTLS(cfg.Fingerprint)
	c, _, e := controlws.Dial(context.Background(), cfg.Server, cfg.Token, tc)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	var welcome model.Control
	if e = c.ReadJSON(&welcome); e != nil {
		t.Fatal(e)
	}
	until(t, func() bool { return r.s.devices(store.Subject{Role: "owner"})[0].Status == "offline" })
}
func TestWrongFingerprintFailsBeforeHTTPRequest(t *testing.T) {
	r := newRig(t)
	cfg := r.cfg
	cfg.Fingerprint = strings.Repeat("0", 64)
	c, _ := client.New(cfg)
	defer c.Close()
	var out any
	e := c.Do(context.Background(), "GET", "/api/v1/status", nil, &out)
	if e == nil || !strings.Contains(e.Error(), "fingerprint mismatch") {
		t.Fatal(e)
	}
	if len(r.st.Recent(100)) != 0 {
		t.Fatal("bad pin reached HTTP auth")
	}
}
func TestSSEReplayAndLiveEvents(t *testing.T) {
	r := newRig(t)
	r.st.Pair(time.Minute, "first")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, e := r.c.Request(ctx, "GET", "/api/v1/events?cursor=0", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	r.st.Pair(time.Minute, "second")
	scanner := bufio.NewScanner(res.Body)
	seqs := []int{}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: ") {
			var seq int
			fmt.Sscanf(line, "id: %d", &seq)
			seqs = append(seqs, seq)
			if len(seqs) == 2 {
				break
			}
		}
	}
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatal(seqs, scanner.Err())
	}
}
func TestRevocationPersistsAndSecretsAreNotPublic(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, "D")
	res := r.raw(t, "GET", "/api/v1/devices", r.token, nil, nil)
	body, _ := io.ReadAll(res.Body)
	if bytes.Contains(body, []byte(cfg.Token)) || bytes.Contains(body, []byte("tokenHash")) {
		t.Fatal("credential leak")
	}
	expect(t, r.raw(t, "DELETE", "/api/v1/devices/"+cfg.SubjectID, r.token, nil, nil), 200)
	_, e := r.st.Auth(cfg.Token)
	if !errors.Is(e, store.ErrRevoked) {
		t.Fatal(e)
	}
	res = r.raw(t, "GET", "/api/v1/activity", r.token, nil, nil)
	body, _ = io.ReadAll(res.Body)
	if bytes.Contains(body, []byte(cfg.Token)) {
		t.Fatal("audit credential leak")
	}
}
func TestAvailableMilestonesAndRename(t *testing.T) {
	r := newRig(t)
	cfg := r.join(t, "before")
	expect(t, r.raw(t, "PATCH", "/api/v1/devices/"+cfg.SubjectID, r.token, map[string]string{"name": "<img src=x onerror=alert(1)>"}, nil), 200)
	expect(t, r.raw(t, "GET", "/api/v1/services", r.token, nil, nil), 200)
	expect(t, r.raw(t, "GET", "/api/v1/messages", r.token, nil, nil), 200)
}

func TestStorageDiagnosticsOwnerOnly(t *testing.T) {
	r := newRig(t)
	var stats map[string]any
	if e := r.c.Do(context.Background(), "GET", "/api/v1/storage", nil, &stats); e != nil {
		t.Fatal(e)
	}
	if stats["engine"] != "sqlite" || stats["schema"] != float64(store.SchemaVersion) {
		t.Fatal(stats)
	}
	cfg := r.join(t, "storage-device")
	c, e := client.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e := c.Do(context.Background(), "GET", "/api/v1/storage", nil, &stats); e == nil {
		t.Fatal("Device accessed Owner storage statistics")
	}
}

func TestMaintenanceScriptServedAndReferenced(t *testing.T) {
	r := newRig(t)
	for _, p := range []string{"/", "/maintenance.js"} {
		req, _ := http.NewRequest("GET", r.cfg.Server+p, nil)
		response, e := r.http.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal(p, response.Status)
		}
		if p == "/" && !bytes.Contains(body, []byte("/maintenance.js")) {
			t.Fatal("maintenance asset missing from HTML")
		}
		if p == "/maintenance.js" && !bytes.Contains(body, []byte("/api/v1/storage")) {
			t.Fatal("wrong maintenance asset")
		}
	}
}
