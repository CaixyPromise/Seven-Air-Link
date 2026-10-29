package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"seven-air-link/internal/model"
	"testing"
)

func TestInitIdempotentAndLegacyIsolation(t *testing.T) {
	d := t.TempDir()
	os.Chmod(d, 0700)
	info, e := initData(d, 7777, nil)
	if e != nil {
		t.Fatal(e)
	}
	old, _ := os.ReadFile(filepath.Join(d, "admin.json"))
	if _, e = initData(d, 7777, nil); e != nil {
		t.Fatal(e)
	}
	now, _ := os.ReadFile(filepath.Join(d, "admin.json"))
	if !bytes.Equal(old, now) {
		t.Fatal("init rotated credentials")
	}
	if info["fingerprint"] == "" {
		t.Fatal(info)
	}
	legacy := t.TempDir()
	os.WriteFile(filepath.Join(legacy, "state.json"), []byte(`{"messages":[]}`), 0600)
	if _, e = initData(legacy, 7777, nil); e == nil {
		t.Fatal("legacy directory overwritten")
	}
}
func TestCLIErrorsAreMachineReadable(t *testing.T) {
	var out, err bytes.Buffer
	n := Run(context.Background(), []string{"bot", "bind", "--output", "json"}, bytes.NewReader(nil), &out, &err)
	if n == 0 {
		t.Fatal("missing required Bot arguments succeeded")
	}
	var got struct {
		Error model.APIError `json:"error"`
	}
	if e := json.Unmarshal(out.Bytes(), &got); e != nil || got.Error.Code == "" {
		t.Fatal(out.String(), e)
	}
}
func TestUnknownFlagsKeepJSONOutputValid(t *testing.T) {
	for _, args := range [][]string{{"version", "--nonexistent", "--output", "json"}, {"serve", "--nonexistent", "--output", "json"}, {"join", "--nonexistent", "--output", "json"}} {
		var out, errout bytes.Buffer
		if Run(context.Background(), args, bytes.NewReader(nil), &out, &errout) == 0 {
			t.Fatal("unknown flag ignored")
		}
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("stdout is not JSON: %s (%v)", out.String(), err)
		}
		if result["error"] == nil {
			t.Fatal(result)
		}
	}
}
