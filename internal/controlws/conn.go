// Package controlws implements a bounded RFC 6455 text-control channel.
// Private bounded transport: no compression or extensions. M2 data uses a separate
// slb.data.v1 handshake and binary messages, never the control connection.
// It accepts fragmented text, interleaved ping/pong and valid close frames.
package controlws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" // RFC 6455 handshake magic, NOT a password/credential hash.
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxMessage = 16 * 1024
const Subprotocol = "slb.control.v1"
const (
	Text   byte = 1
	Binary byte = 2
	Close  byte = 8
	Ping   byte = 9
	Pong   byte = 10
)

var ErrProtocol = errors.New("invalid WebSocket control frame")

type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string {
	return fmt.Sprintf("websocket closed (%d): %s", e.Code, e.Reason)
}

type Conn struct {
	net.Conn
	r             *bufio.Reader
	client        bool
	writeMu       sync.Mutex
	deadlineMu    sync.Mutex
	writeDeadline time.Time
	data          bool
}

func hasToken(v, t string) bool {
	for _, x := range strings.Split(v, ",") {
		if strings.EqualFold(strings.TrimSpace(x), t) {
			return true
		}
	}
	return false
}
func acceptKey(k string) string {
	h := sha1.Sum([]byte(k + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	return upgrade(w, r, Subprotocol)
}
func UpgradeData(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	return upgrade(w, r, "slb.data.v1")
}
func upgrade(w http.ResponseWriter, r *http.Request, protocol string) (*Conn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	decoded, e := base64.StdEncoding.DecodeString(key)
	if r.Method != "GET" || r.ProtoMajor != 1 || !hasToken(r.Header.Get("Connection"), "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Version") != "13" || e != nil || len(decoded) != 16 || !hasToken(r.Header.Get("Sec-WebSocket-Protocol"), protocol) {
		http.Error(w, "invalid WebSocket handshake; subprotocol slb.control.v1 required", http.StatusBadRequest)
		return nil, ErrProtocol
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "HTTP upgrade unavailable", 500)
		return nil, errors.New("hijacker unavailable")
	}
	raw, rw, e := hj.Hijack()
	if e != nil {
		return nil, e
	}
	if e = raw.SetWriteDeadline(time.Now().Add(5 * time.Second)); e != nil {
		raw.Close()
		return nil, e
	}
	_, e = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\nSec-WebSocket-Protocol: %s\r\n\r\n", acceptKey(key), protocol)
	if e == nil {
		e = rw.Flush()
	}
	if e != nil {
		raw.Close()
		return nil, e
	}
	_ = raw.SetDeadline(time.Time{})
	return &Conn{Conn: raw, r: rw.Reader, data: protocol == "slb.data.v1"}, nil
}

// Dial performs the TLS handshake and pin verification BEFORE writing the token.
func Dial(ctx context.Context, address, token string, conf *tls.Config) (*Conn, int, error) {
	return dial(ctx, address, token, conf, "/api/v1/node/control", Subprotocol, nil)
}
func DialData(ctx context.Context, address, token string, conf *tls.Config, id, ticket string) (*Conn, int, error) {
	return dial(ctx, address, token, conf, "/api/v1/tunnel/data", "slb.data.v1", map[string]string{"X-SLB-Tunnel-ID": id, "X-SLB-Tunnel-Ticket": ticket})
}
func dial(ctx context.Context, address, token string, conf *tls.Config, path, protocol string, headers map[string]string) (*Conn, int, error) {
	u, e := url.Parse(address)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, 0, errors.New("expected an https server URL without credentials/query")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	tc := conf.Clone()
	tc.NextProtos = []string{"http/1.1"}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: tc}
	raw, e := d.DialContext(ctx, "tcp", host)
	if e != nil {
		return nil, 0, e
	}
	ok := false
	defer func() {
		if !ok {
			raw.Close()
		}
	}()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	keyb := make([]byte, 16)
	if _, e = rand.Read(keyb); e != nil {
		return nil, 0, e
	}
	key := base64.StdEncoding.EncodeToString(keyb)
	u.Path = path
	u.RawPath = ""
	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Protocol", protocol)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Bridge-Protocol", "1")
	if e = req.Write(raw); e != nil {
		return nil, 0, e
	}
	br := bufio.NewReader(raw)
	resp, e := http.ReadResponse(br, req)
	if e != nil {
		return nil, 0, e
	}
	if resp.StatusCode != 101 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, resp.StatusCode, fmt.Errorf("control handshake rejected (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") || !hasToken(resp.Header.Get("Connection"), "upgrade") || resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) || resp.Header.Get("Sec-WebSocket-Protocol") != protocol || resp.Header.Get("Sec-WebSocket-Extensions") != "" {
		return nil, 0, ErrProtocol
	}
	_ = raw.SetDeadline(time.Time{})
	ok = true
	return &Conn{Conn: raw, r: br, client: true, data: protocol == "slb.data.v1"}, 101, nil
}
func (c *Conn) WriteJSON(v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return c.writeFrame(Text, b)
}
func (c *Conn) WriteText(b []byte) error   { return c.writeFrame(Text, b) }
func (c *Conn) WriteBinary(b []byte) error { return c.writeFrame(Binary, b) }
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func (c *Conn) writeFrame(op byte, b []byte) error {
	if len(b) > MaxMessage || (op >= 8 && len(b) > 125) {
		return errors.New("control message too large")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.deadlineMu.Lock()
	stall := 5 * time.Second
	if c.data {
		stall = 30 * time.Second
	}
	deadline := time.Now().Add(stall)
	if !c.writeDeadline.IsZero() && c.writeDeadline.Before(deadline) {
		deadline = c.writeDeadline
	}
	e := c.Conn.SetWriteDeadline(deadline)
	c.deadlineMu.Unlock()
	if e != nil {
		return e
	}
	h := make([]byte, 0, 14)
	h = append(h, 0x80|op)
	mask := byte(0)
	if c.client {
		mask = 0x80
	}
	if len(b) < 126 {
		h = append(h, mask|byte(len(b)))
	} else {
		h = append(h, mask|126, byte(len(b)>>8), byte(len(b)))
	}
	if c.client {
		var m [4]byte
		if _, e := rand.Read(m[:]); e != nil {
			return e
		}
		h = append(h, m[:]...)
		payload := append([]byte(nil), b...)
		for i := range payload {
			payload[i] ^= m[i%4]
		}
		b = payload
	}
	if e := writeAll(c.Conn, h); e != nil {
		return e
	}
	return writeAll(c.Conn, b)
}
func validClose(code uint16) bool {
	return code >= 3000 && code <= 4999 || code >= 1000 && code <= 1014 && code != 1004 && code != 1005 && code != 1006
}
func (c *Conn) Shutdown(code int, reason string) {
	if !utf8.ValidString(reason) || len(reason) > 100 {
		reason = "closing"
	}
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, uint16(code))
	_ = c.writeFrame(Close, append(b, []byte(reason)...))
	_ = c.Conn.Close()
}
func (c *Conn) ReadJSON(v any) error {
	b, e := c.ReadText()
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func (c *Conn) ReadText() ([]byte, error)   { return c.readMessage(Text) }
func (c *Conn) ReadBinary() ([]byte, error) { return c.readMessage(Binary) }
func (c *Conn) readMessage(kind byte) ([]byte, error) {
	out := []byte{}
	fragmented := false
	for {
		var h [2]byte
		if _, e := io.ReadFull(c.r, h[:]); e != nil {
			return nil, e
		}
		fin := h[0]&0x80 != 0
		op := h[0] & 15
		masked := h[1]&0x80 != 0
		if h[0]&0x70 != 0 || masked == c.client {
			return nil, ErrProtocol
		}
		n := uint64(h[1] & 127)
		switch n {
		case 126:
			var b [2]byte
			if _, e := io.ReadFull(c.r, b[:]); e != nil {
				return nil, e
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
			if n < 126 {
				return nil, ErrProtocol
			}
		case 127:
			var b [8]byte
			if _, e := io.ReadFull(c.r, b[:]); e != nil {
				return nil, e
			}
			n = binary.BigEndian.Uint64(b[:])
			if n < 65536 || n>>63 != 0 {
				return nil, ErrProtocol
			}
		}
		if op >= 8 {
			if !fin || n > 125 || op > 10 {
				return nil, ErrProtocol
			}
		} else {
			if op != 0 && op != kind {
				return nil, ErrProtocol
			}
			if op == 0 && !fragmented || op == kind && fragmented {
				return nil, ErrProtocol
			}
			if n > uint64(MaxMessage-len(out)) {
				return nil, errors.New("control message exceeds 16 KiB")
			}
		}
		var mask [4]byte
		if masked {
			if _, e := io.ReadFull(c.r, mask[:]); e != nil {
				return nil, e
			}
		}
		b := make([]byte, int(n))
		if _, e := io.ReadFull(c.r, b); e != nil {
			return nil, e
		}
		if masked {
			for i := range b {
				b[i] ^= mask[i%4]
			}
		}
		switch op {
		case Ping:
			if e := c.writeFrame(Pong, b); e != nil {
				return nil, e
			}
			continue
		case Pong:
			continue
		case Close:
			code := 1000
			reason := ""
			if len(b) == 1 {
				return nil, ErrProtocol
			}
			if len(b) >= 2 {
				code = int(binary.BigEndian.Uint16(b[:2]))
				reason = string(b[2:])
				if !validClose(uint16(code)) || !utf8.ValidString(reason) {
					return nil, ErrProtocol
				}
			}
			_ = c.writeFrame(Close, b)
			return nil, &CloseError{code, reason}
		default:
			out = append(out, b...)
			fragmented = !fin
			if fin {
				if kind == Text && !utf8.Valid(out) {
					return nil, ErrProtocol
				}
				return out, nil
			}
		}
	}
}

// Explicit deadlines are respected while a bounded write-stall watchdog remains.
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	c.writeDeadline = t
	return c.Conn.SetWriteDeadline(t)
}
func (c *Conn) SetDeadline(t time.Time) error {
	if e := c.SetWriteDeadline(t); e != nil {
		return e
	}
	return c.SetReadDeadline(t)
}
