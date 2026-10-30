package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/terminal"
	"strings"
)

func (a *application) authCommand(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: auth password set|status|disable [--data DIR] [--config OWNER_JSON]; auth sessions list|revoke ID|all")
	}
	if args[0] == "sessions" {
		c, e := a.api()
		if e != nil {
			return e
		}
		defer c.Close()
		var out any
		if args[1] == "list" && len(args) == 2 {
			e = c.Do(ctx, "GET", "/api/v1/sessions", nil, &out)
		} else if args[1] == "revoke" && len(args) == 3 && !strings.ContainsAny(args[2], "/?#") {
			e = c.Do(ctx, "DELETE", "/api/v1/sessions/"+args[2], nil, &out)
		} else {
			return errors.New("usage: auth sessions list|revoke ID|all")
		}
		if e != nil {
			return e
		}
		return a.print(out)
	}
	if args[0] != "password" {
		return errors.New("unknown auth command")
	}
	f := a.flags("auth password " + args[1])
	stdin := f.Bool("password-stdin", false, "read one password line from stdin (never a command-line argument)")
	data := f.String("data", "", "server data directory containing admin.json")
	yes := f.Bool("yes", false, "confirm disabling password login; ticket login remains enabled")
	if e := parse(f, args[2:]); e != nil {
		return e
	}
	if *data != "" {
		if a.opt.configExplicit {
			return errors.New("use either --data or --config, not both")
		}
		a.opt.config = filepath.Join(*data, "admin.json")
	} else if !a.opt.configExplicit {
		a.opt.config = filepath.Join(defaultData(), "admin.json")
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	if c.Config.Role != "owner" {
		return errors.New("password management requires the Owner profile, not a device or Bot credential")
	}
	var out any
	switch args[1] {
	case "status":
		if *stdin || *yes {
			return errors.New("status takes no password/confirmation flags")
		}
		e = c.Do(ctx, "GET", "/api/v1/auth/password", nil, &out)
	case "disable":
		if !*yes {
			return errors.New("disabling password login requires --yes; ticket authentication will remain enabled")
		}
		e = c.Do(ctx, "DELETE", "/api/v1/auth/password", map[string]any{}, &out)
	case "set":
		if *yes {
			return errors.New("set does not accept --yes")
		}
		var pw []byte
		if *stdin {
			pw, e = io.ReadAll(io.LimitReader(a.in, secure.MaxPasswordBytes+3))
			if e != nil {
				return e
			}
			pw = []byte(strings.TrimSuffix(strings.TrimSuffix(string(pw), "\n"), "\r"))
		} else {
			in, ok := a.in.(*os.File)
			if !ok {
				return errors.New("interactive password input requires a terminal; use --password-stdin")
			}
			pw, e = terminal.ReadPassword(ctx, in, a.err, "Set administrator web password: ")
			if e != nil {
				return e
			}
			confirm, err := terminal.ReadPassword(ctx, in, a.err, "Confirm password: ")
			if err != nil {
				clear(pw)
				return err
			}
			same := secure.Equal(string(pw), string(confirm))
			clear(confirm)
			if !same {
				clear(pw)
				return errors.New("passwords do not match")
			}
		}
		defer clear(pw)
		if e = secure.ValidatePassword(string(pw)); e != nil {
			return e
		}
		e = c.Do(ctx, "PUT", "/api/v1/auth/password", map[string]string{"password": string(pw)}, &out)
		if e == nil {
			fmt.Fprintln(a.err, "Web password configured. Existing Owner browser sessions were revoked; Node/Bot credentials are unchanged.")
		}
	default:
		return errors.New("usage: auth password set|status|disable")
	}
	if e != nil {
		return e
	}
	return a.print(out)
}
