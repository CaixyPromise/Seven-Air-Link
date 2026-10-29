//go:build windows

package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var kernel = syscall.NewLazyDLL("kernel32.dll")
var lockFile = kernel.NewProc("LockFileEx")
var unlockFile = kernel.NewProc("UnlockFileEx")

type Lock struct {
	f  *os.File
	ov syscall.Overlapped
}

func Acquire(path string) (*Lock, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	l := &Lock{f: f}
	r, _, e := lockFile.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&l.ov)))
	if r == 0 {
		f.Close()
		return nil, fmt.Errorf("resource already in use (lock %s): %w", path, e)
	}
	return l, nil
}
func (l *Lock) Close() error {
	unlockFile.Call(l.f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&l.ov)))
	return l.f.Close()
}
