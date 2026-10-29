package store

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"seven-air-link/internal/model"
)

const MaxFileBytes = 200 << 20
const MaxFileTotal = 1 << 30

func (st *Store) FileDir() string { return filepath.Join(filepath.Dir(st.path), "uploads") }
func ValidFileName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 240 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func fileVisible(f model.File, sub Subject) bool {
	return f.RecipientID == "shared" || f.OwnerID == sub.ID || f.RecipientID == sub.ID
}
func (st *Store) PutFile(sub Subject, f model.File, req string) error {
	if !ValidFileName(f.Name) || f.Size < 0 || f.Size > MaxFileBytes || len(f.SHA256) != 64 {
		return errors.New("invalid file metadata")
	}
	return st.mutate(func(s *snapshot) error {
		if (sub.Role != "member" && sub.Role != "owner") || !activeSubject(s, sub) {
			return ErrDenied
		}
		if f.RecipientID != "shared" && !activePrincipal(s, f.RecipientID) {
			return ErrPrincipalInactive
		}
		if _, exists := s.Files[f.ID]; exists {
			return ErrConflict
		}
		total := int64(0)
		for _, old := range s.Files {
			if old.DeletedAt == nil {
				total += old.Size
			}
		}
		if total+f.Size > MaxFileTotal || len(s.Files) >= 4096 {
			return ErrQuota
		}
		f.OwnerID = sub.ID
		f.CreatedAt = time.Now().UTC()
		s.Files[f.ID] = f
		addEvent(s, model.Event{ActorID: sub.ID, Action: "file.uploaded", ResourceID: f.ID, Result: "success", RequestID: req})
		return nil
	})
}
func (st *Store) File(sub Subject, id string) (model.File, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if (sub.Role != "member" && sub.Role != "owner") || !activeSubject(&st.s, sub) {
		return model.File{}, ErrDenied
	}
	f, ok := st.s.Files[id]
	if !ok || f.DeletedAt != nil || !fileVisible(f, sub) {
		return model.File{}, os.ErrNotExist
	}
	return f, nil
}
func (st *Store) Files(sub Subject) []model.File {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := []model.File{}
	if (sub.Role != "owner" && sub.Role != "member") || !activeSubject(&st.s, sub) {
		return out
	}
	for _, f := range st.s.Files {
		if f.DeletedAt == nil && fileVisible(f, sub) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
func (st *Store) DeleteFile(sub Subject, id, req string) error {
	return st.mutate(func(s *snapshot) error {
		if !activeSubject(s, sub) {
			return ErrDenied
		}
		f, ok := s.Files[id]
		if !ok {
			return os.ErrNotExist
		}
		// Server operators have filesystem access, but API moderation does not
		// silently grant them the content of somebody else's private attachment.
		if f.OwnerID != sub.ID {
			return ErrDenied
		}
		if f.DeletedAt != nil {
			return nil
		}
		now := time.Now().UTC()
		f.DeletedAt = &now
		s.Files[id] = f
		addEvent(s, model.Event{ActorID: sub.ID, Action: "file.deleted", ResourceID: id, Result: "success", RequestID: req})
		return nil
	})
}
