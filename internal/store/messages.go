package store

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
)

const MaxMessages = 10000
const MaxMessageBytes = 64 << 20
const MaxTextBytes = 32768

var keyRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func ValidateMessage(q model.MessageSend) error {
	if !keyRE.MatchString(q.ClientMessageID) {
		return errors.New("clientMessageId must be 1..128 safe characters")
	}
	if q.To == "" || len(q.To) > 80 || (q.Text == "" && len(q.Attachments) == 0) || len(q.Text) > MaxTextBytes || !utf8.ValidString(q.Text) || strings.ContainsRune(q.Text, 0) {
		return errors.New("recipient and UTF-8 text (up to 32 KiB) or attachments are required")
	}
	if len(q.Attachments) > 8 || (q.CorrelationID != "" && !keyRE.MatchString(q.CorrelationID)) || len(q.ReplyTo) > 80 {
		return errors.New("invalid correlation/reply or too many attachments")
	}
	return nil
}
func conversation(m model.Message, sub Subject, with string) bool {
	if m.RecipientID != "shared" && m.SenderID != sub.ID && m.RecipientID != sub.ID {
		return false
	}
	if with == "" {
		return true
	}
	if with == "shared" {
		return m.RecipientID == "shared"
	}
	return m.RecipientID != "shared" && ((m.SenderID == sub.ID && m.RecipientID == with) || (m.RecipientID == sub.ID && m.SenderID == with))
}
func (st *Store) Send(sub Subject, q model.MessageSend, req string) (model.Message, bool, error) {
	if err := ValidateMessage(q); err != nil {
		return model.Message{}, false, err
	}
	if q.Attachments == nil {
		q.Attachments = []string{}
	}
	var out model.Message
	duplicate := false
	err := st.mutate(func(s *snapshot) error {
		if (sub.Role != "owner" && sub.Role != "member") || !activeSubject(s, sub) {
			return ErrDenied
		}
		// Match before target liveness checks: a retry must still discover a previous
		// durable success, even if that recipient has since been revoked.

		prior, e := st.queryMessages("WHERE sender_id=? AND client_message_id=?", sub.ID, q.ClientMessageID)
		if e != nil {
			return e
		}
		if len(prior) > 0 {
			m := prior[0]
			if m.Text != q.Text || m.RecipientID != q.To || m.ReplyTo != q.ReplyTo || m.CorrelationID != q.CorrelationID || !reflect.DeepEqual(m.Attachments, q.Attachments) {
				return ErrIdempotency
			}
			out = m
			duplicate = true
			return nil
		}

		if q.To != "shared" && !activePrincipal(s, q.To) {
			return ErrPrincipalInactive
		}
		if s.MessageSeq >= MaxMessages || s.MessageBytes+int64(len(q.Text)) > MaxMessageBytes {
			return ErrQuota
		}

		if q.ReplyTo != "" {
			replies, e := st.queryMessages("WHERE id=?", q.ReplyTo)
			if e != nil {
				return e
			}
			if len(replies) != 1 || !conversation(replies[0], sub, q.To) {
				return errors.New("replyTo must be a visible message in the same conversation")
			}
		}

		for _, id := range q.Attachments {
			f, ok := s.Files[id]
			if !ok || f.DeletedAt != nil || !fileVisible(f, sub) {
				return ErrDenied
			}
			// Forwarding a private reference must not silently expand its original ACL.
			if f.RecipientID != "shared" && (q.To == "shared" || (q.To != f.OwnerID && q.To != f.RecipientID)) {
				return ErrDenied
			}
		}
		s.MessageSeq++
		out = model.Message{ID: secure.Random("msg_")[:28], Seq: s.MessageSeq, ClientMessageID: q.ClientMessageID, SenderID: sub.ID, RecipientID: q.To, Kind: "text", Text: q.Text, Attachments: append([]string{}, q.Attachments...), ReplyTo: q.ReplyTo, CorrelationID: q.CorrelationID, Status: "accepted", CreatedAt: time.Now().UTC()}
		s.Messages = append(s.Messages, out)
		s.MessageBytes += int64(len(q.Text))
		// No message body, command text, attachment name or secret enters audit.
		addEvent(s, model.Event{ActorID: sub.ID, Action: "message.accepted", ResourceID: out.ID, Result: "success", RequestID: req, CorrelationID: q.CorrelationID})
		return nil
	})
	out.Attachments = append([]string{}, out.Attachments...)
	return out, duplicate, err
}
func (st *Store) Message(sub Subject, id string) (model.Message, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if (sub.Role != "owner" && sub.Role != "member") || !activeSubject(&st.s, sub) {
		return model.Message{}, ErrDenied
	}
	ms, e := st.queryMessages("WHERE id=? AND (recipient_id='shared' OR sender_id=? OR recipient_id=?)", id, sub.ID, sub.ID)
	if e != nil {
		return model.Message{}, e
	}
	if len(ms) != 1 {
		return model.Message{}, os.ErrNotExist
	}
	return ms[0], nil
}
func messageFilter(sub Subject, with string) (string, []any) {
	if with == "shared" {
		return "recipient_id='shared'", nil
	}
	if with != "" {
		return "recipient_id<>'shared' AND ((sender_id=? AND recipient_id=?) OR (recipient_id=? AND sender_id=?))", []any{sub.ID, with, sub.ID, with}
	}
	return "(recipient_id='shared' OR sender_id=? OR recipient_id=?)", []any{sub.ID, sub.ID}
}

