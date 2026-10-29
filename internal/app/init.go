package app

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"seven-air-link/internal/client"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"sort"
	"strconv"
)

func baseDir() string {
	d, e := os.UserConfigDir()
	if e != nil {
		return ".seven-air-link"
	}
	return filepath.Join(d, "seven-air-link")
}
func defaultData() string   { return filepath.Join(baseDir(), "server") }
func defaultConfig() string { return filepath.Join(baseDir(), "node.json") }
func initData(data string, port int, hosts []string) (map[string]any, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("port must be 1..65535")
	}
	if e := os.MkdirAll(data, 0700); e != nil {
		return nil, e
	}
	if e := secure.PrivateDirectory(data); e != nil {
		return nil, e
	}
	if _, e := os.Stat(filepath.Join(data, "state.json")); e == nil {
		return nil, errors.New("this looks like a Python shared_data directory; select a DIFFERENT Go data directory")
	}
	meta := filepath.Join(data, "bridge.db")
	legacy := filepath.Join(data, "metadata.json")
	if _, e := os.Stat(meta); os.IsNotExist(e) {
		if _, e = os.Stat(legacy); e == nil {
			migrated, _, err := store.MigrateJSON(legacy, meta)
			if err != nil {
				return nil, err
			}
			if err = migrated.Close(); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	configPath := filepath.Join(data, "admin.json")
	certPath := filepath.Join(data, "server.crt")
	keyPath := filepath.Join(data, "server.key")
	if _, e := os.Stat(meta); os.IsNotExist(e) {
		// Do not overwrite partially initialized authority. Manual recovery is explicit.
		for _, p := range []string{configPath, certPath, keyPath} {
			if _, e := os.Stat(p); e == nil {
				return nil, fmt.Errorf("partial initialization at %s; inspect/backup it and choose a fresh directory", data)
			}
		}
		cert, e := secure.MakeCertificate(certPath, keyPath, hosts)
		if e != nil {
			return nil, e
		}
		token := secure.Random("owner_")
		owner := secure.Random("human_")[:28]
		cfg := model.Config{Version: 1, Server: "https://localhost:" + strconv.Itoa(port), Fingerprint: secure.Fingerprint(cert.Raw), Token: token, SubjectID: owner, Role: "owner"}
		if e = secure.WriteJSON(configPath, cfg); e != nil {
			return nil, e
		}
		created, err := store.New(meta, owner, token)
		if err != nil {
			return nil, err
		}
		if err = created.Close(); err != nil {
			return nil, err
		}
	} else if e != nil {
		return nil, e
	}
	cfg, e := client.Load(configPath)
	if e != nil {
		return nil, e
	}
	st, e := store.Open(meta)
	if e != nil {
		return nil, e
	}
	defer st.Close()
	sub, e := st.Auth(cfg.Token)
	if e != nil || sub.Role != "owner" {
		return nil, errors.New("admin profile does not match metadata; refusing to reset authority")
	}
	cert, e := secure.ReadCertificate(certPath)
	if e != nil {
		return nil, e
	}
	if cfg.Fingerprint != secure.Fingerprint(cert.Raw) {
		return nil, errors.New("certificate does not match pinned admin profile")
	}
	abs, _ := filepath.Abs(data)
	urls := []string{}
	for _, ip := range cert.IPAddresses {
		if !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			urls = append(urls, "https://"+net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		}
	}
	sort.Strings(urls)
	return map[string]any{"initialized": true, "dataDir": abs, "adminConfig": filepath.Join(abs, "admin.json"), "fingerprint": cfg.Fingerprint, "localURL": "https://localhost:" + strconv.Itoa(port), "lanURLs": urls, "version": model.Version}, nil
}
