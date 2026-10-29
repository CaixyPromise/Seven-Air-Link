package secure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNewCredentialProfileNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if WriteNewJSON(path, map[string]int{"id": i}) == nil {
				won.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal("concurrent writes succeeded", won.Load())
	}
	before, _ := os.ReadFile(path)
	var data map[string]int
	if e := json.Unmarshal(before, &data); e != nil {
		t.Fatal(e)
	}
	if e := WriteNewJSON(path, map[string]string{"replacement": "forbidden"}); e == nil {
		t.Fatal("overwrote profile")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("existing bytes changed")
	}
	target := filepath.Join(t.TempDir(), "original")
	os.WriteFile(target, []byte("secret"), 0600)
	link := filepath.Join(t.TempDir(), "link")
	if os.Symlink(target, link) == nil {
		if WriteNewJSON(link, data) == nil {
			t.Fatal("replaced symlink")
		}
		b, _ := os.ReadFile(target)
		if string(b) != "secret" {
			t.Fatal("followed symlink")
		}
	}
}

func TestPrivateDirectoryRejectsSymlinkAndPublicMode(t *testing.T) {
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := PrivateDirectory(dir); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(t.TempDir(), "data-link")
	if e := os.Symlink(dir, link); e == nil {
		if PrivateDirectory(link) == nil {
			t.Fatal("followed data directory symlink")
		}
	}
	if runtime.GOOS != "windows" {
		os.Chmod(dir, 0755)
		if PrivateDirectory(dir) == nil {
			t.Fatal("accepted other-user-readable data directory")
		}
	}
}
