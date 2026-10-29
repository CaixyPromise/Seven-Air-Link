package app

import (
	"bufio"
	"context"
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
	"seven-air-link/skills"
)

func localValueFlag(s string) bool {
	switch s {
	case "--text", "--name", "--from", "--to", "--type", "--device", "--out", "--ttl", "--fingerprint", "--code-file", "--idempotency-key", "--reply-to", "--correlation-id", "--attach", "--with", "--after", "--before", "--limit", "--cursor", "--cursor-file", "--timeout", "--max-messages", "--listen", "--port", "--proxy-listen", "--data", "--tls-host", "--heartbeat", "--offline-after", "--max-devices", "--connect-timeout", "--max-tunnels", "--max-per-service", "--max-per-actor", "--target", "--allow-devices", "--allow-principals", "--actor", "--correlation":
		return true
	}
	return false
}
func absentFile(path string) error {
	if path == "" {
		return errors.New("--out/--config path required")
	}
	if _, e := os.Lstat(path); e == nil {
		return errors.New("destination already exists; choose a new file, do not overwrite another identity")
	} else if !os.IsNotExist(e) {
		return e
	}
	return nil
}
func (a *application) saveClaim(c *client.Client, claim model.Claim, path string) error {
	cfg := c.Config
	cfg.SubjectID = claim.Principal.ID
	cfg.Role = "member"
	cfg.Token = claim.Token
	if e := secure.WriteNewJSON(path, cfg); e != nil {
		return fmt.Errorf("identity %s is bound but config save failed; rotate or revoke it: %w", claim.Principal.ID, e)
	}
	return a.print(map[string]any{"principal": claim.Principal, "config": path, "note": "private credential file; use --config for every Bot command; no Agent was started"})
}
func (a *application) createIdentity(ctx context.Context, args []string, kind string, requireOut bool) error {
	f := a.flags("identity create")
	name := f.String("name", "", "participant display name")
	typ := f.String("type", kind, "human or bot")
	device := f.String("device", "", "optional explicit bound device; revocation disables this identity")
	ttl := f.Duration("ttl", 30*24*time.Hour, "credential lifetime, 1m..8760h")
	out := f.String("out", "", "private credential file; without it, return a one-use enrollment code")
	if e := parse(f, args); e != nil {
		return e
	}
	if *name == "" || (*typ != "bot" && *typ != "human") || (requireOut && *out == "") {
		return errors.New("name, type=human|bot and (for bot bind) --out are required")
	}
	if requireOut && *typ != "bot" {
		return errors.New("bot bind requires type bot")
	}
	if *ttl < time.Minute || *ttl > 365*24*time.Hour {
		return errors.New("ttl must be 1m..8760h")
	}
	if *out != "" {
		if e := absentFile(*out); e != nil {
			return e
		}
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	var invitation model.Enrollment
	if e = c.Do(ctx, "POST", "/api/v1/principals", model.PrincipalCreate{Name: *name, Type: *typ, DeviceID: *device, TTLSeconds: int64(ttl.Seconds())}, &invitation); e != nil {
		return e
	}
	if *out == "" {
		return a.print(invitation)
	}
	var claim model.Claim
	if e = c.Do(ctx, "POST", "/api/v1/identity/claim", map[string]string{"code": invitation.Code}, &claim); e != nil {
		return fmt.Errorf("identity %s created, enrollment failed; rotate/revoke this identity: %w", invitation.Principal.ID, e)
	}
	return a.saveClaim(c, claim, *out)
}
func (a *application) identity(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: identity whoami|create|bind|rotate|revoke|use")
	}
	switch args[0] {
	case "create":
		return a.createIdentity(ctx, args[1:], "human", false)
	case "bind":
		return a.bindIdentity(ctx, args[1:])
	case "use":
		f := a.flags("identity use")
		from := f.String("from", "", "existing private profile")
		if e := parse(f, args[1:]); e != nil {
			return e
		}
		cfg, e := client.Load(*from)
		if e != nil {
			return e
		}
		if e = secure.WriteJSON(a.opt.config, cfg); e != nil {
			return e
		}
		return a.print(map[string]any{"id": cfg.SubjectID, "role": cfg.Role, "config": a.opt.config})
	case "revoke", "rotate":
		if len(args) < 2 || !safeID(args[1]) {
			return errors.New("principal ID required")
		}
		f := a.flags("identity " + args[0])
		out := f.String("out", "", "save rotated credential to a NEW private profile")
		if e := parse(f, args[2:]); e != nil {
			return e
		}
		if *out != "" {
			if e := absentFile(*out); e != nil {
				return e
			}
		}
		c, e := a.api()
		if e != nil {
			return e
		}
		defer c.Close()
		path := "/api/v1/principals/" + args[1]
		if args[0] == "revoke" {
			var res any
			if e = c.Do(ctx, "DELETE", path, nil, &res); e != nil {
				return e
			}
			return a.print(res)
		}
		var enrollment model.Enrollment
		if e = c.Do(ctx, "POST", path+"/rotate", map[string]any{}, &enrollment); e != nil {
			return e
		}
		if *out == "" {
			return a.print(enrollment)
		}
		var claim model.Claim
		if e = c.Do(ctx, "POST", "/api/v1/identity/claim", map[string]string{"code": enrollment.Code}, &claim); e != nil {
			return e
		}
		return a.saveClaim(c, claim, *out)
	}
	return errors.New("usage: identity whoami|create|bind|rotate|revoke|use")
}
func (a *application) bindIdentity(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: identity bind https://SERVER --fingerprint SHA256 --config PROFILE")
	}
	origin := args[0]
	f := a.flags("identity bind")
	pin := f.String("fingerprint", "", "independently verified server SHA256")
	file := f.String("code-file", "", "read one-use binding code from file")
	stdin := f.Bool("code-stdin", false, "read binding code from stdin")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	if *file != "" && *stdin {
		return errors.New("choose code-file or code-stdin")
	}
	if e := absentFile(a.opt.config); e != nil {
		return e
	}
	cfg := model.Config{Version: 1, Server: origin, Fingerprint: *pin, Role: "member"}
	c, e := client.New(cfg)
	if e != nil {
		return e
	}
	defer c.Close()
	// Validate TLS pin BEFORE asking for or sending the enrollment code.
	if e = c.Do(ctx, "GET", "/api/v1/info", nil, nil); e != nil {
		return e
	}
	var code string
	if *file != "" {
		fh, e := os.Open(*file)
		if e != nil {
			return e
		}
		defer fh.Close()
		b, e := io.ReadAll(io.LimitReader(fh, 512))
		if e != nil {
			return e
		}
		code = strings.TrimSpace(string(b))
	} else {
		if !*stdin {
			fmt.Fprint(a.err, "一次性身份绑定码（输入会回显）： ")
		}
		line, e := bufio.NewReader(io.LimitReader(a.in, 512)).ReadString('\n')
		if e != nil && e != io.EOF {
			return e
		}
		code = strings.TrimSpace(line)
	}
	var claim model.Claim
	if e = c.Do(ctx, "POST", "/api/v1/identity/claim", map[string]string{"code": code}, &claim); e != nil {
		return e
	}
	return a.saveClaim(c, claim, a.opt.config)
}
func (a *application) bot(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bot bind|create|list|revoke|rotate")
	}
	switch args[0] {
	case "bind":
		return a.createIdentity(ctx, args[1:], "bot", true)
	case "create":
		return a.createIdentity(ctx, args[1:], "bot", false)
	case "revoke", "rotate":
		return a.identity(ctx, args)
	case "list":
		return a.peers(ctx, append([]string{"list", "--type", "bot"}, args[1:]...))
	}
	return errors.New("usage: bot bind|create|list|revoke|rotate")
}
func (a *application) peers(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: peers list [--type human|bot]")
	}
	f := a.flags("peers list")
	typ := f.String("type", "", "filter human or bot")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	if *typ != "" && *typ != "human" && *typ != "bot" {
		return errors.New("type must be human or bot")
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	var out struct {
		Peers []model.Principal `json:"peers"`
	}
	if e = c.Do(ctx, "GET", "/api/v1/peers", nil, &out); e != nil {
		return e
	}
	if *typ != "" {
		filtered := []model.Principal{}
		for _, p := range out.Peers {
			if p.Type == *typ {
				filtered = append(filtered, p)
			}
		}
		out.Peers = filtered
	}
	return a.print(out)
}
func (a *application) message(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: message send|list|get|watch")
	}
	if args[0] == "watch" {
		return a.messageWatch(ctx, args[1:])
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	switch args[0] {
	case "send":
		f := a.flags("message send")
		to := f.String("to", "shared", "shared or principal ID")
		text := f.String("text", "", "UTF-8 text")
		stdin := f.Bool("stdin", false, "read UTF-8 text from stdin")
		key := f.String("idempotency-key", "", "stable retry key")
		reply := f.String("reply-to", "", "message in the same conversation")
		corr := f.String("correlation-id", "", "operation correlation ID")
		attach := f.String("attach", "", "file IDs, comma-separated")
		if e = parse(f, args[1:]); e != nil {
			return e
		}
		if *stdin {
			if *text != "" {
				return errors.New("choose text or stdin")
			}
			b, e := io.ReadAll(io.LimitReader(a.in, 32769))
			if e != nil {
				return e
			}
			if len(b) > 32768 {
				return errors.New("message exceeds 32 KiB")
			}
			*text = string(b)
		}
		if *key == "" {
			*key = secure.Random("cli_")
		}
		q := model.MessageSend{ClientMessageID: *key, To: *to, Text: *text, ReplyTo: *reply, CorrelationID: *corr, Attachments: csv(*attach)}
		var out any
		if e = c.Do(ctx, "POST", "/api/v1/messages", q, &out); e != nil {
			var ae *model.APIError
			if errors.As(e, &ae) && ae.Retryable {
				copy := *ae
				copy.Message += "; retry unchanged content with --idempotency-key " + *key
				return &copy
			}
			return e
		}
		return a.print(out)
	case "list":
		f := a.flags("message list")
		with := f.String("with", "", "conversation ID or shared")
		after := f.Int64("after", -1, "ascending replay cursor")
		before := f.Int64("before", 0, "older history cursor")
		limit := f.Int("limit", 50, "1..100")
		if e = parse(f, args[1:]); e != nil {
			return e
		}
		q := url.Values{"limit": {strconv.Itoa(*limit)}}
		if *with != "" {
			q.Set("with", *with)
		}
		if *after >= 0 {
			q.Set("after", strconv.FormatInt(*after, 10))
		}
		if *before > 0 {
			q.Set("before", strconv.FormatInt(*before, 10))
		}
		var out any
		if e = c.Do(ctx, "GET", "/api/v1/messages?"+q.Encode(), nil, &out); e != nil {
			return e
		}
		return a.print(out)
	case "get":
		if len(args) != 2 || !safeID(args[1]) {
			return errors.New("usage: message get MESSAGE_ID")
		}
		var out any
		if e = c.Do(ctx, "GET", "/api/v1/messages/"+args[1], nil, &out); e != nil {
			return e
		}
		return a.print(out)
	}
	return errors.New("usage: message send|list|get|watch")
}
func (a *application) skill(args []string) error {
	if len(args) != 1 || args[0] != "show" {
		return errors.New("usage: skill show")
	}
	b, e := skills.Assets.ReadFile("seven-air-link/SKILL.md")
	if e != nil {
		return e
	}
	if a.opt.output != "text" {
		return a.print(map[string]string{"content": string(b)})
	}
	_, e = a.out.Write(b)
	return e
}
