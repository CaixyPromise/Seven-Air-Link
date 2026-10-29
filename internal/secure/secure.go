// Package secure contains credential, TLS pinning and atomic-file primitives.
package secure

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var ErrPin = errors.New("server certificate fingerprint mismatch; verify it out of band before rejoining")
var ErrCertificate = errors.New("pinned certificate is not currently valid for server authentication")

func Random(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("operating system random source failed")
	}
	return prefix + hex.EncodeToString(b)
}
func Hash(s string) string          { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func Fingerprint(der []byte) string { h := sha256.Sum256(der); return hex.EncodeToString(h[:]) }
func Equal(a, b string) bool        { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func NormalizePin(s string) (string, error) {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), ":", ""))
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return "", errors.New("fingerprint must be a SHA-256 certificate fingerprint (64 hex digits)")
	}
	return s, nil
}

// AtomicWrite never truncates the old file before the new data is synced.
// Directory sync is best-effort after rename; a successful rename is the commit.
func AtomicWrite(path string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".slb-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if d, e := os.Open(dir); e == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(b, '\n'), 0600)
}

// MakeCertificate includes discovered addresses plus explicitly configured SANs.
// Clients trust the exact certificate fingerprint, never an unverified TLS peer.
func MakeCertificate(certPath, keyPath string, extra []string) (*x509.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	names := map[string]bool{"localhost": true}
	ips := map[string]bool{"127.0.0.1": true, "::1": true}
	if host, e := os.Hostname(); e == nil && host != "" {
		names[host] = true
	}
	if as, e := net.InterfaceAddrs(); e == nil {
		for _, a := range as {
			ip, _, e := net.ParseCIDR(a.String())
			if e == nil && !ip.IsUnspecified() {
				ips[ip.String()] = true
			}
		}
	}
	for _, s := range extra {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			ips[ip.String()] = true
		} else {
			if strings.ContainsAny(s, "/:* ") {
				return nil, fmt.Errorf("invalid TLS hostname: %q", s)
			}
			names[strings.ToLower(s)] = true
		}
	}
	t := &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: "Seven-Air-Link"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	for n := range names {
		t.DNSNames = append(t.DNSNames, n)
	}
	for ip := range ips {
		t.IPAddresses = append(t.IPAddresses, net.ParseIP(ip))
	}
	der, err := x509.CreateCertificate(rand.Reader, t, t, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	pk, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err = AtomicWrite(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0600); err != nil {
		return nil, err
	}
	if err = AtomicWrite(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}
func ReadCertificate(path string) (*x509.Certificate, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, errors.New("invalid certificate PEM")
	}
	return x509.ParseCertificate(p.Bytes)
}

// PinnedTLS deliberately substitutes leaf-certificate pinning for CA/DNS trust.
// VerifyConnection runs even on resumed sessions; expiry and EKU remain checked.
func PinnedTLS(pin string) (*tls.Config, error) {
	pin, err := NormalizePin(pin)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, // Required for self-signed certificates; verification below is mandatory.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("TLS peer supplied no certificate")
			}
			cert := cs.PeerCertificates[0]
			if !Equal(pin, Fingerprint(cert.Raw)) {
				return ErrPin
			}
			now := time.Now()
			if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return ErrCertificate
			}
			ok := false
			for _, u := range cert.ExtKeyUsage {
				if u == x509.ExtKeyUsageServerAuth || u == x509.ExtKeyUsageAny {
					ok = true
				}
			}
			if !ok {
				return ErrCertificate
			}
			return nil
		}}, nil
}

// WriteNewJSON creates a private profile without replacing an existing path,
// including a symlink. A same-directory hard link publishes a fully synced file.
// On filesystems without links, O_EXCL still guarantees no overwrite.
func WriteNewJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".new-profile-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(b); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Link(tmp.Name(), path); err != nil {
		if os.IsExist(err) {
			return err
		}
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(b)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			_ = os.Remove(path)
			return e
		}
	}
	if d, e := os.Open(dir); e == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// PrivateDirectory fails closed rather than chmod-ing an unexpected shared path.
func PrivateDirectory(path string) error {
	fi, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !fi.IsDir() {
		return errors.New("Server data directory must be a non-symlink directory")
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0077 != 0 {
		return errors.New("Server data directory must be private; run chmod 700 on it")
	}
	return nil
}
