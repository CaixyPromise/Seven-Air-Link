package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"seven-air-link/internal/model"
	"seven-air-link/internal/store"
	"strconv"
	"time"
)

func (s *Server) noteRoute(w http.ResponseWriter, r *http.Request, sub store.Subject) bool {
	if r.URL.Path != "/api/v1/note" && r.URL.Path != "/api/v1/note/events" {
		return false
	}
	if !participant(sub) {
		s.collaborationError(w, r, store.ErrDenied)
		return true
	}
	if r.URL.Path == "/api/v1/note/events" && r.Method == "GET" {
		s.noteStream(w, r, sub)
		return true
	}
	switch r.Method {
	case "GET":
		n, e := s.st.SharedNote(sub)
		if e != nil {
			s.collaborationError(w, r, e)
		} else {
			jsonReply(w, 200, n)
		}
	case "PUT":
		if r.URL.Path != "/api/v1/note" {
			s.deny(w, r, 405, "method_not_allowed", "use GET for note events")
			return true
		}
		if !s.passwordRate("note-write:"+sub.ID, 120) {
			w.Header().Set("Retry-After", "60")
			s.deny(w, r, 429, "rate_limited", "note limit: 120 writes per identity per minute")
			return true
		}
		var q model.NoteWrite
		if !s.messageBody(w, r, &q) {
			return true
		}
		n, e := s.st.UpdateSharedNote(sub, q, w.Header().Get("X-Request-ID"))
		var conflict *store.NoteConflict
		if errors.As(e, &conflict) {
			jsonReply(w, 409, map[string]any{"error": model.APIError{Code: "note_revision_conflict", Message: "another participant updated the note; your draft was not saved", RequestID: w.Header().Get("X-Request-ID")}, "note": conflict.Current})
		} else if e != nil {
			s.collaborationError(w, r, e)
		} else {
			jsonReply(w, 200, n)
		}
	default:
		s.deny(w, r, 405, "method_not_allowed", "use GET or PUT")
	}
	return true
}

// This is a latest-state stream, not a revision log. Revision CAS controls writes.
func (s *Server) noteStream(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	after := r.URL.Query().Get("after")
	if after == "" {
		after = r.Header.Get("Last-Event-ID")
	}
	if after != "" {
		if n, e := strconv.ParseInt(after, 10, 64); e != nil || n < 0 {
			s.deny(w, r, 400, "invalid_cursor", "note revision must be nonnegative")
			return
		}
	}
	s.authMu.Lock()
	if s.sseCount >= 64 || s.noteCount[sub.ID] >= 4 {
		s.authMu.Unlock()
		s.deny(w, r, 429, "sse_quota", "too many note subscriptions")
		return
	}
	s.sseCount++
	s.noteCount[sub.ID]++
	s.authMu.Unlock()
	defer func() {
		s.authMu.Lock()
		s.sseCount--
		s.noteCount[sub.ID]--
		if s.noteCount[sub.ID] == 0 {
			delete(s.noteCount, sub.ID)
		}
		s.authMu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	flush := func(b string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, e := io.WriteString(w, b); e != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !flush("retry: 1000\n\n") {
		return
	}
	last := int64(-1)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.Now()
	for {
		if _, _, e := s.auth(r); e != nil {
			flush("event: session.expired\ndata: {}\n\n")
			return
		}
		n, e := s.st.SharedNote(sub)
		if e != nil {
			flush("event: session.expired\ndata: {}\n\n")
			return
		}
		if n.Revision != last {
			b, _ := json.Marshal(n)
			event := "note.updated"
			if last < 0 {
				event = "note.snapshot"
			}
			if !flush(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", n.Revision, event, b)) {
				return
			}
			last = n.Revision
		}
		if time.Since(heartbeat) > 10*time.Second {
			if !flush(": heartbeat\n\n") {
				return
			}
			heartbeat = time.Now()
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
