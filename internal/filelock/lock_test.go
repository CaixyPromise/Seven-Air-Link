package filelock

import (
	"path/filepath"
	"testing"
)

func TestLockExclusionAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	a, e := Acquire(path)
	if e != nil {
		t.Fatal(e)
	}
	if b, e := Acquire(path); e == nil {
		b.Close()
		t.Fatal("second lock acquired")
	}
	a.Close()
	b, e := Acquire(path)
	if e != nil {
		t.Fatal(e)
	}
	b.Close()
}
