package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"seven-air-link/internal/client"
	"seven-air-link/internal/controlws"
	"seven-air-link/internal/forward"
	"seven-air-link/internal/model"
	"seven-air-link/internal/node"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"strings"
	"sync"
	"testing"
	"time"
)

func (r *rig) approvedNode(t *testing.T, cfg model.Config, specs ...model.ServiceCreate) context.CancelFunc {
	t.Helper()
	p := filepath.Join(t.TempDir(), "policy.json")
	policy := node.Policy{Version: 1, DeviceID: cfg.SubjectID, Services: map[string]node.Approval{}}
	for _, q := range specs {
		policy.Services[q.ID] = node.Approval{Service: q}
	}
	if e := secure.WriteJSON(p, policy); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- node.Run(ctx, cfg, node.Options{PolicyPath: p, MinBackoff: 10 * time.Millisecond, MaxBackoff: 30 * time.Millisecond})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("Node did not stop")
		}
	})
	until(t, func() bool {
		r.s.mu.Lock()
		defer r.s.mu.Unlock()
		n := r.s.nodes[cfg.SubjectID]
		return n != nil && len(n.services) == len(specs)
	})
	return cancel
}
func newSpec(name, target string, allow ...string) model.ServiceCreate {
	return model.ServiceCreate{ID: secure.Random("svc_")[:28], Name: name, Protocol: "tcp", Target: target, AllowDevices: allow, AllowPrincipals: []string{}}
}
func lease(t *testing.T, c *client.Client, id string) model.Forward {
	t.Helper()
	var f model.Forward
	e := c.Do(context.Background(), "POST", "/api/v1/forwards", map[string]string{"serviceId": id, "listen": "127.0.0.1:18080"}, &f)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func targetTCP(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); defer c.Close(); handle(c) }()
		}
	}()
	t.Cleanup(func() { l.Close(); wg.Wait() })
	return l.Addr().String()
}
func TestNetworkBinaryHalfCloseAndConcurrentIntegrity(t *testing.T) {
	// The target waits for EOF before replying: a broken FIN implementation hangs.
	addr := targetTCP(t, func(c net.Conn) {
		c.SetDeadline(time.Now().Add(10 * time.Second))
		b, e := io.ReadAll(c)
		if e == nil {
			sum := sha256.Sum256(b)
			c.Write(sum[:])
			c.(*net.TCPConn).CloseWrite()
		}
	})
	r := networkRig(t)
	source := r.join(t, "source")
	target := r.join(t, "target")
	spec := newSpec("digest", addr, source.SubjectID)
	r.approvedNode(t, target, spec)
	r.approvedNode(t, source)
	c, _ := client.New(source)
	defer c.Close()
	f := lease(t, c, spec.ID)
	n := 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			conn, e := forward.Dial(ctx, c, f.ID)
			if e != nil {
				errs <- e
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(8 * time.Second))
			payload := make([]byte, 256*1024)
			rand.Read(payload)
			if _, e = conn.Write(payload); e == nil {
				e = conn.(interface{ CloseWrite() error }).CloseWrite()
			}
			if e != nil {
				errs <- e
				return
			}
			got, e := io.ReadAll(conn)
			want := sha256.Sum256(payload)
			if e != nil || !bytes.Equal(got, want[:]) {
				errs <- fmt.Errorf("corrupt transfer: %v bytes=%d", e, len(got))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	until(t, func() bool { r.s.mu.Lock(); defer r.s.mu.Unlock(); return len(r.s.tunnels) == 0 })
}
func TestServiceACLAndLocalApprovalBothRequired(t *testing.T) {
	addr := targetTCP(t, func(c net.Conn) { io.Copy(c, c) })
	r := networkRig(t)
	source := r.join(t, "source")
	stranger := r.join(t, "stranger")
	target := r.join(t, "target")
	spec := newSpec("echo", addr, source.SubjectID)
	r.approvedNode(t, target, spec)
	expect(t, r.raw(t, "POST", "/api/v1/forwards", stranger.Token, map[string]string{"serviceId": spec.ID, "listen": "127.0.0.1:18080"}, nil), 403)
	expect(t, r.raw(t, "POST", "/api/v1/forwards", r.token, map[string]string{"serviceId": spec.ID, "listen": "127.0.0.1:18080"}, nil), 403)
	expect(t, r.raw(t, "POST", "/api/v1/forwards", source.Token, map[string]string{"serviceId": spec.ID, "listen": "0.0.0.0:18080"}, nil), 400)
	c, _ := client.New(source)
	defer c.Close()
	f := lease(t, c, spec.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, e := forward.Dial(ctx, c, f.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.Write([]byte("echo"))
	b := make([]byte, 4)
	if _, e = io.ReadFull(conn, b); e != nil || string(b) != "echo" {
		t.Fatal(string(b), e)
	}
	expect(t, r.raw(t, "PATCH", "/api/v1/services/"+spec.ID, r.token, model.Grants{}, nil), 200)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = conn.Read(b); e == nil {
		t.Fatal("revocation left flow open")
	}
	expect(t, r.raw(t, "POST", "/api/v1/tunnels", source.Token, model.TunnelRequest{ForwardID: f.ID}, nil), 403)
	// Simulate a registry/control-plane error. Node still must refuse an address
	// that was never locally approved, even though the Server says it is ready.
	rogue := newSpec("rogue", addr, source.SubjectID)
	if _, e = r.st.CreateService(store.Subject{ID: target.SubjectID, Role: "device"}, rogue, ""); e != nil {
		t.Fatal(e)
	}
	r.s.mu.Lock()
	r.s.nodes[target.SubjectID].services[rogue.ID] = true
	r.s.mu.Unlock()
	f = lease(t, c, rogue.ID)
	if _, e = forward.Dial(ctx, c, f.ID); e == nil || !strings.Contains(e.Error(), "local_policy_denied") {
		t.Fatal("local policy bypass", e)
	}
}
func TestTargetRefusedKnownOfflineAndStopTombstone(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	r := networkRig(t)
	source := r.join(t, "source")
	target := r.join(t, "target")
	spec := newSpec("refused", addr, source.SubjectID)
	cancelNode := r.approvedNode(t, target, spec)
	c, _ := client.New(source)
	defer c.Close()
	f := lease(t, c, spec.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, e := forward.Dial(ctx, c, f.ID); e == nil || !strings.Contains(e.Error(), "target_refused") {
		t.Fatal(e)
	}
	cancelNode()
	until(t, func() bool { r.s.mu.Lock(); defer r.s.mu.Unlock(); return r.s.nodes[target.SubjectID] == nil })
	start := time.Now()
	if _, e := forward.Dial(ctx, c, f.ID); e == nil || !strings.Contains(e.Error(), "device_offline") {
		t.Fatal(e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("known offline did not fail fast")
	}
	expect(t, r.raw(t, "DELETE", "/api/v1/services/"+spec.ID, r.token, nil, nil), 200)
	if _, e := r.st.CreateService(store.Subject{ID: target.SubjectID, Role: "device"}, spec, ""); e != store.ErrServiceStopped {
		t.Fatal("stopped share resurrected", e)
	}
}
func TestTunnelTicketIdentityReplayAndOrigin(t *testing.T) {
	addr := targetTCP(t, func(c net.Conn) { c.SetDeadline(time.Now().Add(3 * time.Second)); io.Copy(c, c) })
	r := networkRig(t)
	source := r.join(t, "source")
	stranger := r.join(t, "stranger")
	target := r.join(t, "target")
	spec := newSpec("tickets", addr, source.SubjectID)
	r.approvedNode(t, target, spec)
	c, _ := client.New(source)
	defer c.Close()
	f := lease(t, c, spec.ID)
	var ticket model.TunnelTicket
	if e := c.Do(context.Background(), "POST", "/api/v1/tunnels", model.TunnelRequest{ForwardID: f.ID}, &ticket); e != nil {
		t.Fatal(e)
	}
	tc, _ := secure.PinnedTLS(source.Fingerprint)
	_, status, e := controlws.DialData(context.Background(), source.Server, stranger.Token, tc, ticket.TunnelID, ticket.Ticket)
	if e == nil || status != 403 {
		t.Fatal("wrong identity consumed ticket", status, e)
	}
	ws, _, e := controlws.DialData(context.Background(), source.Server, source.Token, tc, ticket.TunnelID, ticket.Ticket)
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	_, status, e = controlws.DialData(context.Background(), source.Server, source.Token, tc, ticket.TunnelID, ticket.Ticket)
	if e == nil || (status != 403 && status != 410) {
		t.Fatal("ticket replay accepted", status, e)
	}
	expect(t, r.raw(t, "GET", "/api/v1/tunnel/data", source.Token, nil, map[string]string{"Origin": r.http.URL}), 403)
	r.s.authMu.Lock()
	r.s.sessions[secure.Hash("test-session")] = browserSession{subject: store.Subject{ID: r.cfg.SubjectID, Role: "owner"}, expires: time.Now().Add(time.Minute), csrf: "test-csrf"}
	r.s.authMu.Unlock()
	expect(t, r.raw(t, "GET", "/api/v1/tunnel/data", "", nil, map[string]string{"Cookie": "slb_session=test-session"}), 403)
}
func TestHTTPProxyHeaderIsolationRedirectAndSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-SLB-Secret") != "" {
			t.Error("credential leaked upstream")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/next?q=1", 302)
			return
		}
		if r.URL.Path == "/sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(350 * time.Millisecond)
			io.WriteString(w, "data: last\n\n")
			return
		}
		w.Header().Add("Set-Cookie", "slb_session=evil; Path=/")
		fmt.Fprint(w, r.URL.RequestURI())
	}))
	defer upstream.Close()
	r := networkRig(t)
	target := r.join(t, "target")
	spec := newSpec("web", strings.TrimPrefix(upstream.URL, "http://"))
	spec.Protocol = "http"
	spec.AllowPrincipals = []string{r.cfg.SubjectID}
	r.approvedNode(t, target, spec)
	gateway := httptest.NewUnstartedServer(r.s.ProxyHandler())
	port := gateway.Listener.Addr().(*net.TCPAddr).Port
	r.s.ConfigureProxy(r.http.Listener.Addr().(*net.TCPAddr).Port, port)
	gateway.StartTLS()
	defer gateway.Close()
	hc := gateway.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(path string) *http.Response {
		req, _ := http.NewRequest("GET", gateway.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+r.token)
		req.Header.Set("Cookie", "slb_session=privileged")
		req.Header.Set("X-SLB-Secret", "sensitive")
		res, e := hc.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return res
	}
	prefix := "/proxy/" + spec.ID
	res := get(prefix + "/path%20name?q=a%2Fb")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != "/path%20name?q=a%2Fb" || res.Header.Get("Set-Cookie") != "" {
		t.Fatal(res.StatusCode, string(body), res.Header)
	}
	res = get(prefix + "/redirect")
	res.Body.Close()
	if res.Header.Get("Location") != prefix+"/next?q=1" {
		t.Fatal(res.Header)
	}
	began := time.Now()
	res = get(prefix + "/sse")
	b := make([]byte, len("data: first\n\n"))
	_, e := io.ReadFull(res.Body, b)
	if e != nil || time.Since(began) > 300*time.Millisecond {
		t.Fatal("SSE was buffered", time.Since(began), e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	res = get("/api/v1/devices")
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("proxy exposed control API")
	}
	expect(t, r.raw(t, "POST", "/api/v1/pairings", r.token, model.PairingRequest{TTLSeconds: 60}, map[string]string{"Origin": gateway.URL}), 403)
}

