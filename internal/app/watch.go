package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"seven-air-link/internal/client"
	"seven-air-link/internal/secure"
	"strconv"
	"strings"
	"time"
)

func (a *application) activity(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: activity list|watch")
	}
	f := a.flags("activity " + args[0])
	after := f.Int64("after", -1, "list events after cursor")
	limit := f.Int("limit", 100, "list limit, 1..500")
	actor := f.String("actor", "", "filter by actor ID")
	correlation := f.String("correlation", "", "filter by correlation ID")
	cursor := f.Int64("cursor", 0, "initial watch cursor")
	cursorFile := f.String("cursor-file", "", "explicit local resume cursor file")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	if args[0] == "list" {
		if *limit < 1 || *limit > 500 {
			return errors.New("limit must be 1..500")
		}
		path := "/api/v1/activity?limit=" + strconv.Itoa(*limit) + "&actor=" + url.QueryEscape(*actor) + "&correlation=" + url.QueryEscape(*correlation)
		if *after >= 0 {
			path += "&after=" + strconv.FormatInt(*after, 10)
		}
		var out any
		if e = c.Do(ctx, "GET", path, nil, &out); e != nil {
			return e
		}
		return a.print(out)
	}
	if args[0] != "watch" {
		return errors.New("usage: activity list|watch")
	}
	if *cursor < 0 {
		return errors.New("cursor must be >=0")
	}
	if *cursorFile != "" {
		if b, e := os.ReadFile(*cursorFile); e == nil {
			v, e := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			if e != nil || v < 0 {
				return errors.New("invalid cursor file")
			}
			*cursor = v
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	c.HTTP.Timeout = 0 // Streaming requests use context cancellation, not total request timeout.
	for {
		if ctx.Err() != nil {
			return nil
		}
		path := "/api/v1/events?cursor=" + strconv.FormatInt(*cursor, 10) + "&actor=" + url.QueryEscape(*actor) + "&correlation=" + url.QueryEscape(*correlation)
		resp, e := c.Request(ctx, "GET", path, nil)
		if errors.Is(e, secure.ErrPin) || errors.Is(e, secure.ErrCertificate) {
			return e // A changed trust anchor requires a human decision, not endless retries.
		}
		if e == nil {
			if resp.StatusCode != 200 {
				e = client.DecodeError(resp)
				resp.Body.Close()
				if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 409 {
					return e
				}
			}
			if resp.StatusCode == http.StatusOK {
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(make([]byte, 4096), 64*1024)
				id := ""
				kind := ""
				data := ""
				for scanner.Scan() {
					line := scanner.Text()
					if line == "" {
						if kind == "session.expired" || kind == "resync" {
							resp.Body.Close()
							return errors.New("event session expired or cursor compacted; refresh snapshot/login and retry")
						}
						if (kind == "activity" || kind == "checkpoint") && data != "" {
							var obj any
							if e = json.Unmarshal([]byte(data), &obj); e != nil {
								resp.Body.Close()
								return e
							}
							seq, e := strconv.ParseInt(id, 10, 64)
							if e != nil {
								resp.Body.Close()
								return e
							}
							if seq > *cursor {
								if kind == "activity" {
									if e = json.NewEncoder(a.out).Encode(obj); e != nil {
										resp.Body.Close()
										return e
									}
								}
								*cursor = seq
								if *cursorFile != "" {
									if e = secure.AtomicWrite(*cursorFile, []byte(id+"\n"), 0600); e != nil {
										resp.Body.Close()
										return e
									}
								}
							}
						}
						id = ""
						kind = ""
						data = ""
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
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprintln(a.err, "activity stream interrupted; retrying with cursor", *cursor)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
