package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The committed schema and this package must agree.
//
// Same shape as the openapi.yaml check: the document is generated from the
// code, and this fails when the committed copy has drifted. A published schema
// that is wrong is worse than none — an author's editor would reject manifests
// the server accepts, or accept ones it refuses, and either way the author
// trusts the wrong one.
//
// Regenerate with: go test ./internal/plugin/ -run TestThePublishedSchema -update
func TestThePublishedSchemaMatchesThisCode(t *testing.T) {
	want, err := JSONSchemaBytes()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "docs", "plugin.schema.json")

	if os.Getenv("UPDATE_SCHEMA") != "" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("rewrote", path)
		return
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\n\nGenerate it with:\n  UPDATE_SCHEMA=1 go test ./internal/plugin/ -run TestThePublishedSchema", err)
	}
	if string(got) != string(want) {
		t.Fatalf("docs/plugin.schema.json is out of date with this package.\n\n"+
			"Regenerate it:\n  UPDATE_SCHEMA=1 go test ./internal/plugin/ -run TestThePublishedSchema\n\n"+
			"got %d bytes, want %d", len(got), len(want))
	}
}

// A vocabulary the validator knows and the schema does not is a manifest an
// editor calls broken and the server accepts. This catches the case that
// actually happens: somebody adds a technology or a mount point and forgets
// this file exists.
func TestTheSchemaKnowsEveryVocabularyTheValidatorDoes(t *testing.T) {
	b, err := JSONSchemaBytes()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties struct {
			Needs struct {
				Enum []string `json:"enum"`
			} `json:"needs"`
			Mounts struct {
				Items struct {
					Properties struct {
						Point struct {
							Enum []string `json:"enum"`
						} `json:"point"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"mounts"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}

	has := func(all []string, want string) bool {
		for _, v := range all {
			if v == want {
				return true
			}
		}
		return false
	}
	for name := range technologies {
		if !has(doc.Properties.Needs.Enum, name) {
			t.Errorf("the validator accepts needs: %s and the schema does not", name)
		}
	}
	for point := range mountPoints {
		if !has(doc.Properties.Mounts.Items.Properties.Point.Enum, point) {
			t.Errorf("the validator accepts the mount point %s and the schema does not", point)
		}
	}
}
