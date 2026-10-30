//go:build !linux && !darwin && !windows

package terminal

import "errors"

func disableEcho(fd uintptr) (func(), error) {
	return nil, errors.New("no-echo prompt not supported on this platform")
}
