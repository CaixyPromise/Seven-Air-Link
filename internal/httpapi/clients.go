package httpapi

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
)

var errLocalOwner = errors.New("owner access requires the Server's local interface and credential")

const clientCookie = "__Host-sal_client"

type clientLink struct {
	subject store.Subject
	expires time.Time
}

// Source IP is never a credential. Loopback is an additional restriction on an
// already authenticated Owner. Forwarded headers are never trusted. Do not
// expose the management listener through a local reverse proxy or shared tunnel.
func localRequest(r *http.Request) bool {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	return e == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}
func (s *Server) requestSubject(r *http.Request, sub store.Subject, session string) (store.Subject, error) {
	if sub.Role == "owner" && !localRequest(r) {
		return store.Subject{}, errLocalOwner
	}
	sub.Source.IP = remoteIP(r)
	sub.Source.SessionID = session
	if sub.Role == "device" {
		sub.Source.DeviceID = sub.ID
		sub.Source.Kind = "node"
	} else if session != "" {
		sub.Source.Kind = "browser"
	} else if sub.Source.Kind == "" {
		sub.Source.Kind = "cli"
	}
	return sub, nil
}
func (s *Server) capabilities(sub store.Subject) map[string]bool {
	p, _ := s.st.Principal(sub.ID)
	owner := sub.Role == "owner"
	human := sub.Role == "member" && p.Type == "human"
	return map[string]bool{"manageServer": owner, "manageIdentities": owner, "manageDevices": owner, "manageStorage": owner, "createBot": owner || human, "bindCLI": human, "enrollNode": human, "manageOwnClients": human, "collaborate": participant(sub), "useServices": true}
}

