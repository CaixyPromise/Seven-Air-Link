package sqlite

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestBindingTransactionAndRollback(t *testing.T) {
	d, e := Open(filepath.Join(t.TempDir(), "test.db"), false)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	if _, e = d.Exec("CREATE TABLE test (k INTEGER PRIMARY KEY, s TEXT UNIQUE, b BLOB)"); e != nil {
		t.Fatal(e)
	}
	text := "中文'; DROP TABLE test; --\x00尾"
	blob := []byte{0, 1, 255}
	e = d.Transaction(func(tx *Tx) error { _, e := tx.Exec("INSERT INTO test VALUES(?,?,?)", 1, text, blob); return e })
	if e != nil {
		t.Fatal(e)
	}
	rows, e := d.Query("SELECT k,s,b FROM test")
	if e != nil || len(rows) != 1 || rows[0][1] != text || string(rows[0][2].([]byte)) != string(blob) {
		t.Fatal(rows, e)
	}
	e = d.Transaction(func(tx *Tx) error {
		_, e := tx.Exec("INSERT INTO test VALUES(?,?,?)", 2, "rollback", nil)
		if e != nil {
			return e
		}
		return errors.New("abort")
	})
	if e == nil {
		t.Fatal("expected rollback")
	}
	rows, e = d.Query("SELECT count(*) FROM test")
	if e != nil || rows[0][0] != int64(1) {
		t.Fatal(rows, e)
	}
	if _, e = d.Exec("SELECT 1; SELECT 2"); e == nil {
		t.Fatal("multiple statements accepted")
	}
	if _, e = d.Exec("SELECT ?"); e == nil {
		t.Fatal("missing parameter")
	}
}
func TestSQLiteConcurrentAndReadOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db")
	d, e := Open(p, false)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	d.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, s TEXT)")
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, e := d.Exec("INSERT INTO t VALUES(?,?)", i, fmt.Sprint(i)); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	r, e := d.Query("SELECT count(*) FROM t")
	if e != nil || r[0][0] != int64(32) {
		t.Fatal(r, e)
	}
	rd, e := Open(p, true)
	if e != nil {
		t.Fatal(e)
	}
	defer rd.Close()
	if _, e = rd.Exec("DELETE FROM t"); e == nil {
		t.Fatal("readonly write")
	}
	link := p + "link"
	if e = os.Symlink(p, link); e == nil {
		if _, e = Open(link, false); e == nil {
			t.Fatal("symlink accepted")
		}
	}
}

// SQLite extension loading must remain disabled on every supported OS.
func TestExtensionLoadingDisabledByDefault(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "no-extensions.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Query("SELECT load_extension(?)", "untrusted-extension.so"); err == nil {
		t.Fatal("SQLite extension loading unexpectedly enabled")
	}
}

func TestSQLiteMacOSParentSymlink(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS path semantics")
	}
	root := t.TempDir()
	actual := filepath.Join(root, "real")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	db, err := Open(filepath.Join(alias, "bridge.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(actual, "bridge.db")); err != nil {
		t.Fatal(err)
	}
}
