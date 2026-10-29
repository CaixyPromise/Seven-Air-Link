package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"seven-air-link/internal/client"
	"seven-air-link/internal/forward"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
)

func (r *rig) participant(t *testing.T, name, kind, device string) (model.Config, *client.Client) {
	t.Helper()
	ctx := context.Background()
	var inv model.Enrollment
	if e := r.c.Do(ctx, "POST", "/api/v1/principals", model.PrincipalCreate{Name: name, Type: kind, DeviceID: device}, &inv); e != nil {
		t.Fatal(e)
	}
	var claim model.Claim
	if e := r.c.Do(ctx, "POST", "/api/v1/identity/claim", map[string]string{"code": inv.Code}, &claim); e != nil {
		t.Fatal(e)
	}
	cfg := r.cfg
	cfg.Role = "member"
	cfg.SubjectID = claim.Principal.ID
	cfg.Token = claim.Token
	c, e := client.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(c.Close)
	return cfg, c
}
func sendMessage(t *testing.T, c *client.Client, to, key, text string) model.Message {
	t.Helper()
	var result struct {
		Message model.Message `json:"message"`
	}
	if e := c.Do(context.Background(), "POST", "/api/v1/messages", model.MessageSend{ClientMessageID: key, To: to, Text: text, CorrelationID: "test-unit"}, &result); e != nil {
		t.Fatal(e)
	}
	return result.Message
}
func getJSON(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	if e := json.NewDecoder(resp.Body).Decode(out); e != nil {
		t.Fatal(e)
	}
}

func TestCollaborationAPIAuthorityPrivacyAndMetadata(t *testing.T) {
	r := newRig(t)
	a, ac := r.participant(t, "Bot A", "bot", "")
	b, bc := r.participant(t, "Bot B", "bot", "")
	stranger, _ := r.participant(t, "Human", "human", "")
	dev := r.join(t, "node")
	private := sendMessage(t, ac, b.SubjectID, "one", "PRIVATE-MESSAGE-秘密-<script>alert(1)</script>")
	for _, token := range []string{a.Token, b.Token} {
		expect(t, r.raw(t, "GET", "/api/v1/messages/"+private.ID, token, nil, nil), 200)
	}
	for _, token := range []string{r.token, stranger.Token} {
		expect(t, r.raw(t, "GET", "/api/v1/messages/"+private.ID, token, nil, nil), 404)
	}
	expect(t, r.raw(t, "POST", "/api/v1/messages", dev.Token, model.MessageSend{ClientMessageID: "d", To: "shared", Text: "device masquerade"}, nil), 403)
	expect(t, r.raw(t, "POST", "/api/v1/principals", a.Token, model.PrincipalCreate{Name: "unauthorized", Type: "bot"}, nil), 403)
	expect(t, r.raw(t, "POST", "/api/v1/pairings", a.Token, map[string]int{"ttlSeconds": 300}, nil), 403)
	expect(t, r.raw(t, "GET", "/api/v1/node/control", a.Token, nil, nil), 403)
	expect(t, r.raw(t, "POST", "/api/v1/messages", a.Token, map[string]any{"clientMessageId": "spoof", "to": "shared", "text": "x", "senderId": b.SubjectID}, nil), 400)
	expect(t, r.raw(t, "POST", "/api/v1/messages", a.Token, model.MessageSend{ClientMessageID: "large", To: "shared", Text: strings.Repeat("x", 32769)}, nil), 400)
	reply := sendMessage(t, bc, a.SubjectID, "two", "ack without task execution promise")
	if reply.SenderID != b.SubjectID || reply.Status != "accepted" {
		t.Fatal(reply)
	}
	resp := r.raw(t, "GET", "/api/v1/activity?limit=500", r.token, nil, nil)
	expect(t, resp, 200)
	raw, _ := io.ReadAll(resp.Body)
	if bytes.Contains(raw, []byte(private.Text)) || bytes.Contains(raw, []byte(a.Token)) || !bytes.Contains(raw, []byte("message.accepted")) || !bytes.Contains(raw, []byte("collaboration.denied")) {
		t.Fatalf("bad audit: %s", raw)
	}
	var activity struct {
		Events []model.Event `json:"events"`
	}
	getJSON(t, r.raw(t, "GET", "/api/v1/activity?limit=500", a.Token, nil, nil), &activity)
	for _, e := range activity.Events {
		if e.ActorID != a.SubjectID {
			t.Fatal("other actor audit exposed", e)
		}
	}
	var filtered struct {
		Events []model.Event `json:"events"`
	}
	getJSON(t, r.raw(t, "GET", "/api/v1/activity?limit=500&actor="+b.SubjectID+"&correlation=test-unit", r.token, nil, nil), &filtered)
	if len(filtered.Events) != 1 || filtered.Events[0].ResourceID != reply.ID {
		t.Fatal(filtered)
	}
	var peers struct {
		Peers []model.Principal `json:"peers"`
	}
	getJSON(t, r.raw(t, "GET", "/api/v1/peers", a.Token, nil, nil), &peers)
	if len(peers.Peers) != 4 {
		t.Fatal(peers)
	}
	expect(t, r.raw(t, "GET", "/collaboration.js", "", nil, nil), 200)
}

