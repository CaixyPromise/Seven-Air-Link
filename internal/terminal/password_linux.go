//go:build linux

package terminal

import (
	"syscall"
	"unsafe"
)

func disableEcho(fd uintptr) (func(), error) {
	var old syscall.Termios
	if _, _, e := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&old)), 0, 0, 0); e != 0 {
		return nil, e
	}
	next := old
	next.Lflag &^= syscall.ECHO
	if _, _, e := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&next)), 0, 0, 0); e != 0 {
		return nil, e
	}
	return func() {
		syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&old)), 0, 0, 0)
	}, nil
}
