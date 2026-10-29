package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
)

func (s *Server) collaborationError(w http.ResponseWriter, r *http.Request, e error) {
	code, status, message := "invalid_request", 400, e.Error()
	switch {
	case errors.Is(e, store.ErrDenied):
		code, status, message = "access_denied", 403, "this identity cannot access this collaboration resource"
	case errors.Is(e, store.ErrPrincipalInactive):
		code, status, message = "principal_inactive", 403, "identity is pending, expired, revoked or bound to a revoked device"
	case errors.Is(e, store.ErrEnrollment):
		code, status, message = "enrollment_invalid_or_expired", 403, "binding code is invalid, consumed or expired"
	case errors.Is(e, store.ErrConflict):
		code, status = "principal_conflict", 409
	case errors.Is(e, store.ErrIdempotency):
		code, status = "idempotency_conflict", 409
	case errors.Is(e, store.ErrQuota):
		code, status, message = "quota_exceeded", 429, "local storage quota reached; no existing history was deleted"
	case errors.Is(e, os.ErrNotExist):
		code, status, message = "not_found", 404, "resource not found"
	default:
		var pe *os.PathError
		if errors.As(e, &pe) {
			code, status, message = "storage_unavailable", 503, "durable commit failed; operation was not accepted"
		}
	}
	if status == 403 {
		if actor, _, err := s.auth(r); err == nil {
			s.audit(model.Event{ActorID: actor.ID, Action: "collaboration.denied", Result: "denied", Code: code, RequestID: w.Header().Get("X-Request-ID")})
		}
	}
	s.deny(w, r, status, code, message)
}
func participant(sub store.Subject) bool { return sub.Role == "owner" || sub.Role == "member" }
func (s *Server) peerViews(sub store.Subject) []model.Principal {
	peers := s.st.Principals()
	out := []model.Principal{}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	for _, p := range peers {
		if sub.Role != "owner" && p.Status != "active" && p.ID != sub.ID {
			continue
		}
		p.Listening = s.inboxCount[p.ID]
		out = append(out, p)
	}
	return out
}