func TestMemberBrowserSessionCSRFAndRotation(t *testing.T) {
	r := newRig(t)
	a, ac := r.participant(t, "Browser Bot", "bot", "")
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if e := ac.Do(context.Background(), "POST", "/api/v1/browser-tickets", map[string]any{}, &ticket); e != nil {
		t.Fatal(e)
	}
	resp := r.raw(t, "POST", "/api/v1/sessions", "", map[string]string{"ticket": ticket.Ticket}, nil)
	expect(t, resp, 200)
	cookie := resp.Cookies()[0]
	var login struct {
		CSRF string `json:"csrf"`
	}
	getJSON(t, resp, &login)
	headers := map[string]string{"Cookie": cookie.String(), "Origin": r.http.URL}
	var me struct{ ID, Role string }
	resp = r.raw(t, "GET", "/api/v1/me", "", nil, headers)
	expect(t, resp, 200)
	getJSON(t, resp, &me)
	if me.ID != a.SubjectID || me.Role != "member" {
		t.Fatal("cookie elevated to owner", me)
	}
	msg := model.MessageSend{ClientMessageID: "browser", To: "shared", Text: "browser identity"}
	expect(t, r.raw(t, "POST", "/api/v1/messages", "", msg, headers), 403)
	headers["X-SLB-CSRF"] = login.CSRF
	expect(t, r.raw(t, "POST", "/api/v1/messages", "", msg, headers), 201)
	expect(t, r.raw(t, "POST", "/api/v1/pairings", "", map[string]int{"ttlSeconds": 30}, headers), 403)
	var pending struct {
		Ticket string `json:"ticket"`
	}
	if e := ac.Do(context.Background(), "POST", "/api/v1/browser-tickets", map[string]any{}, &pending); e != nil {
		t.Fatal(e)
	}
	var inv model.Enrollment
	if e := r.c.Do(context.Background(), "POST", "/api/v1/principals/"+a.SubjectID+"/rotate", map[string]any{}, &inv); e != nil {
		t.Fatal(e)
	}
	var claim model.Claim
	if e := r.c.Do(context.Background(), "POST", "/api/v1/identity/claim", map[string]string{"code": inv.Code}, &claim); e != nil {
		t.Fatal(e)
	}
	expect(t, r.raw(t, "GET", "/api/v1/me", a.Token, nil, nil), 401)
	expect(t, r.raw(t, "GET", "/api/v1/me", "", nil, headers), 401)
	expect(t, r.raw(t, "POST", "/api/v1/sessions", "", map[string]string{"ticket": pending.Ticket}, nil), 403)
	expect(t, r.raw(t, "GET", "/api/v1/me", claim.Token, nil, nil), 200)
	// A forged/unbound in-process cookie may not silently become an Owner session.
	r.s.authMu.Lock()
	r.s.sessions[secure.Hash("empty")] = browserSession{expires: time.Now().Add(time.Minute), csrf: "x"}
	r.s.authMu.Unlock()
	expect(t, r.raw(t, "GET", "/api/v1/me", "", nil, map[string]string{"Cookie": "slb_session=empty"}), 403)
}