// Every join creates a distinct member unless a browser presents its own
// previously issued credential. Neither name, IP nor password groups identities.
func (s *Server) loginClient(w http.ResponseWriter, r *http.Request, cli bool) {
	if cli && r.Header.Get("Origin") != "" {
		s.deny(w, r, 403, "origin_denied", "CLI enrollment does not accept browser requests")
		return
	}
	if !s.passwordRate("password-ip:"+remoteIP(r), 5) || !s.passwordRate("password-global", 30) {
		w.Header().Set("Retry-After", "60")
		s.deny(w, r, 429, "rate_limited", "too many password attempts")
		return
	}
	var q struct {
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if !s.body(w, r, &q) {
		return
	}
	if len(q.Password) > secure.MaxPasswordBytes {
		s.deny(w, r, 400, "invalid_password", "password too long")
		return
	}
	if q.Name == "" {
		q.Name = "Browser member"
		if cli {
			q.Name = "CLI member"
		}
	}
	if !store.ValidName(q.Name) {
		s.deny(w, r, 400, "invalid_name", "name must be 1..80 characters")
		return
	}
	if !s.passwordSlot(w, r) {
		return
	}
	defer func() { <-s.passwordSlots }()
	valid, rev := s.st.VerifyWebPassword(q.Password)
	q.Password = ""
	if !valid {
		s.deny(w, r, 403, "invalid_credentials", "joining password unavailable or incorrect")
		return
	}
	kind := "browser"
	if cli {
		kind = "cli"
	}
	s.authMu.Lock()
	// Reserve browser capacity before creating persistent identity records.
	if !cli {
		for k, b := range s.sessions {
			if time.Now().After(b.expires) || !s.st.Active(b.subject) {
				delete(s.sessions, k)
			}
		}
		if len(s.sessions) >= 64 {
			s.authMu.Unlock()
			s.deny(w, r, 429, "session_quota", "too many active browser sessions")
			return
		}
	}
	var sub store.Subject
	var claim model.ClientClaim
	var err error
	var remembered bool
	if !cli {
		if c, e := r.Cookie(clientCookie); e == nil {
			remembered = true
			sub, err = s.st.ResumeBrowser(c.Value, rev)
		}
	}
	if !remembered {
		claim, err = s.st.JoinClient(q.Name, kind, remoteIP(r), rev, w.Header().Get("X-Request-ID"))
		if err == nil {
			sub, err = s.st.Auth(claim.Token)
		}
	}
	if err != nil {
		s.authMu.Unlock()
		s.collaborationError(w, r, err)
		return
	}
	if cli {
		s.authMu.Unlock()
		jsonReply(w, 201, claim)
		return
	}
	token, csrf, ok := s.issueSessionLocked(sub, 0, "password", r)
	s.authMu.Unlock()
	if !ok {
		s.deny(w, r, 429, "session_quota", "too many active browser sessions")
		return
	}
	if claim.Token != "" {
		http.SetCookie(w, &http.Cookie{Name: clientCookie, Value: claim.Token, Path: "/", MaxAge: int(time.Until(claim.Client.ExpiresAt).Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
	s.replySession(w, r, sub, token, csrf)
}

// Binding codes are short-lived, high-entropy, single-use capabilities. A CLI
// redeems one to receive a NEW credential under the member's existing identity.
func (s *Server) claimClient(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		s.deny(w, r, 403, "origin_denied", "CLI binding does not accept browser requests")
		return
	}
	var q struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !s.body(w, r, &q) {
		return
	}
	if !store.ValidName(q.Name) {
		s.deny(w, r, 400, "invalid_name", "client name required")
		return
	}
	hash := secure.Hash(q.Code)
	s.authMu.Lock()
	link, ok := s.clientLinks[hash]
	if !ok || len(q.Code) > 160 || time.Now().After(link.expires) || !s.st.Active(link.subject) {
		s.authMu.Unlock()
		s.deny(w, r, 403, "binding_invalid_or_expired", "request a new CLI binding code from your browser")
		return
	}
	claim, e := s.st.BindClient(link.subject, q.Name, remoteIP(r), w.Header().Get("X-Request-ID"))
	if e == nil {
		delete(s.clientLinks, hash)
	}
	s.authMu.Unlock()
	if e != nil {
		s.collaborationError(w, r, e)
		return
	}
	jsonReply(w, 201, claim)
}
func (s *Server) clientsRoute(w http.ResponseWriter, r *http.Request, sub store.Subject) bool {
	path := r.URL.Path
	if path != "/api/v1/client-links" && path != "/api/v1/client/device-pairings" && path != "/api/v1/clients" && !strings.HasPrefix(path, "/api/v1/clients/") {
		return false
	}
	if !participant(sub) {
		s.collaborationError(w, r, store.ErrDenied)
		return true
	}
	if r.Method != "GET" && !s.allowedRate("client-mutation:"+sub.ID) {
		s.deny(w, r, 429, "rate_limited", "client operation limit")
		return true
	}
	switch {
	case path == "/api/v1/client-links" && r.Method == "POST":
		p, _ := s.st.Principal(sub.ID)
		if sub.Role != "member" || p.Type != "human" {
			s.collaborationError(w, r, store.ErrDenied)
			return true
		}
		code := secure.Random("cli_bind_")
		expires := time.Now().Add(2 * time.Minute)
		s.authMu.Lock()
		if s.clientLinks == nil {
			s.clientLinks = map[string]clientLink{}
		}
		for k, v := range s.clientLinks {
			if time.Now().After(v.expires) || !s.st.Active(v.subject) {
				delete(s.clientLinks, k)
			}
		}
		if len(s.clientLinks) >= 64 {
			s.authMu.Unlock()
			s.deny(w, r, 429, "quota_exceeded", "pending CLI binding limit")
			return true
		}
		s.clientLinks[secure.Hash(code)] = clientLink{subject: sub, expires: expires}
		s.authMu.Unlock()
		jsonReply(w, 201, map[string]any{"code": code, "expiresAt": expires, "principalId": sub.ID})
	case path == "/api/v1/client/device-pairings" && r.Method == "POST":
		out, e := s.st.PairClientDevice(sub, w.Header().Get("X-Request-ID"))
		if e != nil {
			s.collaborationError(w, r, e)
		} else {
			jsonReply(w, 201, out)
		}
	case path == "/api/v1/clients" && r.Method == "GET":
		jsonReply(w, 200, map[string]any{"clients": s.st.Clients(sub), "currentClientId": sub.Source.ClientID})
	case strings.HasPrefix(path, "/api/v1/clients/") && r.Method == "DELETE":
		id := strings.TrimPrefix(path, "/api/v1/clients/")
		// Store enforces owner-or-self scope; the runtime cleanup uses credential ID.
		e := s.st.RevokeClient(sub, id, w.Header().Get("X-Request-ID"))
		if e != nil {
			s.collaborationError(w, r, e)
			return true
		}
		s.invalidateClient(id)
		if id == sub.Source.ClientID {
			for _, name := range []string{clientCookie, "slb_session"} {
				http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			}
		}
		jsonReply(w, 200, map[string]any{"revoked": true, "currentRevoked": id == sub.Source.ClientID})
	default:
		s.deny(w, r, 405, "method_not_allowed", "unsupported client operation")
	}
	return true
}
func (s *Server) invalidateClient(id string) {
	s.authMu.Lock()
	for k, b := range s.sessions {
		if b.subject.Source.ClientID == id {
			delete(s.sessions, k)
		}
	}
	for k, b := range s.tickets {
		if b.subject.Source.ClientID == id {
			delete(s.tickets, k)
		}
	}
	for k, b := range s.clientLinks {
		if b.subject.Source.ClientID == id {
			delete(s.clientLinks, k)
		}
	}
	s.authMu.Unlock()
	s.mu.Lock()
	for k, f := range s.forwards {
		if f.ClientID == id {
			delete(s.forwards, k)
		}
	}
	for k, g := range s.proxyTickets {
		if g.Subject.Source.ClientID == id {
			delete(s.proxyTickets, k)
		}
	}
	for k, g := range s.proxySessions {
		if g.Subject.Source.ClientID == id {
			delete(s.proxySessions, k)
		}
	}
	s.mu.Unlock()
	s.closeMatching(func(t *activeTunnel) bool { return t.actor.Source.ClientID == id }, "client_revoked")
}
