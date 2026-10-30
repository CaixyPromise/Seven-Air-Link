package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"seven-air-link/internal/client"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"strconv"
	"strings"
	"time"
)

type noteConflictError struct {
	Note model.SharedNote
	API  model.APIError
}

func (e *noteConflictError) Error() string { return e.API.Error() }
func (a *application) note(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: note get|set|watch")
	}
	f := a.flags("note " + args[0])
	revision := f.Int64("revision", -1, "required expected revision for set; never silently overwrites another writer")
	text := f.String("text", "", "full note text (may be empty)")
	file := f.String("file", "", "read full UTF-8 text from file")
	stdin := f.Bool("stdin", false, "read full UTF-8 text from stdin")
	correlation := f.String("correlation-id", "", "optional operation correlation ID")
	timeout := f.Duration("timeout", 0, "watch timeout, e.g. 30s")
	maxUpdates := f.Int("max-updates", 0, "stop watch after N snapshots/updates, 0 means unlimited")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	switch args[0] {
	case "get":
		var n model.SharedNote
		if e = c.Do(ctx, "GET", "/api/v1/note", nil, &n); e != nil {
			return e
		}
		return a.print(n)
	case "set":
		hasText := false
		f.Visit(func(v *flag.Flag) {
			if v.Name == "text" {
				hasText = true
			}
		})
		count := 0
		if hasText {
			count++
		}
		if *file != "" {
			count++
		}
		if *stdin {
			count++
		}
		if count != 1 || *revision < 0 {
			return errors.New("note set requires --revision N and exactly one of --text TEXT, --stdin, --file PATH")
		}
		content := *text
		if !hasText {
			reader := a.in
			if *file != "" {
				h, err := os.Open(*file)
				if err != nil {
					return err
				}
				defer h.Close()
				reader = h
			}
			b, err := io.ReadAll(io.LimitReader(reader, store.MaxNoteBytes+1))
			if err != nil {
				return err
			}
			content = string(b)
		}
		if len(content) > store.MaxNoteBytes {
			return errors.New("note exceeds 65536 bytes")
		}
		resp, err := c.Request(ctx, "PUT", "/api/v1/note", model.NoteWrite{Text: content, BaseRevision: revision, CorrelationID: *correlation})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode == 409 {
			var v struct {
				Error model.APIError   `json:"error"`
				Note  model.SharedNote `json:"note"`
			}
			if e = json.NewDecoder(io.LimitReader(resp.Body, 512<<10)).Decode(&v); e != nil {
				return e
			}
			return &noteConflictError{Note: v.Note, API: v.Error}
		}
		if resp.StatusCode != 200 {
			return client.DecodeError(resp)
		}
		var n model.SharedNote
		if e = json.NewDecoder(io.LimitReader(resp.Body, 512<<10)).Decode(&n); e != nil {
			return e
		}
		return a.print(n)
	case "watch":
		if *timeout < 0 || *maxUpdates < 0 {
			return errors.New("watch bounds must be nonnegative")
		}
		return a.watchNote(ctx, c, *timeout, *maxUpdates)
	default:
		return errors.New("usage: note get|set|watch")
	}
}
func (a *application) watchNote(parent context.Context, c *client.Client, timeout time.Duration, maxUpdates int) error {
	ctx := parent
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	}
	defer cancel()
	c.HTTP.Timeout = 0
	last := int64(-1)
	updates := 0
	connected := false
	finish := func() error {
		if parent.Err() != nil || connected {
			return nil
		}
		return &model.APIError{Code: "note_unavailable", Message: "could not maintain an authenticated note subscription", Retryable: true}
	}
	for ctx.Err() == nil {
		path := "/api/v1/note/events"
		if last >= 0 {
			path += "?after=" + strconv.FormatInt(last, 10)
		}
		resp, e := c.Request(ctx, "GET", path, nil)
		if errors.Is(e, secure.ErrPin) || errors.Is(e, secure.ErrCertificate) {
			return e
		}
		if e == nil && resp.StatusCode != 200 {
			err := client.DecodeError(resp)
			resp.Body.Close()
			if resp.StatusCode < 500 && resp.StatusCode != 429 {
				return err
			}
			e = err
		}
		if e == nil {
			connected = true
			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(make([]byte, 4096), 512<<10)
			kind, data := "", ""
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					if kind == "session.expired" {
						resp.Body.Close()
						return &model.APIError{Code: "unauthenticated", Message: "note subscription identity expired"}
					}
					if (kind == "note.snapshot" || kind == "note.updated") && data != "" {
						var n model.SharedNote
						if err := json.Unmarshal([]byte(data), &n); err != nil {
							resp.Body.Close()
							return err
						}
						if n.Revision > last {
							if err := json.NewEncoder(a.out).Encode(n); err != nil {
								resp.Body.Close()
								return err
							}
							last = n.Revision
							updates++
							if maxUpdates > 0 && updates >= maxUpdates {
								resp.Body.Close()
								return nil
							}
						}
					}
					kind, data = "", ""
					continue
				}
				if strings.HasPrefix(line, "event: ") {
					kind = strings.TrimPrefix(line, "event: ")
				}
				if strings.HasPrefix(line, "data: ") {
					data = strings.TrimPrefix(line, "data: ")
				}
			}
			resp.Body.Close()
			if ctx.Err() != nil {
				return finish()
			}
			connected = false
		}
		if ctx.Err() != nil {
			return finish()
		}
		fmt.Fprintln(a.err, "note stream interrupted; reconnecting to the latest snapshot")
		select {
		case <-ctx.Done():
			return finish()
		case <-time.After(time.Second):
		}
	}
	return finish()
}
