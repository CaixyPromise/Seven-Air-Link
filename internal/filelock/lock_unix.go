//go:build linux || darwin

// Package filelock prevents two processes from writing the same state/profile.
package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type Lock struct{ f *os.File }

func Acquire(path string) (*Lock, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("lock must be a regular file")
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("resource already in use (lock %s): %w", path, e)
	}
	return &Lock{f}, nil
}
func (l *Lock) Close() error { _ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN); return l.f.Close() }
