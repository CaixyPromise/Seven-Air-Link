// Package terminal provides a small no-echo password prompt on release targets.
// Non-terminal input must be explicitly requested with --password-stdin.
package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

func ReadPassword(ctx context.Context, in *os.File, out io.Writer, prompt string) ([]byte, error) {
	restore, err := disableEcho(in.Fd())
	if err != nil {
		return nil, fmt.Errorf("no interactive terminal; use --password-stdin with a protected input stream: %w", err)
	}
	var once sync.Once
	reset := func() { once.Do(restore) }
	defer reset()
	fmt.Fprint(out, prompt)
	type result struct {
		b []byte
		e error
	}
	ch := make(chan result, 1)
	go func() {
		b := make([]byte, 0, 80)
		var one [1]byte
		for {
			n, e := in.Read(one[:])
			if n > 0 {
				if one[0] == '\n' {
					if len(b) > 0 && b[len(b)-1] == '\r' {
						b = b[:len(b)-1]
					}
					ch <- result{b, nil}
					return
				}
				b = append(b, one[0])
				if len(b) > 1024 {
					ch <- result{nil, errors.New("password too long")}
					return
				}
			}
			if e != nil {
				ch <- result{nil, e}
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		fmt.Fprintln(out)
		return nil, ctx.Err()
	case r := <-ch:
		fmt.Fprintln(out)
		return r.b, r.e
	}
}