// Messages queries indexed sequence ranges; private payloads never leave the
// store. Resume cursors advance over invisible records without exposing bodies.
func (st *Store) Messages(sub Subject, with string, after, before int64, limit int) (model.MessagePage, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := model.MessagePage{Messages: []model.Message{}, SnapshotSeq: st.s.MessageSeq}
	if (sub.Role != "owner" && sub.Role != "member") || !activeSubject(&st.s, sub) {
		return out, ErrDenied
	}
	if limit < 1 || limit > 100 || after < -1 || before < 0 || after > st.s.MessageSeq {
		return out, errors.New("invalid limit or message cursor")
	}
	filter, args := messageFilter(sub, with)
	where := "WHERE " + filter
	if after >= 0 {
		where += " AND seq>? ORDER BY seq ASC LIMIT ?"
		args = append(args, after, limit+1)
	} else if before > 0 {
		where += " AND seq<? ORDER BY seq DESC LIMIT ?"
		args = append(args, before, limit+1)
	} else {
		where += " ORDER BY seq DESC LIMIT ?"
		args = append(args, limit+1)
	}
	ms, e := st.queryMessages(where, args...)
	if e != nil {
		return out, e
	}
	out.HasMore = len(ms) > limit
	if out.HasMore {
		ms = ms[:limit]
	}
	out.Messages = ms
	if len(ms) > 0 {
		out.NextCursor = ms[len(ms)-1].Seq
	}
	if after >= 0 && !out.HasMore {
		out.NextCursor = st.s.MessageSeq
	}
	return out, nil
}

// Validate essential replay invariants rather than silently dropping corrupt state.
func validateCollaboration(s snapshot) error {
	var bytes int64
	last := int64(0)
	ids := map[string]bool{}
	keys := map[string]bool{}
	for _, m := range s.Messages {
		key := m.SenderID + "/" + m.ClientMessageID
		if m.Seq <= last || m.Seq > s.MessageSeq || m.ID == "" || ids[m.ID] || keys[key] {
			return errors.New("corrupt message sequence/idempotency index")
		}
		ids[m.ID] = true
		keys[key] = true
		last = m.Seq
		bytes += int64(len(m.Text))
	}
	if last != s.MessageSeq || bytes != s.MessageBytes || len(s.Messages) > MaxMessages || bytes > MaxMessageBytes {
		return errors.New("corrupt message counts or exceeded snapshot limits")
	}
	for id, r := range s.Principals {
		if id != r.Principal.ID || r.Principal.Role != "member" || (r.Principal.Type != "human" && r.Principal.Type != "bot") || (r.TokenHash != "" && len(r.TokenHash) != 64) || r.Principal.ExpiresAt == nil {
			return errors.New("corrupt principal registry")
		}
	}
	return nil
}
func (st *Store) ExportMessages(sub Subject) ([]byte, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if !activeSubject(&st.s, sub) || (sub.Role != "owner" && sub.Role != "member") {
		return nil, ErrDenied
	}
	filter, args := messageFilter(sub, "")
	ms, e := st.queryMessages("WHERE "+filter+" ORDER BY seq", args...)
	if e != nil {
		return nil, e
	}
	return json.Marshal(ms)
}
