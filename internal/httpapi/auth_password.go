package httpapi

import (
	"errors"
	"net/http"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"sort"
	"strings"
	"time"
)

// Password login is Owner-only, never a Node enrollment or Bot credential.
func (s *Server) ownerWebRevision(sub store.Subject) int64 {
	if sub.Role == "owner" {
		return s.st.WebPasswordStatus().Revision
	}
	return 0
}

// Bounded transient rate windows. The Owner CLI/ticket path remains recovery.
func (s *Server) passwordRate(key string, limit int) bool {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	now := time.Now()
	for k, v := range s.rates {
		if now.Sub(v.start) > time.Minute {
			delete(s.rates, k)
		}
	}
	v, ok := s.rates[key]
	if !ok {
		if len(s.rates) >= 1024 {
			return false
		}
		v = rateWindow{start: now}
	}
	v.n++
	s.rates[key] = v
	return v.n <= limit
}
func (s *Server) passwordSlot(w http.ResponseWriter, r *http.Request) bool {
	select {
	case s.passwordSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "2")
		s.deny(w, r, 429, "auth_busy", "password verification is busy; retry later")
		return false
	}
}
func (s *Server) loginPassword(w http.ResponseWriter, r *http.Request) {
	if !s.passwordRate("password-ip:"+remoteIP(r), 5) || !s.passwordRate("password-global", 30) {
		w.Header().Set("Retry-After", "60")
		s.deny(w, r, 429, "rate_limited", "too many password attempts; use a login ticket or retry later")
		return
	}
	var q struct {
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if !s.body(w, r, &q) {
		return
	}
	if len(q.Password) > secure.MaxPasswordBytes {
		s.deny(w, r, 400, "invalid_password", "password exceeds the size limit")
		return
	}
	if !s.passwordSlot(w, r) {
		return
	}
	defer func() { <-s.passwordSlots }()
	valid, rev := s.st.VerifyWebPassword(q.Password)
	q.Password = ""
	if !valid {
		s.audit(model.Event{ActorID: "anonymous", Action: "auth.password_denied", Result: "denied", Code: "invalid_credentials", RequestID: w.Header().Get("X-Request-ID")})
		s.deny(w, r, 403, "invalid_credentials", "password unavailable or incorrect")
		return
	}
	sub := store.Subject{ID: s.st.OwnerID(), Role: "owner"}
	s.authMu.Lock()
	if s.ownerWebRevision(sub) != rev {
		s.authMu.Unlock()
		s.deny(w, r, 403, "invalid_credentials", "password changed; sign in again")
		return
	}
	token, csrf, ok := s.issueSessionLocked(sub, rev, "password", r)
	s.authMu.Unlock()
	if !ok {
		s.deny(w, r, 429, "session_quota", "too many active browser sessions")
		return
	}
	s.replySession(w, r, sub, token, csrf)
}
func (s *Server) issueSessionLocked(sub store.Subject, rev int64, method string, r *http.Request) (string, string, bool) {
	now := time.Now()
	for k, b := range s.sessions {
		if now.After(b.expires) || !s.st.Active(b.subject) {
			delete(s.sessions, k)
		}
	}
	if len(s.sessions) >= 64 {
		return "", "", false
	}
	token, csrf := secure.Random("browser_"), secure.Random("csrf_")
	agent := r.UserAgent()
	if len(agent) > 256 {
		agent = agent[:256]
	}
	s.sessions[secure.Hash(token)] = browserSession{subject: sub, expires: now.Add(8 * time.Hour), csrf: csrf, id: secure.Random("session_")[:32], created: now, method: method, webRevision: rev, ip: remoteIP(r), userAgent: agent}
	return token, csrf, true
}
func (s *Server) replySession(w http.ResponseWriter, r *http.Request, sub store.Subject, token, csrf string) {
	http.SetCookie(w, &http.Cookie{Name: "slb_session", Value: token, Path: "/", MaxAge: 8 * 3600, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	s.audit(model.Event{ActorID: sub.ID, Action: "browser.login", Result: "success", RequestID: w.Header().Get("X-Request-ID")})
	jsonReply(w, 200, map[string]any{"ok": true, "csrf": csrf})
}
func (s *Server) authSettingsRoute(w http.ResponseWriter, r *http.Request, sub store.Subject) bool {
	path := r.URL.Path
	if path != "/api/v1/auth/password" && !strings.HasPrefix(path, "/api/v1/sessions/") && !(path == "/api/v1/sessions" && r.Method == "GET") {
		return false
	}
	if !participant(sub) {
		s.collaborationError(w, r, store.ErrDenied)
		return true
	}
	if path != "/api/v1/auth/password" {
		s.manageSessions(w, r, sub)
		return true
	}
	if sub.Role != "owner" {
		s.collaborationError(w, r, store.ErrDenied)
		return true
	}
	if r.Method == "GET" {
		jsonReply(w, 200, s.st.WebPasswordStatus())
		return true
	}
	if r.Method != "PUT" && r.Method != "DELETE" {
		s.deny(w, r, 405, "method_not_allowed", "use GET, PUT or DELETE")
		return true
	}
	var q struct {
		Password        string `json:"password"`
		CurrentPassword string `json:"currentPassword"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if !s.body(w, r, &q) {
		return true
	}
	if !s.passwordSlot(w, r) {
		return true
	}
	defer func() { <-s.passwordSlots }()
	old := s.st.WebPasswordStatus()
	// Browser changes require the current password. Owner Bearer can recover it.
	if r.Header.Get("Authorization") == "" && old.Enabled {
		if !s.passwordRate("password-change:"+remoteIP(r), 5) {
			s.deny(w, r, 429, "rate_limited", "too many password change attempts")
			return true
		}
		ok, rev := s.st.VerifyWebPassword(q.CurrentPassword)
		if !ok || rev != old.Revision {
			s.deny(w, r, 403, "invalid_current_password", "current password is incorrect or changed")
			return true
		}
	}
	encoded := ""
	if r.Method == "PUT" {
		var e error
		encoded, e = secure.HashPassword(q.Password)
		if e != nil {
			s.deny(w, r, 400, "password_policy", e.Error())
			return true
		}
	}
	q.Password = ""
	q.CurrentPassword = ""
	s.authMu.Lock()
	err := s.st.SetWebPassword(sub, encoded, old.Revision, w.Header().Get("X-Request-ID"))
	if err == nil {
		for k, b := range s.sessions {
			if b.subject.ID == sub.ID {
				delete(s.sessions, k)
			}
		}
		for k, b := range s.tickets {
			if b.subject.ID == sub.ID {
				delete(s.tickets, k)
			}
		}
	}
	s.authMu.Unlock()
	if errors.Is(err, store.ErrAuthRevision) {
		s.deny(w, r, 409, "auth_revision_conflict", err.Error())
		return true
	}
	if err != nil {
		s.deny(w, r, 503, "storage_unavailable", "password change was not committed")
		return true
	}
	s.mu.Lock()
	for k, g := range s.proxySessions {
		if g.Subject.ID == sub.ID {
			delete(s.proxySessions, k)
		}
	}
	for k, g := range s.proxyTickets {
		if g.Subject.ID == sub.ID {
			delete(s.proxyTickets, k)
		}
	}
	s.mu.Unlock()
	if _, e := r.Cookie("slb_session"); e == nil {
		http.SetCookie(w, &http.Cookie{Name: "slb_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	}
	jsonReply(w, 200, map[string]any{"password": s.st.WebPasswordStatus(), "ownerBrowserSessionsRevoked": true})
	return true
}

type sessionView struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Method    string    `json:"method"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent"`
	Current   bool      `json:"current"`
}

func (s *Server) manageSessions(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	current := ""
	if c, e := r.Cookie("slb_session"); e == nil {
		current = secure.Hash(c.Value)
	}
	if r.URL.Path == "/api/v1/sessions" && r.Method == "GET" {
		out := []sessionView{}
		s.authMu.Lock()
		for k, b := range s.sessions {
			if b.subject.ID == sub.ID && time.Now().Before(b.expires) && s.st.Active(b.subject) && b.webRevision == s.ownerWebRevision(b.subject) {
				out = append(out, sessionView{b.id, b.created, b.expires, b.method, b.ip, b.userAgent, k == current})
			}
		}
		s.authMu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
		jsonReply(w, 200, map[string]any{"sessions": out})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	if r.Method != "DELETE" || id == "" {
		s.deny(w, r, 405, "method_not_allowed", "use GET /sessions or DELETE /sessions/{id|all}")
		return
	}
	removed, self := 0, false
	s.authMu.Lock()
	for k, b := range s.sessions {
		if b.subject.ID == sub.ID && (id == "all" || b.id == id) {
			delete(s.sessions, k)
			removed++
			self = self || k == current
		}
	}
	s.authMu.Unlock()
	if removed == 0 && id != "all" {
		s.deny(w, r, 404, "session_not_found", "session not found for this identity")
		return
	}
	if self {
		http.SetCookie(w, &http.Cookie{Name: "slb_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	}
	s.audit(model.Event{ActorID: sub.ID, Action: "browser.sessions_revoked", Result: "success", RequestID: w.Header().Get("X-Request-ID")})
	jsonReply(w, 200, map[string]any{"revoked": removed, "currentRevoked": self})
}