// Race instrumentation and parallel TLS handshakes are deliberately not tested
// with the M1 unit fixture's 280ms silence threshold. Production is 15 seconds.
func networkRig(t *testing.T) *rig {
	r := newRig(t)
	r.s.opt.Heartbeat = 250 * time.Millisecond
	r.s.opt.Timeout = 3 * time.Second
	return r
}

func TestExpiredRenewalClosesExistingStreams(t *testing.T) {
	addr := targetTCP(t, func(c net.Conn) { c.SetDeadline(time.Now().Add(5 * time.Second)); io.Copy(c, c) })
	r := networkRig(t)
	source := r.join(t, "source")
	target := r.join(t, "target")
	spec := newSpec("expire", addr, source.SubjectID)
	r.approvedNode(t, target, spec)
	c, _ := client.New(source)
	defer c.Close()
	f := lease(t, c, spec.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, e := forward.Dial(ctx, c, f.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.Write([]byte("ok"))
	b := make([]byte, 2)
	if _, e = io.ReadFull(conn, b); e != nil {
		t.Fatal(e)
	}
	r.s.mu.Lock()
	old := r.s.forwards[f.ID]
	old.ExpiresAt = time.Now().Add(-time.Second)
	r.s.forwards[f.ID] = old
	r.s.mu.Unlock()
	expect(t, r.raw(t, "PATCH", "/api/v1/forwards/"+f.ID, source.Token, nil, nil), 404)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = conn.Read(b); e == nil {
		t.Fatal("expired lease kept stream alive")
	}
	until(t, func() bool { r.s.mu.Lock(); defer r.s.mu.Unlock(); return len(r.s.tunnels) == 0 })
}

func TestTunnelSetupQuotaAndTimeout(t *testing.T) {
	r := networkRig(t)
	r.s.opt.MaxTunnels = 1
	r.s.opt.ConnectTimeout = 100 * time.Millisecond
	source := r.join(t, "source")
	target := r.join(t, "target")
	tc, _ := secure.PinnedTLS(target.Fingerprint)
	ws, _, e := controlws.Dial(context.Background(), target.Server, target.Token, tc)
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	var hello model.Control
	if e = ws.ReadJSON(&hello); e != nil {
		t.Fatal(e)
	}
	// The authenticated test peer announces readiness but intentionally never
	// attaches its data side. Setup must expire without leaking a permit.
	spec := newSpec("silent", "127.0.0.1:45678", source.SubjectID)
	_, e = r.st.CreateService(store.Subject{ID: target.SubjectID, Role: "device"}, spec, "")
	if e != nil {
		t.Fatal(e)
	}
	r.s.mu.Lock()
	r.s.nodes[target.SubjectID].services[spec.ID] = true
	r.s.mu.Unlock()
	sub := store.Subject{ID: source.SubjectID, Role: "device"}
	pending, _, e := r.s.newTunnel(sub, spec.ID, "", false)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = r.s.newTunnel(sub, spec.ID, "", false); errorCode(e) != "quota_exceeded" {
		t.Fatal("quota bypass", e)
	}
	select {
	case <-pending.done:
	case <-time.After(time.Second):
		t.Fatal("setup did not expire")
	}
	r.s.mu.Lock()
	n := len(r.s.tunnels)
	reason := r.s.failed[pending.id].code
	r.s.mu.Unlock()
	if n != 0 || reason != "timeout" {
		t.Fatal(n, reason)
	}
}

func TestProxyBrowserTicketIsSingleUseScopedAndRevocable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("gateway cookie leaked")
		}
		io.WriteString(w, "service-ok")
	}))
	defer upstream.Close()
	r := networkRig(t)
	target := r.join(t, "target")
	a := newSpec("one", strings.TrimPrefix(upstream.URL, "http://"))
	a.Protocol = "http"
	a.AllowPrincipals = []string{r.cfg.SubjectID}
	b := newSpec("two", a.Target)
	b.Protocol = "http"
	b.AllowPrincipals = []string{r.cfg.SubjectID}
	r.approvedNode(t, target, a, b)
	gate := httptest.NewUnstartedServer(r.s.ProxyHandler())
	r.s.ConfigureProxy(r.http.Listener.Addr().(*net.TCPAddr).Port, gate.Listener.Addr().(*net.TCPAddr).Port)
	gate.StartTLS()
	defer gate.Close()
	hc := gate.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var invitation struct {
		Ticket string `json:"ticket"`
	}
	if e := r.c.Do(context.Background(), "POST", "/api/v1/services/"+a.ID+"/browser-ticket", nil, &invitation); e != nil {
		t.Fatal(e)
	}
	form := url.Values{"ticket": {invitation.Ticket}, "serviceId": {a.ID}}
	login := func(origin string) *http.Response {
		req, _ := http.NewRequest("POST", gate.URL+"/_bridge/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		res, e := hc.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return res
	}
	res := login("https://evil.invalid")
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross origin login", res.StatusCode)
	}
	res = login(r.http.URL)
	res.Body.Close()
	if res.StatusCode != 303 || len(res.Cookies()) != 1 {
		t.Fatal(res.StatusCode, res.Header)
	}
	cookie := res.Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/proxy/"+a.ID+"/" {
		t.Fatal(cookie)
	}
	res = login(r.http.URL)
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("ticket replay", res.StatusCode)
	}
	get := func(id string) *http.Response {
		req, _ := http.NewRequest("GET", gate.URL+"/proxy/"+id+"/", nil)
		req.AddCookie(cookie)
		res, e := hc.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return res
	}
	res = get(a.ID)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("valid scoped session", res.StatusCode)
	}
	res = get(b.ID)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("cookie crossed service boundary", res.StatusCode)
	}
	expect(t, r.raw(t, "PATCH", "/api/v1/services/"+a.ID, r.token, model.Grants{}, nil), 200)
	res = get(a.ID)
	res.Body.Close()
	if res.StatusCode != 401 && res.StatusCode != 403 {
		t.Fatal("old cookie survived ACL change", res.StatusCode)
	}
}

