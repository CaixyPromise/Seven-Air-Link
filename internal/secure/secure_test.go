package secure

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPinAndCertificateValidity(t *testing.T) {
	d := t.TempDir()
	c, e := MakeCertificate(filepath.Join(d, "cert"), filepath.Join(d, "key"), []string{"dev.local", "192.168.1.2"})
	if e != nil {
		t.Fatal(e)
	}
	tc, e := PinnedTLS(Fingerprint(c.Raw))
	if e != nil {
		t.Fatal(e)
	}
	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{c}}
	if e = tc.VerifyConnection(cs); e != nil {
		t.Fatal(e)
	}
	bad, _ := PinnedTLS(strings.Repeat("0", 64))
	if e = bad.VerifyConnection(cs); !errors.Is(e, ErrPin) {
		t.Fatal(e)
	}
	c.NotAfter = time.Now().Add(-time.Second)
	if e = tc.VerifyConnection(cs); !errors.Is(e, ErrCertificate) {
		t.Fatal(e)
	}
	if _, e = NormalizePin("123"); e == nil {
		t.Fatal("bad pin accepted")
	}
}
func TestAtomicWriteAndCertificatePermissions(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "cred")
	if e := AtomicWrite(p, []byte("one"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := AtomicWrite(p, []byte("two"), 0600); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "two" {
		t.Fatal(string(b))
	}
	if _, e := MakeCertificate(filepath.Join(d, "cert"), filepath.Join(d, "key"), nil); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadCertificate(filepath.Join(d, "cert")); e != nil {
		t.Fatal(e)
	}
}

func TestGeneratedCertificateUsesP256(t *testing.T) {
	dir := t.TempDir()
	cert, err := MakeCertificate(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("unexpected certificate algorithm: %s", cert.PublicKeyAlgorithm)
	}
	key, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve.Params().Name != "P-256" {
		t.Fatal("expected P-256 public key")
	}
}