// Invalidate all process-owned capabilities when rotating or revoking a member.
func (s *Server) invalidatePrincipal(id string) {
	s.authMu.Lock()
	for key, b := range s.sessions {
		if b.subject.ID == id {
			delete(s.sessions, key)
		}
	}
	for key, b := range s.tickets {
		if b.subject.ID == id {
			delete(s.tickets, key)
		}
	}
	s.authMu.Unlock()
	s.mu.Lock()
	for key, f := range s.forwards {
		if f.ActorID == id {
			delete(s.forwards, key)
		}
	}
	for key, g := range s.proxySessions {
		if g.Subject.ID == id {
			delete(s.proxySessions, key)
		}
	}
	for key, g := range s.proxyTickets {
		if g.Subject.ID == id {
			delete(s.proxyTickets, key)
		}
	}
	s.mu.Unlock()
	s.closeMatching(func(t *activeTunnel) bool { return t.actor.ID == id }, "principal_inactive")
}
func (s *Server) claim(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Code string `json:"code"`
	}
	if !s.body(w, r, &q) {
		return
	}
	out, e := s.st.Claim(q.Code, w.Header().Get("X-Request-ID"))
	if e != nil {
		s.collaborationError(w, r, e)
		return
	}
	jsonReply(w, 201, out)
}
func (s *Server) collaborationRoute(w http.ResponseWriter, r *http.Request, sub store.Subject) bool {
	path := r.URL.Path
	isCollab := path == "/api/v1/peers" || strings.HasPrefix(path, "/api/v1/principals") || strings.HasPrefix(path, "/api/v1/messages") || strings.HasPrefix(path, "/api/v1/files") || path == "/api/v1/browser-tickets" || path == "/api/v1/sessions" || path == "/api/v1/activity" || path == "/api/v1/events"
	if !isCollab {
		return false
	}
	if !participant(sub) {
		s.collaborationError(w, r, store.ErrDenied)
		return true
	}
	if r.Method != "GET" && !s.allowedRate("participant:"+sub.ID) {
		s.deny(w, r, 429, "rate_limited", "participant mutation limit: 60/minute")
		return true
	}
	req := w.Header().Get("X-Request-ID")
	switch {
	case path == "/api/v1/peers" && r.Method == "GET":
		jsonReply(w, 200, map[string]any{"peers": s.peerViews(sub), "workspace": "shared", "note": "listening means an active inbox subscription, not an AI task status"})
	case path == "/api/v1/principals" && r.Method == "POST":
		var q model.PrincipalCreate
		if !s.body(w, r, &q) {
			return true
		}
		out, e := s.st.CreatePrincipal(sub, q, req)
		if e != nil {
			s.collaborationError(w, r, e)
		} else {
			jsonReply(w, 201, out)
		}
	case strings.HasPrefix(path, "/api/v1/principals/"):
		parts := strings.Split(strings.TrimPrefix(path, "/api/v1/principals/"), "/")
		if sub.Role != "owner" {
			s.collaborationError(w, r, store.ErrDenied)
			break
		}
		if len(parts) == 1 && r.Method == "DELETE" {
			s.mu.Lock()
			e := s.st.RevokePrincipal(sub, parts[0], req)
			s.mu.Unlock()
			if e != nil {
				s.collaborationError(w, r, e)
			} else {
				s.invalidatePrincipal(parts[0])
				jsonReply(w, 200, map[string]bool{"revoked": true})
			}
		} else if len(parts) == 2 && parts[1] == "rotate" && r.Method == "POST" {
			s.mu.Lock()
			out, e := s.st.Reenroll(sub, parts[0], req)
			s.mu.Unlock()
			if e != nil {
				s.collaborationError(w, r, e)
			} else {
				s.invalidatePrincipal(parts[0])
				jsonReply(w, 201, out)
			}
		} else {
			s.deny(w, r, 404, "not_found", "unsupported identity operation")
		}
	case path == "/api/v1/messages" && r.Method == "POST":
		var q model.MessageSend
		if !s.messageBody(w, r, &q) {
			return true
		}
		out, duplicate, e := s.st.Send(sub, q, req)
		if e != nil {
			s.collaborationError(w, r, e)
		} else {
			status := 201
			if duplicate {
				status = 200
			}
			jsonReply(w, status, map[string]any{"message": out, "duplicate": duplicate})
		}
	case path == "/api/v1/messages" && r.Method == "GET":
		s.messageList(w, r, sub)
	case path == "/api/v1/messages/stream" && r.Method == "GET":
		s.messageStream(w, r, sub)
	case strings.HasPrefix(path, "/api/v1/messages/") && r.Method == "GET":
		out, e := s.st.Message(sub, strings.TrimPrefix(path, "/api/v1/messages/"))
		if e != nil {
			s.collaborationError(w, r, e)
		} else {
			jsonReply(w, 200, out)
		}
	case path == "/api/v1/files" && r.Method == "POST":
		s.upload(w, r, sub)
	case path == "/api/v1/files" && r.Method == "GET":
		jsonReply(w, 200, map[string]any{"files": s.st.Files(sub)})
	case strings.HasPrefix(path, "/api/v1/files/"):
		s.file(w, r, sub)
	case path == "/api/v1/browser-tickets" && r.Method == "POST":
		s.createTicket(w, r, sub)
	case path == "/api/v1/sessions" && r.Method == "DELETE":
		s.logout(w, r)
	case path == "/api/v1/activity" && r.Method == "GET":
		s.activityFor(w, r, sub)
	case path == "/api/v1/events" && r.Method == "GET":
		s.eventsFor(w, r, sub)
	default:
		s.deny(w, r, 404, "not_found", "unsupported collaboration operation")
	}
	return true
}
func (s *Server) messageBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		s.deny(w, r, 415, "unsupported_media_type", "application/json required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	e := d.Decode(v)
	if e == nil {
		var extra any
		if e = d.Decode(&extra); e == io.EOF {
			return true
		}
	}
	var max *http.MaxBytesError
	if errors.As(e, &max) {
		s.deny(w, r, 413, "body_too_large", "message request exceeds 256 KiB")
	} else {
		s.deny(w, r, 400, "invalid_request", "malformed message JSON or unknown field")
	}
	return false
}
func (s *Server) messageList(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	q := r.URL.Query()
	after, before := int64(-1), int64(0)
	limit := 50
	var e error
	if q.Has("after") {
		after, e = strconv.ParseInt(q.Get("after"), 10, 64)
		if e != nil || after < 0 {
			s.deny(w, r, 400, "invalid_cursor", "invalid after")
			return
		}
	}
	if q.Has("before") {
		before, e = strconv.ParseInt(q.Get("before"), 10, 64)
		if e != nil || before < 1 || after >= 0 {
			s.deny(w, r, 400, "invalid_cursor", "before must be positive; do not combine with after")
			return
		}
	}
	if q.Has("limit") {
		limit, e = strconv.Atoi(q.Get("limit"))
		if e != nil || limit < 1 || limit > 100 {
			s.deny(w, r, 400, "invalid_limit", "limit must be 1..100")
			return
		}
	}
	out, e := s.st.Messages(sub, q.Get("with"), after, before, limit)
	if e != nil {
		s.collaborationError(w, r, e)
	} else {
		jsonReply(w, 200, out)
	}
}
func eventCursor(r *http.Request) (int64, error) {
	str := r.Header.Get("Last-Event-ID")
	if str == "" {
		str = r.URL.Query().Get("cursor")
	}
	if str == "" {
		return 0, nil
	}
	c, e := strconv.ParseInt(str, 10, 64)
	if e != nil || c < 0 {
		return 0, errors.New("invalid cursor")
	}
	return c, nil
}
func (s *Server) messageStream(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	cursor, e := eventCursor(r)
	if e != nil {
		s.deny(w, r, 400, "invalid_cursor", e.Error())
		return
	}
	with := r.URL.Query().Get("with")
	if _, e = s.st.Messages(sub, with, cursor, 0, 100); e != nil {
		s.deny(w, r, 409, "cursor_expired", "refresh the message history after a restore")
		return
	}
	s.authMu.Lock()
	if s.sseCount >= 64 || s.inboxCount[sub.ID] >= 4 {
		s.authMu.Unlock()
		s.deny(w, r, 429, "stream_quota", "inbox limit is four streams per identity")
		return
	}
	s.sseCount++
	s.inboxCount[sub.ID]++
	s.authMu.Unlock()
	defer func() {
		s.authMu.Lock()
		s.sseCount--
		s.inboxCount[sub.ID]--
		if s.inboxCount[sub.ID] == 0 {
			delete(s.inboxCount, sub.ID)
		}
		s.authMu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	flush := func(text string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, e := io.WriteString(w, text); e != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !flush("retry: 1000\nevent: ready\ndata: {}\n\n") {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	last := time.Now()
	for {
		current, _, e := s.auth(r)
		if e != nil || current != sub {
			flush("event: session.expired\ndata: {}\n\n")
			return
		}
		page, e := s.st.Messages(sub, with, cursor, 0, 100)
		if e != nil {
			flush("event: resync\ndata: {}\n\n")
			return
		}
		for _, m := range page.Messages {
			b, _ := json.Marshal(m)
			if !flush(fmt.Sprintf("id: %d\nevent: message.created\ndata: %s\n\n", m.Seq, b)) {
				return
			}
		}
		// A checkpoint permits efficient resume across messages the caller cannot see.
		if page.NextCursor > cursor {
			cursor = page.NextCursor
			if !flush(fmt.Sprintf("id: %d\nevent: checkpoint\ndata: {}\n\n", cursor)) {
				return
			}
		}
		if page.HasMore {
			continue
		}
		if time.Since(last) > 10*time.Second {
			if !flush(": heartbeat\n\n") {
				return
			}
			last = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func visibleActivity(events []model.Event, sub store.Subject, r *http.Request) []model.Event {
	out := []model.Event{}
	actor, correlation := r.URL.Query().Get("actor"), r.URL.Query().Get("correlation")
	for _, ev := range events {
		if sub.Role != "owner" && ev.ActorID != sub.ID {
			continue
		}
		if actor != "" && ev.ActorID != actor {
			continue
		}
		if correlation != "" && ev.CorrelationID != correlation {
			continue
		}
		out = append(out, ev)
	}
	return out
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("slb_session"); e == nil {
		s.authMu.Lock()
		delete(s.sessions, secure.Hash(c.Value))
		s.authMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "slb_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	jsonReply(w, 200, map[string]bool{"ok": true})
}