func TestDeviceRevocationClosesSourceAndTargetNetworkStreams(t *testing.T) {
	for _, side := range []string{"source", "target"} {
		t.Run(side, func(t *testing.T) {
			addr := targetTCP(t, func(c net.Conn) { c.SetDeadline(time.Now().Add(4 * time.Second)); io.Copy(c, c) })
			r := networkRig(t)
			source := r.join(t, "source")
			target := r.join(t, "target")
			spec := newSpec("revoke", addr, source.SubjectID)
			r.approvedNode(t, target, spec)
			c, _ := client.New(source)
			defer c.Close()
			f := lease(t, c, spec.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stream, e := forward.Dial(ctx, c, f.ID)
			if e != nil {
				t.Fatal(e)
			}
			defer stream.Close()
			stream.Write([]byte("ready"))
			b := make([]byte, 5)
			if _, e = io.ReadFull(stream, b); e != nil {
				t.Fatal(e)
			}
			id := source.SubjectID
			if side == "target" {
				id = target.SubjectID
			}
			expect(t, r.raw(t, "DELETE", "/api/v1/devices/"+id, r.token, nil, nil), 200)
			stream.SetReadDeadline(time.Now().Add(time.Second))
			if _, e = stream.Read(b); e == nil {
				t.Fatal("revoked device kept data stream")
			}
			if _, e = forward.Dial(ctx, c, f.ID); e == nil {
				t.Fatal("new stream accepted after revocation")
			}
		})
	}
}
