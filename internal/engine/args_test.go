package engine

import "testing"

// Real models — small ones especially — send wrong types and JSON null for
// individual tool arguments. These cases come from behavior observed against
// a live model, not from imagination.

func TestArgsCoercesScalarsToString(t *testing.T) {
	a, err := parseArgs("agent_create", `{"name":"researcher","model":42,"role":true}`)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	for _, tc := range []struct{ key, want string }{
		{"name", "researcher"},
		{"model", "42"},
		{"role", "true"},
	} {
		got, err := a.str(tc.key)
		if err != nil {
			t.Errorf("str(%q): %v", tc.key, err)
			continue
		}
		if got != tc.want {
			t.Errorf("str(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestArgsRejectsStructuredValueWithActionableMessage(t *testing.T) {
	// Observed live: the model sent {"model":{"type":"$1"}}. A whole-payload
	// unmarshal used to discard the fields it got right and return a Go
	// internals error the model could not act on.
	a, err := parseArgs("agent_create", `{"name":"ok","model":{"type":"$1"}}`)
	if err != nil {
		t.Fatalf("parseArgs must not fail on one bad field: %v", err)
	}

	if name, err := a.str("name"); err != nil || name != "ok" {
		t.Errorf("the correct field must survive: %q, %v", name, err)
	}

	_, err = a.str("model")
	if err == nil {
		t.Fatal("expected an error for a structured value")
	}
	for _, want := range []string{"agent_create", "model", "string"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q should name %q so the model can self-correct", err, want)
		}
	}
}

func TestArgsTreatsNullAsAbsent(t *testing.T) {
	// {"role": null} means "no change", not "wipe the system prompt".
	a, err := parseArgs("agent_update", `{"name":"bob","role":null}`)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if a.has("role") {
		t.Error("null must count as absent, otherwise the field gets wiped")
	}
	if !a.has("name") {
		t.Error("a present value must count as present")
	}
}

func TestArgsRequiredFieldMissing(t *testing.T) {
	a, _ := parseArgs("agent_create", `{"role":"x"}`)
	if _, err := a.reqStr("name"); err == nil {
		t.Fatal("expected a required-field error")
	}
}

func TestArgsStrSliceAcceptsSingleString(t *testing.T) {
	a, _ := parseArgs("forge_gear", `{"tags":"math"}`)
	got, err := a.strSlice("tags")
	if err != nil {
		t.Fatalf("strSlice: %v", err)
	}
	if len(got) != 1 || got[0] != "math" {
		t.Errorf("a lone string should become a one-element list, got %v", got)
	}
}

func TestArgsJSONStringAcceptsInlineObject(t *testing.T) {
	// args_schema arrives either as a JSON string or an inline object.
	inline, _ := parseArgs("forge_gear", `{"args_schema":{"type":"object"}}`)
	got, err := inline.jsonString("args_schema")
	if err != nil || got != `{"type":"object"}` {
		t.Errorf("inline object: got %q, err %v", got, err)
	}

	quoted, _ := parseArgs("forge_gear", `{"args_schema":"{\"type\":\"object\"}"}`)
	got, err = quoted.jsonString("args_schema")
	if err != nil || got != `{"type":"object"}` {
		t.Errorf("JSON string: got %q, err %v", got, err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
