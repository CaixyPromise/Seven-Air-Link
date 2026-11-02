package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"seven-air-link/internal/client"
	"seven-air-link/internal/model"
)

const joinTestPassword = "test-joining-password-123"

type memberView struct {
	ID           string          `json:"id"`
	Role         string          `json:"role"`
	Source       model.Source    `json:"source"`
	Capabilities map[string]bool `json:"capabilities"`
}
type browserMember struct {
	cookie, csrf, remember string
	me                     memberView
}

func configureJoin(t *testing.T, r *rig) {
	t.Helper()
	expect(t, r.raw(t, "PUT", "/api/v1/auth/password", r.token, map[string]string{"password": joinTestPassword}, nil), 200)
}
func joinBrowser(t *testing.T, r *rig, name string) browserMember {
	t.Helper()
	resp := r.raw(t, "POST", "/api/v1/sessions/password", "", map[string]string{"password": joinTestPassword, "name": name}, nil)
	expect(t, resp, 200)
	var data struct {
		CSRF string `json:"csrf"`
	}
	getJSON(t, resp, &data)
	b := browserMember{csrf: data.CSRF}
	for _, c := range resp.Cookies() {
		if c.Name == "slb_session" {
			b.cookie = c.Name + "=" + c.Value
		}
		if c.Name == clientCookie {
			b.remember = c.Name + "=" + c.Value
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
				t.Fatal("unsafe persistent client cookie")
			}
		}
	}
	if b.cookie == "" || b.remember == "" {
		t.Fatal("missing separate client/session cookie")
	}
	getJSON(t, r.raw(t, "GET", "/api/v1/me", "", nil, map[string]string{"Cookie": b.cookie}), &b.me)
	return b
}
func joinCLI(t *testing.T, r *rig, name string) model.ClientClaim {
	t.Helper()
	var c model.ClientClaim
	resp := r.raw(t, "POST", "/api/v1/clients/login", "", map[string]string{"password": joinTestPassword, "name": name}, nil)
	expect(t, resp, 201)
	getJSON(t, resp, &c)
	return c
}
func bindingCode(t *testing.T, r *rig, b browserMember) string {
	t.Helper()
	var out struct {
		Code string `json:"code"`
	}
	resp := r.raw(t, "POST", "/api/v1/client-links", "", map[string]any{}, map[string]string{"Cookie": b.cookie, "X-SLB-CSRF": b.csrf})
	expect(t, resp, 201)
	getJSON(t, resp, &out)
	return out.Code
}
func TestPasswordMembersAreIndependentEvenWithSameNameAndIP(t *testing.T) {
	r := newRig(t)
	configureJoin(t, r)
	a := joinBrowser(t, r, "Windows-PC")
	b := joinBrowser(t, r, "Windows-PC")
	if a.me.ID == b.me.ID || a.me.Source.ClientID == b.me.Source.ClientID || a.cookie == b.cookie {
		t.Fatal("independent clients merged")
	}
	for _, m := range []browserMember{a, b} {
		if m.me.Role != "member" || m.me.Capabilities["manageServer"] || m.me.Capabilities["manageDevices"] || m.me.ID == r.cfg.SubjectID {
			t.Fatal("password conferred Owner")
		}
		if m.me.Source.IP != "127.0.0.1" || m.me.Source.Kind != "browser" {
			t.Fatal(m.me.Source)
		}
		h := map[string]string{"Cookie": m.cookie, "X-SLB-CSRF": m.csrf}
		expect(t, r.raw(t, "POST", "/api/v1/pairings", "", map[string]any{"ttlSeconds": 60}, h), 403)
		expect(t, r.raw(t, "GET", "/api/v1/storage", "", nil, h), 403)
		expect(t, r.raw(t, "PUT", "/api/v1/auth/password", "", map[string]string{"password": "unauthorized-new-password"}, h), 403)
		expect(t, r.raw(t, "POST", "/api/v1/principals", "", map[string]string{"name": "rogue admin", "type": "human"}, h), 403)
	}
	expect(t, r.raw(t, "DELETE", "/api/v1/clients/"+a.me.Source.ClientID, "", nil, map[string]string{"Cookie": b.cookie, "X-SLB-CSRF": b.csrf}), 403)
	var clients struct {
		Clients []model.Client `json:"clients"`
	}
	getJSON(t, r.raw(t, "GET", "/api/v1/clients", "", nil, map[string]string{"Cookie": a.cookie}), &clients)
	if len(clients.Clients) != 1 || clients.Clients[0].PrincipalID != a.me.ID {
		t.Fatal("client enumeration crossed identity")
	}
	// Raw role/source injection is rejected, not trusted.
	expect(t, r.raw(t, "POST", "/api/v1/clients/login", "", map[string]string{"password": joinTestPassword, "name": "fake", "role": "owner"}, nil), 400)
}
func TestOwnerRequiresLocalTransportAndCredential(t *testing.T) {
	r := newRig(t)
	cookie, csrf := ticketSession(t, r, r.token)
	request := func(addr, token, cookie string, headers map[string]string) *httptest.ResponseRecorder {
		q := httptest.NewRequest("GET", r.http.URL+"/api/v1/me", nil)
		q.RemoteAddr = addr
		if token != "" {
			q.Header.Set("Authorization", "Bearer "+token)
		}
		if cookie != "" {
			q.Header.Set("Cookie", cookie)
		}
		for k, v := range headers {
			q.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.s.ServeHTTP(w, q)
		return w
	}
	for _, addr := range []string{"192.0.2.9:1234", "[2001:db8::1]:1234"} {
		for _, creds := range [][2]string{{r.token, ""}, {"", cookie}} {
			w := request(addr, creds[0], creds[1], map[string]string{"X-Forwarded-For": "127.0.0.1", "X-Real-IP": "127.0.0.1"})
			if w.Code != 403 || !strings.Contains(w.Body.String(), "owner_local_only") {
				t.Fatalf("remote owner accepted: %d %s", w.Code, w.Body)
			}
		}
	}
	if w := request("127.0.0.1:12", "", "", nil); w.Code != 401 {
		t.Fatal("localhost itself grants authority")
	}
	if w := request("[::1]:12", r.token, "", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	// Owner ticket cannot be redeemed remotely, and a failed remote attempt does not consume it.
	var tk struct {
		Ticket string `json:"ticket"`
	}
	getJSON(t, r.raw(t, "POST", "/api/v1/browser-tickets", r.token, map[string]any{}, nil), &tk)
	raw, _ := json.Marshal(map[string]string{"ticket": tk.Ticket})
	q := httptest.NewRequest("POST", r.http.URL+"/api/v1/sessions", bytes.NewReader(raw))
	q.RemoteAddr = "192.0.2.9:12"
	q.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.s.ServeHTTP(w, q)
	if w.Code != 403 {
		t.Fatal("remote Owner ticket accepted", w.Code)
	}
	expect(t, r.raw(t, "POST", "/api/v1/sessions", "", map[string]string{"ticket": tk.Ticket}, nil), 200)
	expect(t, r.raw(t, "DELETE", "/api/v1/sessions", "", nil, map[string]string{"Cookie": cookie, "X-SLB-CSRF": csrf}), 200)
}
func TestBrowserResumeAndExplicitCLISeparationAndTargetedRevocation(t *testing.T) {
	r := newRig(t)
	configureJoin(t, r)
	a := joinBrowser(t, r, "Browser A")
	b := joinBrowser(t, r, "Browser B")
	unbound := joinCLI(t, r, "Unbound CLI")
	if unbound.Principal.ID == a.me.ID || unbound.Principal.ID == b.me.ID {
		t.Fatal("IP implicitly bound CLI")
	}
	resp := r.raw(t, "POST", "/api/v1/sessions/password", "", map[string]string{"password": joinTestPassword, "name": "different display name"}, map[string]string{"Cookie": a.remember})
	expect(t, resp, 200)
	var me memberView
	getJSON(t, r.raw(t, "GET", "/api/v1/me", "", nil, map[string]string{"Cookie": sessionCookie(resp).String()}), &me)
	if me.ID != a.me.ID || me.Source.ClientID != a.me.Source.ClientID {
		t.Fatal("own persistent client credential not restored")
	}
	code := bindingCode(t, r, a)
	var linked model.ClientClaim
	resp = r.raw(t, "POST", "/api/v1/clients/claim", "", map[string]string{"code": code, "name": "Windows CLI"}, nil)
	expect(t, resp, 201)
	getJSON(t, resp, &linked)
	if linked.Principal.ID != a.me.ID || linked.Client.ID == a.me.Source.ClientID || linked.Token == unbound.Token {
		t.Fatal("bad explicit binding")
	}
	expect(t, r.raw(t, "POST", "/api/v1/clients/claim", "", map[string]string{"code": code, "name": "Replay"}, nil), 403)
	expect(t, r.raw(t, "POST", "/api/v1/pairings", linked.Token, map[string]int{"ttlSeconds": 60}, nil), 403)
	expect(t, r.raw(t, "DELETE", "/api/v1/clients/"+linked.Client.ID, "", nil, map[string]string{"Cookie": b.cookie, "X-SLB-CSRF": b.csrf}), 403)
	// Only the chosen credential is revoked, not all sessions under the identity.
	expect(t, r.raw(t, "DELETE", "/api/v1/clients/"+linked.Client.ID, r.token, nil, nil), 200)
	expect(t, r.raw(t, "GET", "/api/v1/me", linked.Token, nil, nil), 403)
	expect(t, r.raw(t, "GET", "/api/v1/me", "", nil, map[string]string{"Cookie": a.cookie}), 200)
	expect(t, r.raw(t, "GET", "/api/v1/me", "", nil, map[string]string{"Cookie": b.cookie}), 200)
	expect(t, r.raw(t, "GET", "/api/v1/me", unbound.Token, nil, nil), 200)
}
func TestConcurrentBindingCodeConsumedOnce(t *testing.T) {
	r := newRig(t)
	configureJoin(t, r)
	a := joinBrowser(t, r, "Binder")
	code := bindingCode(t, r, a)
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, _ := json.Marshal(map[string]string{"code": code, "name": "Concurrent CLI"})
			resp, e := r.http.Client().Post(r.http.URL+"/api/v1/clients/claim", "application/json", bytes.NewReader(b))
			if e != nil {
				results <- 0
				return
			}
			defer resp.Body.Close()
			results <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for code := range results {
		if code == 201 {
			success++
		} else if code != 403 {
			t.Fatal(code)
		}
	}
	if success != 1 {
		t.Fatal("binding replay", success)
	}
}
func TestMemberCanEnrollOwnNodeAndBotButNotManageOtherMembers(t *testing.T) {
	r := newRig(t)
	configureJoin(t, r)
	a := joinCLI(t, r, "Windows")
	b := joinCLI(t, r, "Linux")
	var pairing model.PairingResponse
	getJSON(t, r.raw(t, "POST", "/api/v1/client/device-pairings", a.Token, map[string]any{}, nil), &pairing)
	var device model.JoinResponse
	getJSON(t, r.raw(t, "POST", "/api/v1/join", "", model.JoinRequest{Code: pairing.Code, Name: "Windows-Node", OS: "windows", Arch: "amd64", NodeVersion: model.Version, Protocol: 1}, nil), &device)
	d, _ := r.st.Device(device.DeviceID)
	if d.OwnerID != a.Principal.ID {
		t.Fatal("Node ownership collapsed to Server Owner")
	}
	create := func(token, name string) model.Claim {
		var inv model.Enrollment
		resp := r.raw(t, "POST", "/api/v1/principals", token, model.PrincipalCreate{Name: name, Type: "bot"}, nil)
		expect(t, resp, 201)
		getJSON(t, resp, &inv)
		var cl model.Claim
		getJSON(t, r.raw(t, "POST", "/api/v1/identity/claim", "", map[string]string{"code": inv.Code}, nil), &cl)
		return cl
	}
	ab := create(a.Token, "Windows Bot")
	bb := create(b.Token, "Linux Bot")
	if ab.Principal.OwnerID != a.Principal.ID {
		t.Fatal("Bot owner wrong")
	}
	expect(t, r.raw(t, "POST", "/api/v1/client/device-pairings", ab.Token, map[string]any{}, nil), 403)
	expect(t, r.raw(t, "POST", "/api/v1/principals", ab.Token, model.PrincipalCreate{Name: "Nested Bot", Type: "bot"}, nil), 403)
	expect(t, r.raw(t, "POST", "/api/v1/principals", b.Token, model.PrincipalCreate{Name: "Hijack Node", Type: "bot", DeviceID: device.DeviceID}, nil), 400)
	expect(t, r.raw(t, "DELETE", "/api/v1/principals/"+bb.Principal.ID, a.Token, nil, nil), 403)
	expect(t, r.raw(t, "DELETE", "/api/v1/principals/"+ab.Principal.ID, a.Token, nil, nil), 200)
	expect(t, r.raw(t, "GET", "/api/v1/me", ab.Token, nil, nil), 403)
	expect(t, r.raw(t, "GET", "/api/v1/me", bb.Token, nil, nil), 200)
	expect(t, r.raw(t, "GET", "/api/v1/me", device.Token, nil, nil), 200)
}
func TestIndependentNoteActorsSourcesAndCAS(t *testing.T) {
	r := newRig(t)
	configureJoin(t, r)
	a := joinBrowser(t, r, "A")
	b := joinBrowser(t, r, "B")
	put := func(m browserMember, revision int, text string) *http.Response {
		return r.raw(t, "PUT", "/api/v1/note", "", map[string]any{"baseRevision": revision, "text": text}, map[string]string{"Cookie": m.cookie, "X-SLB-CSRF": m.csrf})
	}
	var n model.SharedNote
	resp := put(a, 0, "A note")
	expect(t, resp, 200)
	getJSON(t, resp, &n)
	if n.UpdatedBy != a.me.ID || n.Source == nil || n.Source.ClientID != a.me.Source.ClientID || n.Source.SessionID == "" || n.Source.Kind != "browser" {
		t.Fatal(n)
	}
	expect(t, put(b, 0, "would lose update"), 409)
	resp = put(b, 1, "B note")
	expect(t, resp, 200)
	getJSON(t, resp, &n)
	if n.Revision != 2 || n.UpdatedBy != b.me.ID || n.Source.ClientID != b.me.Source.ClientID {
		t.Fatal(n)
	}
}
func TestLinkedCredentialRevocationRemovesOnlyItsLeaseRecords(t *testing.T) {
	r := newRig(t)
	configureJoin(t, r)
	a := joinBrowser(t, r, "Network member")
	linked := func(name string) model.ClientClaim {
		code := bindingCode(t, r, a)
		var cl model.ClientClaim
		getJSON(t, r.raw(t, "POST", "/api/v1/clients/claim", "", map[string]string{"code": code, "name": name}, nil), &cl)
		return cl
	}
	first, second := linked("First CLI"), linked("Second CLI")
	// Process E2E covers the actual socket close; this unit test checks lease scope.
	r.s.mu.Lock()
	r.s.forwards["one"] = model.Forward{ID: "one", ActorID: a.me.ID, ActorRole: "member", ClientID: first.Client.ID}
	r.s.forwards["two"] = model.Forward{ID: "two", ActorID: a.me.ID, ActorRole: "member", ClientID: second.Client.ID}
	r.s.mu.Unlock()
	expect(t, r.raw(t, "DELETE", "/api/v1/clients/"+first.Client.ID, r.token, nil, nil), 200)
	r.s.mu.Lock()
	_, one := r.s.forwards["one"]
	_, two := r.s.forwards["two"]
	r.s.mu.Unlock()
	if one || !two {
		t.Fatal("revocation crossed credential boundary")
	}
	cfg := r.cfg
	cfg.Token = second.Token
	cfg.Role = "member"
	cfg.SubjectID = second.Principal.ID
	c, e := client.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = c.Do(context.Background(), "GET", "/api/v1/me", nil, nil); e != nil {
		t.Fatal(e)
	}
}