func TestInboxSSEPrivacyReplayLimitsAndRevocation(t *testing.T) {
	r := newRig(t)
	a, ac := r.participant(t, "A", "bot", "")
	b, _ := r.participant(t, "B", "bot", "")
	c, cc := r.participant(t, "C", "human", "")
	hidden := sendMessage(t, cc, a.SubjectID, "hidden", "NOT-FOR-B")
	visible := sendMessage(t, ac, b.SubjectID, "visible", "FOR-B")
	streams := []*http.Response{}
	for i := 0; i < 4; i++ {
		resp := r.raw(t, "GET", "/api/v1/messages/stream?cursor=0", b.Token, nil, nil)
		expect(t, resp, 200)
		streams = append(streams, resp)
	}
	expect(t, r.raw(t, "GET", "/api/v1/messages/stream?cursor=0", b.Token, nil, nil), 429)
	var peers struct {
		Peers []model.Principal `json:"peers"`
	}
	getJSON(t, r.raw(t, "GET", "/api/v1/peers", c.Token, nil, nil), &peers)
	for _, p := range peers.Peers {
		if p.ID == b.SubjectID && p.Listening != 4 {
			t.Fatal(p)
		}
	}
	lines := make(chan string, 100)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(streams[0].Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	got := ""
	deadline := time.After(3 * time.Second)
	for !strings.Contains(got, visible.ID) {
		select {
		case line := <-lines:
			got += line + "\n"
		case <-deadline:
			t.Fatal("message not streamed", got)
		}
	}
	if strings.Contains(got, hidden.Text) || strings.Contains(got, hidden.ID) {
		t.Fatal("SSE leaked other conversation")
	}
	expect(t, r.raw(t, "DELETE", "/api/v1/principals/"+b.SubjectID, r.token, nil, nil), 200)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked SSE did not terminate")
	}
	for len(lines) > 0 {
		got += <-lines + "\n"
	}
	if !strings.Contains(got, "session.expired") {
		t.Fatal("no explicit expiry event", got)
	}
	for _, resp := range streams {
		resp.Body.Close()
	}
	expect(t, r.raw(t, "GET", "/api/v1/messages/stream?cursor=100000", a.Token, nil, nil), 409)
	// New identity subscriptions can resume past already delivered records.
	resp := r.raw(t, "GET", "/api/v1/messages/stream?cursor="+fmtInt(visible.Seq), a.Token, nil, nil)
	expect(t, resp, 200)
	resp.Body.Close()
}
func fmtInt(x int64) string { b, _ := json.Marshal(x); return string(b) }

func TestFileHTTPIntegrityAuthorizationAndDeletion(t *testing.T) {
	r := newRig(t)
	a, _ := r.participant(t, "Uploader", "human", "")
	b, _ := r.participant(t, "Recipient", "bot", "")
	third, _ := r.participant(t, "Other", "bot", "")
	body := append([]byte("中文\x00\xff\xfe"), bytes.Repeat([]byte{0, 17, 238, 255}, 100000)...)
	req, _ := http.NewRequest("POST", r.http.URL+"/api/v1/files?name="+url.QueryEscape("中文日志.bin")+"&to="+b.SubjectID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+a.Token)
	resp, e := r.http.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	expect(t, resp, 201)
	var f model.File
	getJSON(t, resp, &f)
	sum := sha256.Sum256(body)
	if f.Size != int64(len(body)) || f.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("file hash", f)
	}
	for _, token := range []string{r.token, third.Token} {
		expect(t, r.raw(t, "GET", "/api/v1/files/"+f.ID, token, nil, nil), 404)
	}
	resp = r.raw(t, "GET", "/api/v1/files/"+f.ID, b.Token, nil, nil)
	expect(t, resp, 200)
	got, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || !bytes.Equal(body, got) {
		t.Fatal("file changed", e)
	}
	if resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatal("unsafe download headers")
	}
	resp = r.raw(t, "GET", "/api/v1/files/"+f.ID, b.Token, nil, map[string]string{"Range": "bytes=10-19"})
	expect(t, resp, 206)
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, body[10:20]) {
		t.Fatal("range corrupted")
	}
	expect(t, r.raw(t, "DELETE", "/api/v1/files/"+f.ID, b.Token, nil, nil), 403)
	expect(t, r.raw(t, "POST", "/api/v1/messages", b.Token, model.MessageSend{ClientMessageID: "leak", To: "shared", Attachments: []string{f.ID}}, nil), 403)
	expect(t, r.raw(t, "DELETE", "/api/v1/files/"+f.ID, a.Token, nil, nil), 200)
	expect(t, r.raw(t, "GET", "/api/v1/files/"+f.ID, b.Token, nil, nil), 404)
	req, _ = http.NewRequest("POST", r.http.URL+"/api/v1/files?name="+url.QueryEscape("../secret"), strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer "+a.Token)
	resp, e = r.http.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	expect(t, resp, 400)
	resp.Body.Close()
}

