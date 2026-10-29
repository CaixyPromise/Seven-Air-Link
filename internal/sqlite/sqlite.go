// Package sqlite is a narrow, parameterized binding to the OS SQLite C engine.
// It is not a SQL implementation or an external sqlite3 subprocess. A connection
// is serialized, and Transaction holds the mutex across BEGIN/COMMIT/ROLLBACK.
package sqlite

/*
#cgo linux LDFLAGS: -l:libsqlite3.a -lm -ldl -lpthread
#cgo darwin LDFLAGS: -lsqlite3
#cgo windows LDFLAGS: -lsqlite3
#include <sqlite3.h>
#include <stdlib.h>
static int slb_text(sqlite3_stmt *s, int i, const char *p, int n) {
    return sqlite3_bind_text(s, i, p, n, SQLITE_TRANSIENT);
}
static int slb_blob(sqlite3_stmt *s, int i, const void *p, int n) {
    return sqlite3_bind_blob(s, i, p, n, SQLITE_TRANSIENT);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"
)

type Row []any
type DB struct {
	mu     sync.Mutex
	handle *C.sqlite3
	path   string
}
type Tx struct{ db *DB }

func Version() string       { return C.GoString(C.sqlite3_libversion()) }
func (db *DB) Path() string { return db.path }

// Open rejects symlinks and creates private files. readonly never creates a DB.
func Open(path string, readonly bool) (*DB, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && (!fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("database must be a regular, non-symlink file")
	}
	if err == nil && runtime.GOOS != "windows" && fi.Mode().Perm()&0077 != 0 {
		return nil, errors.New("database permissions must be 0600 (chmod before opening)")
	}
	if os.IsNotExist(err) {
		if readonly {
			return nil, err
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
	}
	flags := C.int(C.SQLITE_OPEN_READWRITE | C.SQLITE_OPEN_FULLMUTEX | C.SQLITE_OPEN_NOFOLLOW)
	if readonly {
		flags = C.SQLITE_OPEN_READONLY | C.SQLITE_OPEN_FULLMUTEX | C.SQLITE_OPEN_NOFOLLOW
	}
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	db := &DB{path: path}
	rc := C.sqlite3_open_v2(cs, &db.handle, flags, nil)
	if rc != C.SQLITE_OK {
		e := db.failure(rc)
		db.Close()
		return nil, e
	}
	C.sqlite3_extended_result_codes(db.handle, 1)
	C.sqlite3_busy_timeout(db.handle, 5000)
	// Extension loading remains disabled; callers never receive a raw SQL endpoint.
	C.sqlite3_enable_load_extension(db.handle, 0)
	if _, err = db.Exec("PRAGMA trusted_schema=OFF"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func (db *DB) failure(rc C.int) error {
	if db.handle == nil {
		return fmt.Errorf("sqlite error %d", int(rc))
	}
	// The engine does not print bound parameters, so tokens/bodies are not logged.
	return fmt.Errorf("sqlite error %d: %s", int(rc), C.GoString(C.sqlite3_errmsg(db.handle)))
}
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.handle == nil {
		return nil
	}
	rc := C.sqlite3_close_v2(db.handle)
	if rc != C.SQLITE_OK {
		return db.failure(rc)
	}
	db.handle = nil
	return nil
}
func (db *DB) prepare(query string, args []any) (*C.sqlite3_stmt, error) {
	if db.handle == nil {
		return nil, errors.New("sqlite connection is closed")
	}
	if strings.ContainsRune(query, 0) {
		return nil, errors.New("invalid SQL")
	}
	csql := C.CString(query)
	defer C.free(unsafe.Pointer(csql))
	var stmt *C.sqlite3_stmt
	var tail *C.char
	rc := C.sqlite3_prepare_v2(db.handle, csql, -1, &stmt, &tail)
	if rc != C.SQLITE_OK {
		return nil, db.failure(rc)
	}
	fail := func(e error) (*C.sqlite3_stmt, error) {
		if stmt != nil {
			C.sqlite3_finalize(stmt)
		}
		return nil, e
	}
	if stmt == nil || strings.TrimSpace(C.GoString(tail)) != "" {
		return fail(errors.New("exactly one SQL statement required"))
	}
	if int(C.sqlite3_bind_parameter_count(stmt)) != len(args) {
		return fail(errors.New("SQL parameter count mismatch"))
	}
	for i, arg := range args {
		pos := C.int(i + 1)
		switch v := arg.(type) {
		case nil:
			rc = C.sqlite3_bind_null(stmt, pos)
		case int:
			rc = C.sqlite3_bind_int64(stmt, pos, C.sqlite3_int64(v))
		case int64:
			rc = C.sqlite3_bind_int64(stmt, pos, C.sqlite3_int64(v))
		case bool:
			n := 0
			if v {
				n = 1
			}
			rc = C.sqlite3_bind_int64(stmt, pos, C.sqlite3_int64(n))
		case float64:
			rc = C.sqlite3_bind_double(stmt, pos, C.double(v))
		case string:
			p := C.CString(v)
			rc = C.slb_text(stmt, pos, p, C.int(len(v)))
			C.free(unsafe.Pointer(p))
		case []byte:
			if len(v) == 0 {
				rc = C.sqlite3_bind_zeroblob(stmt, pos, 0)
			} else {
				p := C.CBytes(v)
				rc = C.slb_blob(stmt, pos, p, C.int(len(v)))
				C.free(p)
			}
		default:
			return fail(fmt.Errorf("unsupported SQLite binding type %T", arg))
		}
		if rc != C.SQLITE_OK {
			return fail(db.failure(rc))
		}
	}
	return stmt, nil
}
func (db *DB) exec(query string, args ...any) (int64, error) {
	stmt, e := db.prepare(query, args)
	if e != nil {
		return 0, e
	}
	defer C.sqlite3_finalize(stmt)
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return int64(C.sqlite3_changes64(db.handle)), nil
		}
		if rc != C.SQLITE_ROW {
			return 0, db.failure(rc)
		}
	}
}
func (db *DB) query(query string, args ...any) ([]Row, error) {
	stmt, e := db.prepare(query, args)
	if e != nil {
		return nil, e
	}
	defer C.sqlite3_finalize(stmt)
	rows := []Row{}
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return rows, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, db.failure(rc)
		}
		row := make(Row, int(C.sqlite3_column_count(stmt)))
		for i := range row {
			n := C.int(i)
			switch C.sqlite3_column_type(stmt, n) {
			case C.SQLITE_INTEGER:
				row[i] = int64(C.sqlite3_column_int64(stmt, n))
			case C.SQLITE_FLOAT:
				row[i] = float64(C.sqlite3_column_double(stmt, n))
			case C.SQLITE_TEXT:
				row[i] = C.GoStringN((*C.char)(unsafe.Pointer(C.sqlite3_column_text(stmt, n))), C.sqlite3_column_bytes(stmt, n))
			case C.SQLITE_BLOB:
				row[i] = C.GoBytes(C.sqlite3_column_blob(stmt, n), C.sqlite3_column_bytes(stmt, n))
			default:
				row[i] = nil
			}
		}
		rows = append(rows, row)
	}
}
func (db *DB) Exec(query string, args ...any) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.exec(query, args...)
}
func (db *DB) Query(query string, args ...any) ([]Row, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.query(query, args...)
}
func (tx *Tx) Exec(query string, args ...any) (int64, error)  { return tx.db.exec(query, args...) }
func (tx *Tx) Query(query string, args ...any) ([]Row, error) { return tx.db.query(query, args...) }
func (db *DB) Transaction(fn func(*Tx) error) (err error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if _, err = db.exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = db.exec("ROLLBACK")
		}
	}()
	if err = fn(&Tx{db: db}); err != nil {
		return err
	}
	if _, err = db.exec("COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
