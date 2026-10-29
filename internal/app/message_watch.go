package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"seven-air-link/internal/client"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
)

type inboxCursor struct {
	Version      int    `json:"version"`
	Fingerprint  string `json:"fingerprint"`
	SubjectID    string `json:"subjectId"`
	With         string `json:"with"`
	Seq          int64  `json:"seq"`
	IncomingOnly bool   `json:"incomingOnly,omitempty"`
}

func (a *application) messageWatch(ctx context.Context, args []string) error {
	f := a.flags("message watch")
	cursor := f.Int64("cursor", 0, "initial replay cursor")
	file := f.String("cursor-file", "", "private resume file bound to server/identity/filter")
	with := f.String("with", "", "optional conversation filter")
	incoming := f.Bool("incoming-only", false, "skip this identity's own messages; still advance the cursor")
	timeout := f.Duration("timeout", 0, "maximum wait; 0 means foreground subscription")
	max := f.Int("max-messages", 0, "exit after N new messages; 0 means unlimited")
	if e := parse(f, args); e != nil {
		return e
	}
	if *cursor < 0 || *timeout < 0 || *max < 0 {
		return errors.New("cursor, timeout, and max-messages must be nonnegative")
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	c.HTTP.Timeout = 0
	state := inboxCursor{Version: 1, Fingerprint: c.Config.Fingerprint, SubjectID: c.Config.SubjectID, With: *with, IncomingOnly: *incoming, Seq: *cursor}
	if *file != "" {
		if b, e := os.ReadFile(*file); e == nil {
			var old inboxCursor
			if e = json.Unmarshal(b, &old); e != nil || old.Version != 1 || old.Fingerprint != state.Fingerprint || old.SubjectID != state.SubjectID || old.With != state.With || old.IncomingOnly != state.IncomingOnly || old.Seq < 0 {
				return errors.New("cursor file belongs to a different server, identity or filter, or is invalid")
			}
			state = old
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	emitted := 0
	connected := false
	timeoutResult := func() error {
		if !connected && ctx.Err() == context.DeadlineExceeded {
			return &model.APIError{Code: "inbox_unavailable", Message: "subscription could not stay connected before the timeout; no empty-inbox guarantee", Retryable: true}
		}
		return nil
	}
	commit := func(seq int64) error {
		if seq <= state.Seq {
			return nil
		}
		state.Seq = seq
		if *file != "" {
			return secure.WriteJSON(*file, state)
		}
		return nil
	}
	for {
		if ctx.Err() != nil {
			return timeoutResult()
		}
		query := url.Values{"cursor": {strconv.FormatInt(state.Seq, 10)}}
		if *with != "" {
			query.Set("with", *with)
		}
		resp, e := c.Request(ctx, "GET", "/api/v1/messages/stream?"+query.Encode(), nil)
		if errors.Is(e, secure.ErrPin) || errors.Is(e, secure.ErrCertificate) {
			return e
		}
		if e == nil {
			if resp.StatusCode != 200 {
				e = client.DecodeError(resp)
				resp.Body.Close()
				if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
					return e
				}
			} else {
				connected = true
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(make([]byte, 4096), 256<<10)
				kind, id, data := "", "", ""
				for scanner.Scan() {
					line := scanner.Text()
					if line == "" {
						if kind == "session.expired" || kind == "resync" {
							resp.Body.Close()
							return &model.APIError{Code: "stream_invalidated", Message: "identity expired/revoked or cursor invalidated; reauthenticate or explicitly refresh history"}
						}
						if (kind == "message.created" || kind == "checkpoint") && id != "" {
							seq, err := strconv.ParseInt(id, 10, 64)
							if err != nil || seq < 0 {
								resp.Body.Close()
								return errors.New("invalid stream sequence")
							}
							if seq > state.Seq {
								if kind == "message.created" {
									var m model.Message
									if err = json.Unmarshal([]byte(data), &m); err != nil || m.Seq != seq {
										resp.Body.Close()
										return errors.New("invalid stream message")
									}
									if !*incoming || m.SenderID != c.Config.SubjectID {
										if err = json.NewEncoder(a.out).Encode(m); err != nil {
											resp.Body.Close()
											return err
										}
										emitted++
									}
								}
								// Commit only after stdout delivery. A crash between write and commit
								// may replay the same ID; consumers must deduplicate side effects.
								if err = commit(seq); err != nil {
									resp.Body.Close()
									return err
								}
								if *max > 0 && emitted >= *max {
									resp.Body.Close()
									return nil
								}
							}
						}
						kind, id, data = "", "", ""
						continue
					}
					if strings.HasPrefix(line, "id: ") {
						id = strings.TrimPrefix(line, "id: ")
					}
					if strings.HasPrefix(line, "event: ") {
						kind = strings.TrimPrefix(line, "event: ")
					}
					if strings.HasPrefix(line, "data: ") {
						data = strings.TrimPrefix(line, "data: ")
					}
				}
				e = scanner.Err()
				resp.Body.Close()
				if e != nil && !errors.Is(e, io.EOF) && ctx.Err() == nil {
					fmt.Fprintln(a.err, "inbox stream interrupted; reconnecting from", state.Seq)
				}
			}
		}
		if ctx.Err() != nil {
			return timeoutResult()
		}
		connected = false
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return timeoutResult()
		case <-timer.C:
		}
	}
}
