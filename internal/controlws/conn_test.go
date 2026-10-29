package controlws

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type fakeConn struct{ written bytes.Buffer }

func (c *fakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *fakeConn) Write(p []byte) (int, error)      { return c.written.Write(p) }
func (c *fakeConn) Close() error                     { return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return nil }
func (c *fakeConn) RemoteAddr() net.Addr             { return nil }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }
func frame(fin bool, op byte, p []byte, masked bool) []byte {
	h := op
	if fin {
		h |= 128
	}
	m := byte(0)
	if masked {
		m = 128
	}
	b := []byte{h}
	if len(p) < 126 {
		b = append(b, m|byte(len(p)))
	} else {
		b = append(b, m|126, byte(len(p)>>8), byte(len(p)))
	}
	if masked {
		b = append(b, 1, 2, 3, 4)
		for i, x := range p {
			b = append(b, x^byte(i%4+1))
		}
	} else {
		b = append(b, p...)
	}
	return b
}
func readBytes(b []byte) ([]byte, error) {
	c := &Conn{Conn: &fakeConn{}, r: bufio.NewReader(bytes.NewReader(b))}
	return c.ReadText()
}
func TestReadMaskedTextAndFragmentedUTF8WithPing(t *testing.T) {
	for _, s := range []string{"hello", "中文", strings.Repeat("a", 16384)} {
		b, e := readBytes(frame(true, Text, []byte(s), true))
		if e != nil || string(b) != s {
			t.Fatal(e)
		}
	}
	body := []byte("你好")
	wire := frame(false, Text, body[:1], true)
	wire = append(wire, frame(true, Ping, []byte("hi"), true)...)
	wire = append(wire, frame(true, 0, body[1:], true)...)
	b, e := readBytes(wire)
	if e != nil || string(b) != "你好" {
		t.Fatal(string(b), e)
	}
}
func TestRejectMalformedFrames(t *testing.T) {
	cases := map[string][]byte{
		"unmasked client": frame(true, Text, []byte("x"), false), "reserved bit": {0xc1, 0x80, 1, 2, 3, 4}, "orphan continuation": frame(true, 0, []byte("x"), true), "binary unsupported": frame(true, 2, []byte("x"), true), "fragmented ping": frame(false, Ping, nil, true), "one-byte close": frame(true, Close, []byte{1}, true), "reserved close": frame(true, Close, []byte{3, 237}, true), "invalid UTF8": frame(true, Text, []byte{255}, true), "oversize": frame(true, Text, bytes.Repeat([]byte{'a'}, 16385), true), "nested text": append(frame(false, Text, []byte("a"), true), frame(true, Text, []byte("b"), true)...), "noncanonical length": {0x81, 0xfe, 0, 1}, "high length bit": {0x81, 0xff, 128, 0, 0, 0, 0, 0, 0, 0}, "unknown opcode": frame(true, 15, nil, true)}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, e := readBytes(b); e == nil {
				t.Fatal("accepted malformed frame")
			}
		})
	}
}
func TestClientMaskingAndServerFraming(t *testing.T) {
	f := &fakeConn{}
	c := &Conn{Conn: f, client: true}
	if e := c.WriteText([]byte("secret")); e != nil {
		t.Fatal(e)
	}
	wire := f.written.Bytes()
	if wire[1]&128 == 0 {
		t.Fatal("client failed to mask")
	}
	b, e := readBytes(wire)
	if e != nil || string(b) != "secret" {
		t.Fatal(e, string(b))
	}
	f = &fakeConn{}
	c = &Conn{Conn: f}
	if e = c.WriteText([]byte("hello")); e != nil {
		t.Fatal(e)
	}
	c = &Conn{Conn: &fakeConn{}, client: true, r: bufio.NewReader(bytes.NewReader(f.written.Bytes()))}
	b, e = c.ReadText()
	if e != nil || string(b) != "hello" {
		t.Fatal(e)
	}
}
func TestValidClose(t *testing.T) {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, 1008)
	b = append(b, []byte("device_revoked")...)
	_, e := readBytes(frame(true, Close, b, true))
	var ce *CloseError
	if !errors.As(e, &ce) || ce.Code != 1008 {
		t.Fatal(e)
	}
}
func TestHandshakeMagic(t *testing.T) {
	if acceptKey("dGhlIHNhbXBsZSBub25jZQ==") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatal("RFC6455 accept mismatch")
	}
}
func FuzzReadText(f *testing.F) {
	f.Add(frame(true, Text, []byte("hello"), true))
	f.Add([]byte{0x81, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 65536 {
			t.Skip()
		}
		_, _ = readBytes(b)
	})
}

func TestBinaryFragmentationAndTypeIsolation(t *testing.T) {
	body := []byte{0, 255, 128, 1, 2, 3}
	wire := frame(false, Binary, body[:2], true)
	wire = append(wire, frame(true, Ping, []byte("probe"), true)...)
	wire = append(wire, frame(true, 0, body[2:], true)...)
	c := &Conn{Conn: &fakeConn{}, r: bufio.NewReader(bytes.NewReader(wire)), data: true}
	got, e := c.ReadBinary()
	if e != nil || !bytes.Equal(got, body) {
		t.Fatal(got, e)
	}
	c = &Conn{Conn: &fakeConn{}, r: bufio.NewReader(bytes.NewReader(frame(true, Text, []byte("payload"), true))), data: true}
	if _, e = c.ReadBinary(); e == nil {
		t.Fatal("data channel accepted text")
	}
	c = &Conn{Conn: &fakeConn{}, r: bufio.NewReader(bytes.NewReader(frame(true, Binary, bytes.Repeat([]byte{1}, MaxMessage+1), true))), data: true}
	if _, e = c.ReadBinary(); e == nil {
		t.Fatal("unbounded data allocation")
	}
}
func FuzzReadBinary(f *testing.F) {
	f.Add(frame(true, Binary, []byte{1, 0, 255}, true))
	f.Add([]byte{0x82, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 65536 {
			t.Skip()
		}
		c := &Conn{Conn: &fakeConn{}, r: bufio.NewReader(bytes.NewReader(b)), data: true}
		_, _ = c.ReadBinary()
	})
}
