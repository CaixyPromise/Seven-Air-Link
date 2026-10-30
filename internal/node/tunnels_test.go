package node

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"syscall"
	"testing"
)

func TestTargetDialErrorUsesNativeWrappedCodes(t *testing.T) {
	refused := syscall.ECONNREFUSED
	if runtime.GOOS == "windows" {
		refused = syscall.Errno(10061) // WSAECONNREFUSED, Winsock2.h
	}
	wrapped := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: refused}}
	if got := targetDialError(wrapped); got != "target_refused" {
		t.Fatalf("wrapped refusal classified as %s: %v", got, wrapped)
	}
	if got := targetDialError(context.DeadlineExceeded); got != "timeout" {
		t.Fatalf("deadline classified as %s", got)
	}
	if got := targetDialError(errors.New("connection refused is just arbitrary text")); got != "target_unreachable" {
		t.Fatalf("classified by error text: %s", got)
	}
}
