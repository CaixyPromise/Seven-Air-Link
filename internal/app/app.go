// Package app assembles commands; it never launches an unmanaged background process.
package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"seven-air-link/internal/client"
	"seven-air-link/internal/filelock"
	"seven-air-link/internal/httpapi"
	"seven-air-link/internal/model"
	"seven-air-link/internal/node"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"strconv"
	"strings"
	"time"
)

const help = `Seven-Air-Link 0.1.0-rc.2 — Human / Bot collaboration and local service networking

Usage:
  seven-air-link init [--data DIR] [--port 7777] [--tls-host HOST,IP]
  seven-air-link serve [--data DIR] [--listen 0.0.0.0:7777] [--port PORT]
  seven-air-link pairing create --config ADMIN_JSON [--ttl 5m] [--out FILE]
  seven-air-link join https://SERVER:7777 --fingerprint SHA256 --name NAME
                       --config NODE_JSON [--code-file FILE | --code-stdin]
  seven-air-link node start --config NODE_JSON
  seven-air-link devices list --config ADMIN_JSON
  seven-air-link devices rename DEVICE_ID --name NAME --config ADMIN_JSON
  seven-air-link devices revoke DEVICE_ID --config ADMIN_JSON
  seven-air-link status --config CONFIG_JSON
  seven-air-link identity whoami --config CONFIG_JSON
  seven-air-link doctor --config CONFIG_JSON
  seven-air-link login create --config PRINCIPAL_JSON
  seven-air-link activity list --config PRINCIPAL_JSON [--after N] [--limit 100]
                                [--actor ID] [--correlation KEY]
  seven-air-link activity watch --config ADMIN_JSON [--cursor N]
                                 [--cursor-file FILE] --output ndjson
  seven-air-link version

Common flags: --config FILE, --server https://HOST:PORT, --output text|json|ndjson.
Global flags may appear before or after the subcommand. Node runs in foreground.
Pairing codes are single-use. Secrets are read from stdin/files, NOT URL/argv.
M4 offline maintenance (stop Server first, destinations must be new):
  storage check --data DIR [--hash-files]
  storage backup --data DIR --out NEW_BACKUP_DIR
  storage verify --from BACKUP_DIR
  storage restore --from BACKUP_DIR --data NEW_DIR
  storage migrate --data DIR  (legacy JSON -> SQLite)
  storage gc --data DIR [--min-age 24h] [--apply]
  storage export-json --data DIR --out NEW_JSON
M2 networking (all long-running commands stay in the foreground):
  serve --proxy-listen 0.0.0.0:7778  (optional isolated HTTP proxy origin)
  share http|tcp --name NAME --target 127.0.0.1:PORT --config NODE_JSON
                 [--allow-devices DEVICE_ID,...] [--allow-owner]
  share stop SERVICE --config NODE_JSON
  services list --config CONFIG
  services grants SERVICE [--allow-devices IDS] [--allow-owner] --config CONFIG
  services open SERVICE --config CONFIG   (one-use browser service ticket)
  services stop SERVICE --config CONFIG
  proxy open SERVICE --listen 127.0.0.1:18080 --config CONFIG
  proxy list --config CONFIG
  proxy close FORWARD_ID --config CONFIG
M3 collaboration (Human and Bot share the same API; no Agent runtime):
  bot bind --name NAME --out BOT_JSON [--device DEVICE_ID] --config ADMIN_JSON
  identity create --type human|bot --name NAME [--out PROFILE] --config ADMIN_JSON
  identity bind https://SERVER:7777 --fingerprint SHA256 --config PROFILE
                [--code-file FILE | --code-stdin]
  identity rotate PRINCIPAL_ID [--out NEW_PROFILE] --config ADMIN_JSON
  identity revoke PRINCIPAL_ID --config ADMIN_JSON
  identity use --from PROFILE --config ACTIVE_PROFILE
  peers list [--type human|bot] --config PROFILE
  message send --to shared|PRINCIPAL_ID (--text TEXT | --stdin)
               [--idempotency-key KEY] [--reply-to ID] [--correlation-id KEY]
               [--attach FILE_ID,...] --config PROFILE
  message list [--with shared|PRINCIPAL_ID] [--after SEQ | --before SEQ] [--limit 50]
  message get MESSAGE_ID
  message watch [--cursor N] [--cursor-file FILE] [--with PRINCIPAL_ID]
                [--incoming-only] [--timeout 30s] [--max-messages 1] --output ndjson
  file send PATH [--to shared|PRINCIPAL_ID] --config PROFILE
  file list | file get FILE_ID --out PATH | file delete FILE_ID
  skill show
Network service ACL additionally supports --allow-principals PRINCIPAL_ID,...
Identity creation requires Owner; member credentials do not inherit administration.
Messages are durable acceptance, NOT confirmation that another AI executed a task.
`

