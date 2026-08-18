package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// The record has to say what a tool was called WITH, and it has to survive a
// write_file whose argument is a whole document.

func TestArgumentsAreKeptWholeWhenSmall(t *testing.T) {
	got := abridgeArgs(`{"path":"out/report.md","overwrite":true}`)
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, got)
	}
	if m["path"] != "out/report.md" {
		t.Fatalf("path lost: %v", m["path"])
	}
	if m["overwrite"] != true {
		t.Fatalf("non-string value lost: %v", m["overwrite"])
	}
}

func TestABodySizedArgumentIsCutAndSaysSo(t *testing.T) {
	body := strings.Repeat("x", 50_000)
	raw, _ := json.Marshal(map[string]string{"path": "out/a.md", "content": body})
	got := abridgeArgs(string(raw))

	if len(got) > argTotalMax {
		t.Fatalf("record kept %d bytes, cap is %d", len(got), argTotalMax)
	}
	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	// The key that matters is still there, in full.
	if m["path"] != "out/a.md" {
		t.Fatalf("the short argument was damaged: %q", m["path"])
	}
	// And the long one is visibly an abridgement, not a value.
	if !strings.Contains(m["content"], "(50000 bytes)") {
		t.Fatalf("a cut value must say it was cut, got %q", m["content"][:60])
	}
	if len(m["content"]) > argValueMax+40 {
		t.Fatalf("value cut to %d, cap is %d", len(m["content"]), argValueMax)
	}
}

func TestManyLargeArgumentsCannotBeatTheTotalCap(t *testing.T) {
	obj := map[string]string{}
	for i := 0; i < 200; i++ {
		obj[string(rune('a'+i%26))+strings.Repeat("k", i)] = strings.Repeat("v", 300)
	}
	raw, _ := json.Marshal(obj)
	got := abridgeArgs(string(raw))
	if len(got) > argTotalMax {
		t.Fatalf("per-value cuts alone let %d bytes through, cap is %d", len(got), argTotalMax)
	}
	if !json.Valid(got) {
		t.Fatalf("the fallback must still be JSON: %s", got)
	}
}

func TestNonObjectArgumentsSurvive(t *testing.T) {
	// A gear declares its own schema and may take an array.
	got := abridgeArgs(`["one","two"]`)
	if string(got) != `["one","two"]` {
		t.Fatalf("array arguments dropped: %s", got)
	}
	if abridgeArgs("") != nil {
		t.Fatalf("no arguments should record no arguments")
	}
	if abridgeArgs("not json at all") != nil {
		t.Fatalf("unparseable short arguments should record nothing, got %s", abridgeArgs("not json at all"))
	}
}

func TestTheRecordCarriesArgumentsThroughToJSON(t *testing.T) {
	r := Record{Tools: []ToolRun{{Name: "gear_deploy", Agent: "releaser", OK: true,
		Args: abridgeArgs(`{"target":"production"}`)}}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"target":"production"`) {
		t.Fatalf("arguments did not reach the record: %s", b)
	}
	// A record with no arguments must not carry an empty field.
	b2, _ := json.Marshal(Record{Tools: []ToolRun{{Name: "list_gears"}}})
	if strings.Contains(string(b2), `"args"`) {
		t.Fatalf("absent arguments should be absent: %s", b2)
	}
}

func TestAnEmptyRecordStillStatesItsShape(t *testing.T) {
	b, _ := json.Marshal(Record{})
	for _, want := range []string{`"tools":[]`, `"files":[]`, `"context":[]`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("a record that says nothing happened must say it with %s: %s", want, b)
		}
	}
}
