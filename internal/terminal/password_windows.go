//go:build windows

package terminal

import (
	"syscall"
	"unsafe"
)

var consoleDLL = syscall.NewLazyDLL("kernel32.dll")
var getMode = consoleDLL.NewProc("GetConsoleMode")
var setMode = consoleDLL.NewProc("SetConsoleMode")

func disableEcho(fd uintptr) (func(), error) {
	var mode uint32
	if ok, _, e := getMode.Call(fd, uintptr(unsafe.Pointer(&mode))); ok == 0 {
		return nil, e
	}
	if ok, _, e := setMode.Call(fd, uintptr(mode&^4)); ok == 0 {
		return nil, e
	}
	return func() { setMode.Call(fd, uintptr(mode)) }, nil
}
