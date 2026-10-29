// Package tunnel implements one bounded byte stream per data WebSocket.
// FIN ends only one direction. A WebSocket Close without FIN is an error.
package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"seven-air-link/internal/controlws"
	"seven-air-link/internal/model"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	Ready     byte = 0
	Data      byte = 1
	FIN       byte = 2
	Reset     byte = 3
	ChunkSize      = controlws.MaxMessage - 1
)

func SendReady(c *controlws.Conn) error { return c.WriteBinary([]byte{Ready}) }
func SendReset(c *controlws.Conn, code string) error {
	if len(code) > 64 || !utf8.ValidString(code) {
		code = "stream_reset"
	}
	return c.WriteBinary(append([]byte{Reset}, code...))
}
func AwaitReady(c *controlws.Conn, deadline time.Time) error {
	_ = c.SetReadDeadline(deadline)
	b, e := c.ReadBinary()
	_ = c.SetReadDeadline(time.Time{})
	if e != nil {
		return e
	}
	if len(b) == 1 && b[0] == Ready {
		return nil
	}
	if len(b) > 1 && b[0] == Reset && len(b) <= 65 && utf8.Valid(b[1:]) {
		return &model.APIError{Code: string(b[1:]), Message: "remote stream could not be established", Retryable: true}
	}
	return errors.New("invalid tunnel ready frame")
}

type Conn struct {
	ws                *controlws.Conn
	readMu            sync.Mutex
	writeMu           sync.Mutex
	pending           []byte
	readEOF, writeEOF bool
	once              sync.Once
	onClose           func()
}

func New(c *controlws.Conn, onClose func()) *Conn { return &Conn{ws: c, onClose: onClose} }
func (c *Conn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for len(c.pending) == 0 {
		if c.readEOF {
			return 0, io.EOF
		}
		b, e := c.ws.ReadBinary()
		if e != nil {
			return 0, e
		} // Transport close is NOT a graceful application FIN.
		if len(b) == 0 {
			return 0, errors.New("empty tunnel frame")
		}
		switch b[0] {
		case Data:
			if len(b) < 2 {
				return 0, errors.New("empty DATA frame")
			}
			c.pending = b[1:]
		case FIN:
			if len(b) != 1 {
				return 0, errors.New("invalid FIN frame")
			}
			c.readEOF = true
			return 0, io.EOF
		case Reset:
			if len(b) < 2 || len(b) > 65 || !utf8.Valid(b[1:]) {
				return 0, errors.New("invalid RESET frame")
			}
			return 0, &model.APIError{Code: string(b[1:]), Message: "remote stream reset"}
		default:
			return 0, errors.New("unknown tunnel frame")
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
func (c *Conn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeEOF {
		return 0, net.ErrClosed
	}
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > ChunkSize {
			n = ChunkSize
		}
		b := make([]byte, n+1)
		b[0] = Data
		copy(b[1:], p[:n])
		if e := c.ws.WriteBinary(b); e != nil {
			return total, e
		}
		total += n
		p = p[n:]
	}
	return total, nil
}
func (c *Conn) CloseWrite() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeEOF {
		return nil
	}
	c.writeEOF = true
	return c.ws.WriteBinary([]byte{FIN})
}
func (c *Conn) Close() error {
	var e error
	c.once.Do(func() {
		e = c.ws.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
	return e
}
func (c *Conn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *Conn) SetDeadline(t time.Time) error      { return c.ws.SetDeadline(t) }
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

type Result struct {
	AtoB, BtoA int64
	Err        error
}

// Bridge uses constant buffers and backpressure. It waits for BOTH graceful FINs,
// but closes both sockets immediately on an error or parent cancellation.
func Bridge(ctx context.Context, a, b net.Conn) Result {
	type copied struct {
		dir int
		n   int64
		e   error
	}
	finished := make(chan copied, 2)
	stop := context.AfterFunc(ctx, func() { a.Close(); b.Close() })
	defer stop()
	defer a.Close()
	defer b.Close()
	copyOne := func(dir int, dst, src net.Conn) {
		n, e := io.CopyBuffer(dst, src, make([]byte, ChunkSize))
		if e == nil {
			if w, ok := dst.(interface{ CloseWrite() error }); ok {
				e = w.CloseWrite()
			} else {
				e = errors.New("destination lacks half-close")
			}
		}
		finished <- copied{dir, n, e}
	}
	go copyOne(0, b, a)
	go copyOne(1, a, b)
	result := Result{}
	for i := 0; i < 2; i++ {
		v := <-finished
		if v.dir == 0 {
			result.AtoB = v.n
		} else {
			result.BtoA = v.n
		}
		if v.e != nil && result.Err == nil {
			result.Err = v.e
			a.Close()
			b.Close()
		}
	}
	return result
}
