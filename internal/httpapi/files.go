package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
)

func (s *Server) upload(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	name, to := r.URL.Query().Get("name"), r.URL.Query().Get("to")
	if to == "" {
		to = "shared"
	}
	if !store.ValidFileName(name) {
		s.deny(w, r, 400, "invalid_filename", "a filename without path or control characters is required")
		return
	}
	if r.ContentLength < 0 || r.ContentLength > store.MaxFileBytes {
		s.deny(w, r, 413, "file_too_large", "Content-Length is required and must be at most 200 MiB")
		return
	}
	// Validate the recipient before reading data, then revalidate at commit time.
	if to != "shared" {
		p, ok := s.st.Principal(to)
		if !ok || p.Status != "active" {
			s.collaborationError(w, r, store.ErrPrincipalInactive)
			return
		}
	}
	select {
	case s.uploadSlots <- struct{}{}:
		defer func() { <-s.uploadSlots }()
	default:
		s.deny(w, r, 429, "upload_quota", "at most four concurrent uploads")
		return
	}
	dir := s.st.FileDir()
	if e := os.MkdirAll(dir, 0700); e != nil {
		s.collaborationError(w, r, e)
		return
	}
	tmp, e := os.CreateTemp(dir, ".upload-*")
	if e != nil {
		s.collaborationError(w, r, e)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxFileBytes)
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(tmp, h), r.Body)
	if e != nil || n != r.ContentLength {
		s.deny(w, r, 400, "upload_interrupted", "upload incomplete; no file was accepted")
		return
	}
	if e = tmp.Sync(); e != nil {
		s.collaborationError(w, r, e)
		return
	}
	if e = tmp.Close(); e != nil {
		s.collaborationError(w, r, e)
		return
	}
	f := model.File{ID: secure.Random("file_")[:29], Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), OwnerID: sub.ID, RecipientID: to, CreatedAt: time.Now().UTC()}
	path := filepath.Join(dir, f.ID)
	if e = os.Rename(tmp.Name(), path); e != nil {
		s.collaborationError(w, r, e)
		return
	}
	if e = s.st.PutFile(sub, f, w.Header().Get("X-Request-ID")); e != nil {
		_ = os.Remove(path)
		s.collaborationError(w, r, e)
		return
	}
	stored, _ := s.st.File(sub, f.ID)
	jsonReply(w, 201, stored)
}
func (s *Server) file(w http.ResponseWriter, r *http.Request, sub store.Subject) {
	suffix := strings.TrimPrefix(r.URL.Path, "/api/v1/files/")
	parts := strings.Split(suffix, "/")
	id := parts[0]
	// Only server-generated keys ever become filesystem paths.
	if !strings.HasPrefix(id, "file_") || len(id) != 29 || strings.ContainsAny(id, ".\\/") {
		s.deny(w, r, 404, "not_found", "file not found")
		return
	}
	if len(parts) == 1 && r.Method == "DELETE" {
		if e := s.st.DeleteFile(sub, id, w.Header().Get("X-Request-ID")); e != nil {
			s.collaborationError(w, r, e)
			return
		}
		e := os.Remove(filepath.Join(s.st.FileDir(), id))
		pending := e != nil && !os.IsNotExist(e)
		jsonReply(w, 200, map[string]bool{"deleted": true, "cleanupPending": pending})
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		s.deny(w, r, 404, "not_found", "unsupported file operation")
		return
	}
	f, e := s.st.File(sub, id)
	if e != nil {
		s.collaborationError(w, r, e)
		return
	}
	if len(parts) == 2 && parts[1] == "meta" {
		jsonReply(w, 200, f)
		return
	}
	if len(parts) != 1 {
		s.deny(w, r, 404, "not_found", "file not found")
		return
	}
	fh, e := os.Open(filepath.Join(s.st.FileDir(), id))
	if e != nil {
		s.deny(w, r, 404, "file_missing", "attachment bytes are missing; restore a backup")
		return
	}
	defer fh.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Content-SHA256", f.SHA256)
	w.Header().Set("ETag", fmt.Sprintf(`"%s"`, f.SHA256))
	s.audit(model.Event{ActorID: sub.ID, Action: "file.downloaded", ResourceID: id, Result: "authorized", RequestID: w.Header().Get("X-Request-ID")})
	http.ServeContent(w, r, f.Name, f.CreatedAt, &authorizedFile{file: fh, valid: func() bool { _, _, err := s.auth(r); return err == nil }})
}

// Recheck authentication for each read; bytes already buffered by TCP cannot be
// retracted, but revocation stops future file reads rather than waiting for EOF.
type authorizedFile struct {
	file  *os.File
	valid func() bool
}

func (f *authorizedFile) Read(p []byte) (int, error) {
	if !f.valid() {
		return 0, errors.New("file authorization expired")
	}
	return f.file.Read(p)
}

func (f *authorizedFile) Seek(offset int64, whence int) (int64, error) {
	return f.file.Seek(offset, whence)
}
