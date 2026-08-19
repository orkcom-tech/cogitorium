package view

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The published registry and this package must agree.
//
// Same shape as the openapi.yaml and plugin.schema.json checks: generated from
// the code, and this fails when the committed copy drifts. An author reading a
// stale registry writes an override against a model field that no longer
// exists — and learns about it from a plugin that installs, validates and
// disables itself at boot.
//
// Regenerate with: UPDATE_REGISTRY=1 go test ./internal/view/ -run TestTheRegistry
func TestTheRegistryMatchesTheTemplatesAndModels(t *testing.T) {
	want, err := RegistryJSON(Core(), CoreModels())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "docs", "registry.json")

	if os.Getenv("UPDATE_REGISTRY") != "" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("rewrote", path)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\n\nGenerate it:\n  UPDATE_REGISTRY=1 go test ./internal/view/ -run TestTheRegistry", err)
	}
	if string(got) != string(want) {
		t.Fatal("docs/registry.json is out of date.\n\nRegenerate it:\n" +
			"  UPDATE_REGISTRY=1 go test ./internal/view/ -run TestTheRegistry")
	}
}

// Every name the host registers has to appear, or an author has no way to
// discover it — which is the whole reason this file exists.
func TestEveryRegisteredNameIsDescribed(t *testing.T) {
	b, err := RegistryJSON(Core(), CoreModels())
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Entries []struct {
			Name  string `json:"name"`
			Model []struct {
				Path string `json:"path"`
			} `json:"model"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatal(err)
	}
	described := map[string]int{}
	for _, e := range reg.Entries {
		described[e.Name] = len(e.Model)
	}
	for name := range CoreModels() {
		n, ok := described[name]
		if !ok {
			t.Errorf("%s is registered and not described", name)
			continue
		}
		if n == 0 {
			t.Errorf("%s is described with no model at all, so an author learns nothing", name)
		}
	}
}

// The body of a template containing an {{if}} must not stop at that if's own
// {{end}} — which is what taking the first one does, and it would truncate
// every interesting template at its first conditional.
func TestABodyWithAConditionalIsNotTruncated(t *testing.T) {
	src := `{{define "a"}}start{{if .X}}inner{{end}}finish{{end}}{{define "b"}}second{{end}}`
	got := definitions(src)
	if got["a"] != "start{{if .X}}inner{{end}}finish" {
		t.Fatalf("body a is %q", got["a"])
	}
	if got["b"] != "second" {
		t.Fatalf("the definition after it was lost: %q", got["b"])
	}
}
