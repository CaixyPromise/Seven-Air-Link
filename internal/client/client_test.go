package client

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"seven-air-link/internal/model"
	"seven-air-link/internal/secure"
	"strings"
	"testing"
)

func TestValidateServerOriginOnly(t *testing.T) {
	for _, valid := range []string{"https://localhost:7777", "https://127.0.0.1:7777/", "https://[::1]:7777"} {
		if e := ValidateServer(valid); e != nil {
			t.Fatal(e)
		}
	}
	for _, invalid := range []string{"http://localhost:7777", "https://u:p@localhost", "https://localhost/path", "https://localhost?q=x", "https://localhost#x", "https:///"} {
		if ValidateServer(invalid) == nil {
			t.Fatal("accepted", invalid)
		}
	}
}
func TestLoadConfigAndPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node.json")
	cfg := model.Config{Version: 1, Server: "https://localhost:7777", Fingerprint: secure.Hash("cert"), Token: secure.Random("dev_"), SubjectID: "d", Role: "device"}
	if e := secure.WriteJSON(p, cfg); e != nil {
		t.Fatal(e)
	}
	got, e := Load(p)
	if e != nil || got != cfg {
		t.Fatal(got, e)
	}
	if runtime.GOOS != "windows" {
		if e = os.Chmod(p, 0644); e != nil {
			t.Fatal(e)
		}
		if _, e = Load(p); e == nil {
			t.Fatal("world-readable credential accepted")
		}
		os.Chmod(p, 0600)
	}
	cfg.Version = 99
	secure.WriteJSON(p, cfg)
	if _, e = Load(p); e == nil {
		t.Fatal("unknown version accepted")
	}
}
func TestDecodeErrorDoesNotLeakResponseBody(t *testing.T) {
	r := &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("private response secret"))}
	e := DecodeError(r)
	if strings.Contains(e.Error(), "secret") {
		t.Fatal(e)
	}
	r = &http.Response{StatusCode: 403, Body: io.NopCloser(bytes.NewBufferString(`{"error":{"code":"access_denied","message":"Denied","retryable":false}}`))}
	if e = DecodeError(r); !strings.Contains(e.Error(), "access_denied") {
		t.Fatal(e)
	}
}

func TestCredentialProfileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "profile.json")
	cfg := model.Config{Version: 1, Server: "https://localhost:7777", Fingerprint: secure.Hash("cert"), Token: secure.Random("dev_"), SubjectID: "d", Role: "device"}
	if e := secure.WriteJSON(p, cfg); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "profile-link.json")
	if e := os.Symlink(p, link); e != nil {
		t.Skip(e)
	}
	if _, e := Load(link); e == nil {
		t.Fatal("followed symlink credential profile")
	}
}
