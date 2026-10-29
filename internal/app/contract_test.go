package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"seven-air-link/internal/model"
)

func TestCommittedAPIContract(t *testing.T) {
	b, e := os.ReadFile("../../api/openapi.json")
	if e != nil {
		t.Fatal(e)
	}
	var spec struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]map[string]any `json:"paths"`
	}
	if e = json.Unmarshal(b, &spec); e != nil {
		t.Fatal(e)
	}
	if spec.Info.Version != model.Version {
		t.Fatalf("contract version %s != binary %s", spec.Info.Version, model.Version)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Fatal(spec.OpenAPI)
	}
	for _, p := range []string{"/api/v1/join", "/api/v1/devices", "/api/v1/node/control", "/api/v1/events", "/api/v1/pairings", "/api/v1/status", "/api/v1/peers", "/api/v1/principals", "/api/v1/identity/claim", "/api/v1/principals/{id}/rotate", "/api/v1/messages", "/api/v1/messages/{id}", "/api/v1/messages/stream", "/api/v1/files", "/api/v1/files/{id}", "/api/v1/files/{id}/meta"} {
		if spec.Paths[p] == nil {
			t.Fatalf("missing contract: %s", p)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if path, ok := x["$ref"].(string); ok {
				if !strings.HasPrefix(path, "#/") {
					t.Fatalf("unexpected remote schema %s", path)
				}
				var current any = doc
				for _, key := range strings.Split(strings.TrimPrefix(path, "#/"), "/") {
					m, ok := current.(map[string]any)
					if !ok {
						t.Fatalf("broken schema %s", path)
					}
					current, ok = m[key]
					if !ok {
						t.Fatalf("missing schema %s", path)
					}
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(doc)
}
