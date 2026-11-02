package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"seven-air-link/internal/client"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/terminal"
)

// Clients call ONE existing Server; none of these commands starts a server or
// imports its admin profile. Browser binding issues a fresh local credential.
func (a *application) clientCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: client login|bind URL|list|revoke ID|node --out FILE")
	}
	switch args[0] {
	case "login", "bind":
		if len(args) < 2 {
			return errors.New("Server HTTPS URL required")
		}
		origin := args[1]
		f := a.flags("client " + args[0])
		pin := f.String("fingerprint", "", "independently verified Server SHA-256")
		host, _ := os.Hostname()
		if host == "" {
			host = "Client"
		}
		name := f.String("name", host+" CLI", "display name (not an authorization claim)")
		passwordStdin := f.Bool("password-stdin", false, "read admission password from protected stdin")
		codeStdin := f.Bool("code-stdin", false, "read one-use browser binding code from protected stdin")
		if e := parse(f, args[2:]); e != nil {
			return e
		}
		if (*passwordStdin && args[0] != "login") || (*codeStdin && args[0] != "bind") {
			return errors.New("use password-stdin for login, code-stdin for bind")
		}
		if !a.opt.configExplicit {
			return errors.New("client enrollment requires explicit --config NEW_FILE to avoid overwriting another identity")
		}
		if e := absentFile(a.opt.config); e != nil {
			return e
		}
		cfg := model.Config{Version: 1, Server: strings.TrimSuffix(origin, "/"), Fingerprint: *pin, Role: "member"}
		c, e := client.New(cfg)
		if e != nil {
			return e
		}
		defer c.Close()
		// Verify the destination before requesting or transmitting a secret.
		if e = c.Do(ctx, "GET", "/api/v1/info", nil, nil); e != nil {
			return e
		}
		var secret []byte
		if *passwordStdin || *codeStdin {
			secret, e = io.ReadAll(io.LimitReader(a.in, secure.MaxPasswordBytes+3))
			secret = []byte(strings.TrimSuffix(strings.TrimSuffix(string(secret), "\n"), "\r"))
		} else {
			in, ok := a.in.(*os.File)
			if !ok {
				return errors.New("interactive terminal required or use explicit stdin flag")
			}
			prompt := "Joining password: "
			if args[0] == "bind" {
				prompt = "Browser CLI binding code: "
			}
			secret, e = terminal.ReadPassword(ctx, in, a.err, prompt)
		}
		if e != nil {
			return e
		}
		defer clear(secret)
		if len(secret) == 0 || len(secret) > secure.MaxPasswordBytes {
			return errors.New("invalid secret length")
		}
		path := "/api/v1/clients/login"
		key := "password"
		if args[0] == "bind" {
			path = "/api/v1/clients/claim"
			key = "code"
		}
		var claim model.ClientClaim
		if e = c.Do(ctx, "POST", path, map[string]string{key: string(secret), "name": *name}, &claim); e != nil {
			return e
		}
		cfg = c.Config
		cfg.Token = claim.Token
		cfg.SubjectID = claim.Principal.ID
		cfg.Role = "member"
		if e = secure.WriteNewJSON(a.opt.config, cfg); e != nil {
			return fmt.Errorf("client %s issued but profile save failed; revoke this client: %w", claim.Client.ID, e)
		}
		return a.print(map[string]any{"principal": claim.Principal, "client": claim.Client, "config": a.opt.config})
	case "list", "revoke", "node":
		c, e := a.api()
		if e != nil {
			return e
		}
		defer c.Close()
		var out any
		switch args[0] {
		case "list":
			if len(args) != 1 {
				return errors.New("usage: client list --config FILE")
			}
			if e = c.Do(ctx, "GET", "/api/v1/clients", nil, &out); e != nil {
				return e
			}
		case "revoke":
			if len(args) != 2 || !safeID(args[1]) {
				return errors.New("usage: client revoke CLIENT_ID")
			}
			if e = c.Do(ctx, "DELETE", "/api/v1/clients/"+args[1], nil, &out); e != nil {
				return e
			}
		case "node":
			f := a.flags("client node")
			dest := f.String("out", "", "NEW Node-only profile on this machine")
			host, _ := os.Hostname()
			name := f.String("name", host, "Node display name")
			if e = parse(f, args[1:]); e != nil {
				return e
			}
			if e = absentFile(*dest); e != nil {
				return e
			}
			var pairing model.PairingResponse
			if e = c.Do(ctx, "POST", "/api/v1/client/device-pairings", map[string]any{}, &pairing); e != nil {
				return e
			}
			var joined model.JoinResponse
			if e = c.Do(ctx, "POST", "/api/v1/join", model.JoinRequest{Code: pairing.Code, Name: *name, OS: runtime.GOOS, Arch: runtime.GOARCH, NodeVersion: model.Version, Protocol: model.Protocol}, &joined); e != nil {
				return e
			}
			cfg := c.Config
			cfg.Role = "device"
			cfg.SubjectID = joined.DeviceID
			cfg.Token = joined.Token
			if e = secure.WriteNewJSON(*dest, cfg); e != nil {
				return fmt.Errorf("device %s enrolled but file creation failed; request local Server revocation: %w", joined.DeviceID, e)
			}
			out = map[string]any{"deviceId": joined.DeviceID, "config": *dest, "next": "node start --config " + *dest}
		}
		return a.print(out)
	}
	return errors.New("usage: client login|bind URL|list|revoke ID|node --out FILE")
}