func TestBotServiceGrantAndRevocationCloseActualTunnel(t *testing.T) {
	addr := targetTCP(t, func(c net.Conn) { c.SetDeadline(time.Now().Add(5 * time.Second)); io.Copy(c, c) })
	r := networkRig(t)
	target := r.join(t, "target")
	a, ac := r.participant(t, "Bound Bot", "bot", target.SubjectID)
	spec := newSpec("bot-echo", addr)
	r.approvedNode(t, target, spec)
	expect(t, r.raw(t, "POST", "/api/v1/forwards", a.Token, map[string]string{"serviceId": spec.ID, "listen": "127.0.0.1:18080"}, nil), 403)
	expect(t, r.raw(t, "PATCH", "/api/v1/services/"+spec.ID, a.Token, model.Grants{AllowPrincipals: []string{a.SubjectID}}, nil), 403)
	expect(t, r.raw(t, "PATCH", "/api/v1/services/"+spec.ID, r.token, model.Grants{AllowPrincipals: []string{a.SubjectID}}, nil), 200)
	f := lease(t, ac, spec.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, e := forward.Dial(ctx, ac, f.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, e = conn.Write([]byte("bot-data")); e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, 8)
	if _, e = io.ReadFull(conn, buf); e != nil || string(buf) != "bot-data" {
		t.Fatal("real tunnel did not echo", e)
	}
	expect(t, r.raw(t, "DELETE", "/api/v1/principals/"+a.SubjectID, r.token, nil, nil), 200)
	if _, e = conn.Read(buf); e == nil {
		t.Fatal("revoked Bot retained existing tunnel")
	}
	expect(t, r.raw(t, "POST", "/api/v1/forwards", a.Token, map[string]string{"serviceId": spec.ID, "listen": "127.0.0.1:18080"}, nil), 403)
	r.s.mu.Lock()
	for _, lease := range r.s.forwards {
		if lease.ActorID == a.SubjectID {
			t.Error("revoked lease remains")
		}
	}
	r.s.mu.Unlock()
	var events struct {
		Events []model.Event `json:"events"`
	}
	getJSON(t, r.raw(t, "GET", "/api/v1/activity?actor="+a.SubjectID, r.token, nil, nil), &events)
	found := false
	for _, event := range events.Events {
		if event.Action == "tunnel.opened" {
			found = true
		}
	}
	if !found {
		t.Fatal("Bot tunnel actor not observable")
	}
}

func TestBoundIdentityDisabledWhenDeviceRevoked(t *testing.T) {
	r := newRig(t)
	device := r.join(t, "bound")
	bot, _ := r.participant(t, "Dependent", "bot", device.SubjectID)
	expect(t, r.raw(t, "DELETE", "/api/v1/devices/"+device.SubjectID, r.token, nil, nil), 200)
	expect(t, r.raw(t, "GET", "/api/v1/peers", bot.Token, nil, nil), 403)
	// A direct stale Subject captured before rotation is rejected by every durable write.
	st := r.st
	sub := store.Subject{ID: bot.SubjectID, Role: "member", CredentialStamp: secure.Hash(bot.Token)}
	if _, _, e := st.Send(sub, model.MessageSend{ClientMessageID: "stale", To: "shared", Text: "stale"}, ""); e == nil {
		t.Fatal("stale subject accepted")
	}
}