type options struct{ config, server, output string }
type application struct {
	in       io.Reader
	out, err io.Writer
	opt      options
}

func common(args []string) ([]string, options, error) {
	o := options{config: defaultConfig(), output: "text"}
	out := []string{}
	for i := 0; i < len(args); i++ {
		key, val, eq := strings.Cut(args[i], "=")
		var p *string
		switch key {
		case "--config":
			p = &o.config
		case "--server":
			p = &o.server
		case "--output":
			p = &o.output
		}
		if p == nil {
			out = append(out, args[i])
			if !eq && localValueFlag(key) && i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
			continue
		}
		if !eq {
			i++
			if i >= len(args) {
				return nil, o, fmt.Errorf("missing value for %s", key)
			}
			val = args[i]
		}
		*p = val
	}
	if o.output != "text" && o.output != "json" && o.output != "ndjson" {
		return nil, o, errors.New("output must be text, json or ndjson")
	}
	return out, o, nil
}
func Run(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) int {
	rest, opt, e := common(args)
	a := &application{in: in, out: out, err: errout, opt: opt}
	if e == nil {
		e = a.run(ctx, rest)
	}
	if e == nil || errors.Is(e, flag.ErrHelp) {
		return 0
	}
	if errors.Is(e, context.Canceled) {
		return 0
	}
	var storageErr *storageCheckError
	if errors.As(e, &storageErr) {
		_ = json.NewEncoder(out).Encode(map[string]any{"error": model.APIError{Code: "storage_integrity_failed", Message: e.Error()}, "report": storageErr.Report})
		return 1
	}
	var apiErr *model.APIError
	if !errors.As(e, &apiErr) {
		apiErr = &model.APIError{Code: "command_failed", Message: e.Error()}
	}
	if opt.output == "json" || opt.output == "ndjson" {
		_ = json.NewEncoder(out).Encode(map[string]any{"error": apiErr})
	} else {
		fmt.Fprintln(errout, "Error:", apiErr.Error())
	}
	return 1
}
func (a *application) print(v any) error {
	enc := json.NewEncoder(a.out)
	if a.opt.output != "ndjson" {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}
func (a *application) flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(a.err) // Diagnostics never corrupt machine-readable stdout.
	return f
}
func parse(f *flag.FlagSet, args []string) error {
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(f.Args(), " "))
	}
	return nil
}
func (a *application) api() (*client.Client, error) {
	c, e := client.Load(a.opt.config)
	if e != nil {
		return nil, fmt.Errorf("load --config %s: %w", a.opt.config, e)
	}
	if a.opt.server != "" {
		c.Server = a.opt.server
	}
	return client.New(c)
}
func (a *application) run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		_, e := io.WriteString(a.out, help)
		return e
	}
	switch args[0] {
	case "version":
		if len(args) > 1 {
			return errors.New("version takes no arguments")
		}
		return a.print(map[string]any{"version": model.Version, "protocol": model.Protocol, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH})
	case "init":
		f := a.flags("init")
		data := f.String("data", defaultData(), "new Go server data directory (never Python shared_data)")
		port := f.Int("port", 7777, "future HTTPS port")
		hosts := f.String("tls-host", "", "extra certificate SANs, comma-separated")
		if e := parse(f, args[1:]); e != nil {
			return e
		}
		l, e := filelock.Acquire(filepath.Join(*data, "server.lock"))
		if e != nil {
			return e
		}
		defer l.Close()
		info, e := initData(*data, *port, strings.Split(*hosts, ","))
		if e != nil {
			return e
		}
		return a.print(info)
	case "storage":
		return a.storage(ctx, args[1:])
	case "serve":
		return a.serve(ctx, args[1:])
	case "join":
		return a.join(ctx, args[1:])
	case "node":
		if len(args) < 2 || args[1] != "start" {
			return errors.New("usage: node start --config NODE_JSON")
		}
		f := a.flags("node start")
		if e := parse(f, args[2:]); e != nil {
			return e
		}
		cfg, e := client.Load(a.opt.config)
		if e != nil {
			return e
		}
		if a.opt.server != "" {
			cfg.Server = a.opt.server
		}
		if e = client.ValidateServer(cfg.Server); e != nil {
			return e
		}
		lock, e := filelock.Acquire(a.opt.config + ".run.lock")
		if e != nil {
			return e
		}
		defer lock.Close()
		policy, control := node.LocalPaths(a.opt.config)
		return node.Run(ctx, cfg, node.Options{Log: a.err, PolicyPath: policy, ControlFile: control})
	case "pairing":
		if len(args) < 2 || args[1] != "create" {
			return errors.New("usage: pairing create --config ADMIN_JSON")
		}
		f := a.flags("pairing create")
		ttl := f.Duration("ttl", 5*time.Minute, "one-time code validity, 1s..15m")
		dest := f.String("out", "", "save code to a private file instead of stdout")
		if e := parse(f, args[2:]); e != nil {
			return e
		}
		if *ttl < time.Second || *ttl > 15*time.Minute {
			return errors.New("ttl must be 1s..15m")
		}
		c, e := a.api()
		if e != nil {
			return e
		}
		defer c.Close()
		var p model.PairingResponse
		if e = c.Do(ctx, "POST", "/api/v1/pairings", model.PairingRequest{TTLSeconds: int(ttl.Seconds())}, &p); e != nil {
			return e
		}
		if *dest != "" {
			if e = secure.AtomicWrite(*dest, []byte(p.Code+"\n"), 0600); e != nil {
				return e
			}
			return a.print(map[string]any{"codeFile": *dest, "expiresAt": p.ExpiresAt})
		}
		return a.print(p)
	case "devices":
		return a.devices(ctx, args[1:])
	case "status", "doctor", "identity":
		name := args[0]
		remaining := args[1:]
		if name == "identity" {
			if len(remaining) > 0 && remaining[0] != "whoami" {
				return a.identity(ctx, remaining)
			}
			if len(remaining) < 1 || remaining[0] != "whoami" {
				return errors.New("usage: identity whoami")
			}
			remaining = remaining[1:]
		}
		f := a.flags(name)
		if e := parse(f, remaining); e != nil {
			return e
		}
		c, e := a.api()
		if e != nil {
			return e
		}
		defer c.Close()
		path := "/api/v1/status"
		if name == "identity" {
			path = "/api/v1/me"
		}
		var result map[string]any
		if e = c.Do(ctx, "GET", path, nil, &result); e != nil {
			return e
		}
		if name == "doctor" {
			var ready map[string]any
			if e = c.Do(ctx, "GET", "/readyz", nil, &ready); e != nil {
				return e
			}
			if result["role"] == "owner" {
				var storageReport map[string]any
				if e = c.Do(ctx, "GET", "/api/v1/storage", nil, &storageReport); e != nil {
					return e
				}
				result["storage"] = storageReport
			}
			result["checks"] = []map[string]string{{"check": "TLS certificate fingerprint", "result": "pass"}, {"check": "credential authorization", "result": "pass"}, {"check": "SQLite read readiness", "result": "pass"}}
			result["note"] = "Only checks this profile and reachable server. Offline device may need node start. This does not verify OS firewall, port forwarding or other physical machines."
		}
		return a.print(result)
	case "login":
		if len(args) != 2 || args[1] != "create" {
			return errors.New("usage: login create --config ADMIN_JSON")
		}
		c, e := a.api()
		if e != nil {
			return e
		}
		defer c.Close()
		var result map[string]any
		if e = c.Do(ctx, "POST", "/api/v1/browser-tickets", map[string]any{}, &result); e != nil {
			return e
		}
		return a.print(result)
	case "activity":
		return a.activity(ctx, args[1:])
	case "share":
		return a.share(ctx, args[1:])
	case "services":
		return a.services(ctx, args[1:])
	case "proxy":
		return a.proxy(ctx, args[1:])
	case "bot":
		return a.bot(ctx, args[1:])
	case "message":
		return a.message(ctx, args[1:])
	case "file":
		return a.file(ctx, args[1:])
	case "peers":
		return a.peers(ctx, args[1:])
	case "skill":
		return a.skill(args[1:])
	default:
		return fmt.Errorf("unknown command %q; run --help", args[0])
	}
}
func (a *application) devices(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: devices list|rename|revoke")
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("devices list takes no arguments")
		}
		var out struct {
			Devices []model.Device `json:"devices"`
		}
		if e = c.Do(ctx, "GET", "/api/v1/devices", nil, &out); e != nil {
			return e
		}
		if a.opt.output != "text" {
			return a.print(out)
		}
		fmt.Fprintln(a.out, "ID\tNAME\tOS/ARCH\tSTATUS\tOBSERVED_IP")
		for _, d := range out.Devices {
			fmt.Fprintf(a.out, "%s\t%s\t%s/%s\t%s\t%s\n", d.ID, d.Name, d.OS, d.Arch, d.Status, d.IP)
		}
		return nil
	case "revoke", "rename":
		if len(args) < 2 {
			return errors.New("device ID required")
		}
		id := args[1]
		if strings.ContainsAny(id, "/\\?# ") {
			return errors.New("invalid device ID")
		}
		f := a.flags("devices " + args[0])
		name := f.String("name", "", "new device display name (rename only)")
		if e = parse(f, args[2:]); e != nil {
			return e
		}
		method := "DELETE"
		var in any
		if args[0] == "rename" {
			method = "PATCH"
			in = map[string]string{"name": *name}
		}
		var result map[string]any
		if e = c.Do(ctx, method, "/api/v1/devices/"+id, in, &result); e != nil {
			return e
		}
		return a.print(result)
	default:
		return errors.New("usage: devices list|rename|revoke")
	}
}
func (a *application) join(ctx context.Context, args []string) error {
	server := a.opt.server
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		server = args[0]
		args = args[1:]
	}
	f := a.flags("join")
	fp := f.String("fingerprint", "", "SHA-256 fingerprint from the SERVER terminal, required")
	name := f.String("name", "", "device display name")
	codeFile := f.String("code-file", "", "private file containing the one-time pairing code")
	codeStdin := f.Bool("code-stdin", false, "read code from standard input without prompt")
	if e := parse(f, args); e != nil {
		return e
	}
	if !store.ValidName(*name) {
		return errors.New("--name must contain 1..80 visible characters")
	}
	if e := client.ValidateServer(server); e != nil {
		return e
	}
	pin, e := secure.NormalizePin(*fp)
	if e != nil {
		return e
	}
	if *codeFile != "" && *codeStdin {
		return errors.New("choose --code-file OR --code-stdin")
	}
	lock, e := filelock.Acquire(a.opt.config + ".setup.lock")
	if e != nil {
		return e
	}
	defer lock.Close()
	if _, e = os.Stat(a.opt.config); e == nil {
		return errors.New("config already exists; choose a new profile or explicitly archive the old one before re-pairing")
	} else if !os.IsNotExist(e) {
		return e
	}
	var code string
	if *codeFile != "" {
		b, e := os.ReadFile(*codeFile)
		if e != nil {
			return e
		}
		if len(b) > 1024 {
			return errors.New("pairing code file too large")
		}
		code = strings.TrimSpace(string(b))
	} else {
		if !*codeStdin {
			fmt.Fprint(a.err, "One-time pairing code (will be consumed): ")
		}
		line, e := bufio.NewReader(io.LimitReader(a.in, 1025)).ReadString('\n')
		if e != nil && e != io.EOF {
			return e
		}
		code = strings.TrimSpace(line)
	}
	if len(code) < 16 || len(code) > 256 {
		return errors.New("invalid pairing code length")
	}
	cfg := model.Config{Version: 1, Server: strings.TrimSuffix(server, "/"), Fingerprint: pin, Role: "device"}
	c, e := client.New(cfg)
	if e != nil {
		return e
	}
	defer c.Close()
	var joined model.JoinResponse
	if e = c.Do(ctx, "POST", "/api/v1/join", model.JoinRequest{Code: code, Name: *name, OS: runtime.GOOS, Arch: runtime.GOARCH, NodeVersion: model.Version, Protocol: model.Protocol}, &joined); e != nil {
		return e
	}
	cfg.Token = joined.Token
	cfg.SubjectID = joined.DeviceID
	if e = secure.WriteJSON(a.opt.config, cfg); e != nil {
		return fmt.Errorf("device %s was paired, but config write failed; revoke this device and pair again: %w", joined.DeviceID, e)
	}
	return a.print(map[string]any{"paired": true, "deviceId": joined.DeviceID, "config": a.opt.config, "next": "node start --config " + a.opt.config})
}
func (a *application) serve(ctx context.Context, args []string) error {
	f := a.flags("serve")
	data := f.String("data", defaultData(), "Go server data directory")
	listen := f.String("listen", "0.0.0.0:7777", "HTTPS listen address")
	port := f.Int("port", 0, "override port in --listen")
	hosts := f.String("tls-host", "", "extra certificate SANs on first initialization")
	hb := f.Duration("heartbeat", 5*time.Second, "Node heartbeat interval")
	timeout := f.Duration("offline-after", 15*time.Second, "Node silence timeout (at least 3 heartbeats)")
	max := f.Int("max-devices", 64, "maximum trusted devices")
	proxyListen := f.String("proxy-listen", "", "optional isolated HTTPS service proxy listener, e.g. 0.0.0.0:7778")
	connectTimeout := f.Duration("connect-timeout", 5*time.Second, "tunnel setup budget")
	maxTunnels := f.Int("max-tunnels", 256, "global concurrent data tunnel limit")
	maxPerService := f.Int("max-per-service", 64, "concurrent data tunnel limit per service")
	maxPerActor := f.Int("max-per-actor", 128, "concurrent data tunnel limit per actor")
	if e := parse(f, args); e != nil {
		return e
	}
	host, ps, e := net.SplitHostPort(*listen)
	if e != nil {
		return fmt.Errorf("invalid --listen: %w", e)
	}
	p, e := strconv.Atoi(ps)
	if e != nil {
		return e
	}
	if *port != 0 {
		p = *port
	}
	if p < 0 || p > 65535 {
		return errors.New("port must be 1..65535 (or --listen HOST:0 for tests)")
	}
	if *connectTimeout < time.Second || *connectTimeout > 5*time.Second {
		return errors.New("connect-timeout must be between 1s and 5s")
	}
	for _, limit := range []int{*maxTunnels, *maxPerService, *maxPerActor} {
		if limit < 1 || limit > 4096 {
			return errors.New("tunnel limits must be 1..4096")
		}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(p))
	if *timeout > 120*time.Second {
		return errors.New("offline-after must not exceed 120s")
	}
	lock, e := filelock.Acquire(filepath.Join(*data, "server.lock"))
	if e != nil {
		return e
	}
	defer lock.Close()
	initPort := p
	if initPort == 0 {
		initPort = 7777
	}
	info, e := initData(*data, initPort, strings.Split(*hosts, ","))
	if e != nil {
		return e
	}
	st, e := store.Open(filepath.Join(*data, "bridge.db"))
	if e != nil {
		return e
	}
	defer st.Close()
	cert, e := secure.ReadCertificate(filepath.Join(*data, "server.crt"))
	if e != nil {
		return e
	}
	api, e := httpapi.New(st, cert, httpapi.Options{Heartbeat: *hb, Timeout: *timeout, MaxDevices: *max, ConnectTimeout: *connectTimeout, MaxTunnels: *maxTunnels, MaxPerService: *maxPerService, MaxPerActor: *maxPerActor})
	if e != nil {
		return e
	}
	defer api.Close()
	listener, e := net.Listen("tcp", addr)
	if e != nil {
		return fmt.Errorf("listen failed (port occupied or unavailable): %w", e)
	}
	defer listener.Close()
	actualPort := listener.Addr().(*net.TCPAddr).Port
	cfg, e := client.Load(filepath.Join(*data, "admin.json"))
	if e != nil {
		return e
	}
	cfg.Server = "https://localhost:" + strconv.Itoa(actualPort)
	if e = secure.WriteJSON(filepath.Join(*data, "admin.json"), cfg); e != nil {
		return e
	}
	var proxyListener net.Listener
	if *proxyListen != "" {
		proxyListener, e = net.Listen("tcp", *proxyListen)
		if e != nil {
			return fmt.Errorf("proxy listen failed: %w", e)
		}
		defer proxyListener.Close()
		proxyPort := proxyListener.Addr().(*net.TCPAddr).Port
		if proxyPort == actualPort {
			return errors.New("proxy and control must use different ports")
		}
		api.ConfigureProxy(actualPort, proxyPort)
		info["proxyURL"] = "https://localhost:" + strconv.Itoa(proxyPort)
		info["proxyListen"] = proxyListener.Addr().String()
	}
	info["event"] = "server.started"
	info["listen"] = listener.Addr().String()
	info["localURL"] = cfg.Server
	info["port"] = actualPort
	// Recompute advertised URLs when port 0 or --port selected a different port.
	lan := []string{}
	for _, ip := range cert.IPAddresses {
		if !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			lan = append(lan, "https://"+net.JoinHostPort(ip.String(), strconv.Itoa(actualPort)))
		}
	}
	info["lanURLs"] = lan
	if e = a.print(info); e != nil {
		return e
	}
	srv := &http.Server{Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}, TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){}}
	done := make(chan error, 2)
	if proxyListener != nil {
		proxyServer := &http.Server{Handler: api.ProxyHandler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}, TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){}}
		defer proxyServer.Close()
		go func() {
			done <- proxyServer.ServeTLS(proxyListener, filepath.Join(*data, "server.crt"), filepath.Join(*data, "server.key"))
		}()
	}
	go func() {
		done <- srv.ServeTLS(listener, filepath.Join(*data, "server.crt"), filepath.Join(*data, "server.key"))
	}()
	select {
	case e = <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case <-ctx.Done():
		api.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e = srv.Shutdown(shutdown); e != nil {
			_ = srv.Close()
			return e
		}
		e = <-done
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}
}
