package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"seven-air-link/internal/filelock"
	"seven-air-link/internal/secure"
	"seven-air-link/internal/store"
	"time"
)

func (a *application) storage(ctx context.Context, args []string) error {
	_ = ctx
	if len(args) < 1 {
		return errors.New("usage: storage check|migrate|backup|verify|restore|gc|export-json")
	}
	f := a.flags("storage " + args[0])
	data := f.String("data", defaultData(), "offline Server directory")
	out := f.String("out", "", "new output path")
	from := f.String("from", "", "verified backup directory")
	hashes := f.Bool("hash-files", false, "verify each active attachment SHA-256")
	apply := f.Bool("apply", false, "actually remove GC candidates (default: preview)")
	age := f.Duration("min-age", 24*time.Hour, "minimum age of unreferenced files / upload partials")
	if e := parse(f, args[1:]); e != nil {
		return e
	}
	switch args[0] {
	case "verify":
		if *from == "" {
			return errors.New("--from required")
		}
		r, e := store.VerifyBackup(*from)
		if e != nil {
			return e
		}
		return a.print(r)
	case "restore":
		if *from == "" {
			return errors.New("--from required")
		}
		r, e := store.RestoreBackup(*from, *data)
		if e != nil {
			return e
		}
		return a.print(r)
	}
	if fi, e := os.Lstat(*data); e != nil || !fi.IsDir() {
		return errors.New("--data must be an existing non-symlink Server directory")
	}
	lock, e := filelock.Acquire(filepath.Join(*data, "server.lock"))
	if e != nil {
		return errors.New("Server is running or directory lock unavailable; stop it before storage maintenance")
	}
	defer lock.Close()
	switch args[0] {
	case "backup":
		if *out == "" {
			return errors.New("--out required")
		}
		r, e := store.Backup(*data, *out)
		if e != nil {
			return e
		}
		return a.print(r)
	case "migrate":
		st, r, e := store.MigrateJSON(filepath.Join(*data, "metadata.json"), filepath.Join(*data, "bridge.db"))
		if e != nil {
			return e
		}
		defer st.Close()
		return a.print(r)
	}
	st, e := store.Open(filepath.Join(*data, "bridge.db"))
	if e != nil {
		return e
	}
	defer st.Close()
	switch args[0] {
	case "check":
		r, e := st.Check(*hashes)
		if e != nil {
			return e
		}
		if !r.OK {
			return &storageCheckError{Report: r}
		}
		return a.print(r)
	case "gc":
		r, e := st.GC(*age, *apply)
		if e != nil {
			return e
		}
		if len(r.Failures) > 0 {
			return errors.New("some GC candidates failed; retry after checking filesystem permissions")
		}
		return a.print(r)
	case "export-json":
		if *out == "" {
			return errors.New("--out required")
		}
		if _, e = os.Lstat(*out); !os.IsNotExist(e) {
			return errors.New("output already exists")
		}
		b, e := st.ExportLegacy()
		if e != nil {
			return e
		}
		if e = secure.AtomicWrite(*out, b, 0600); e != nil {
			return e
		}
		return a.print(map[string]any{"exported": true, "path": *out, "schema": 3, "containsCredentialHashes": true, "note": "not a full backup; includes private message bodies"})
	default:
		return errors.New("unknown storage command")
	}
}

type storageCheckError struct{ Report store.StorageReport }

func (e *storageCheckError) Error() string {
	return "attachment integrity issues; restore missing/corrupt files from a verified backup"
}
