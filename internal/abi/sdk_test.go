package abi

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The SDKs are a promise, and this is what keeps it.
//
// Nine calls, identical on every tier and in every language — that sentence is
// in the package doc, on the plugin screen and in the guides. It stops being
// true the moment somebody adds a tenth call here and stops after the Go one,
// and nothing else in the build would notice: every SDK still compiles, every
// existing plugin still runs, and the promise is quietly false for whoever
// picked the wrong language.
func TestEverySDKOffersEveryHostCall(t *testing.T) {
	for _, sdk := range sdks(t) {
		source := read(t, sdk.dir)
		for _, call := range Calls() {
			if !strings.Contains(source, `"`+call+`"`) {
				t.Errorf("the %s SDK never sends %q.\n"+
					"Every tier offers the same nine calls in the same words. An author who "+
					"picked %s and cannot make this call has been told something untrue.",
					sdk.name, call, sdk.name)
			}
		}
		for _, op := range []KVOp{KVGet, KVSet, KVDelete, KVList, KVCAS, KVIncr} {
			if !strings.Contains(source, `"`+string(op)+`"`) {
				t.Errorf("the %s SDK has no kv %q.\n"+
					"cas and incr especially: without them an author reaches for "+
					"read-then-write, and two instances of their plugin WILL race.",
					sdk.name, op)
			}
		}
	}
}

// A plugin states the contract its code speaks and the host refuses a
// mismatch. An SDK that states the wrong number turns every plugin written
// against it into a plugin refused at load — with a message about the plugin,
// which is the one place the author will not think to look.
func TestEverySDKStatesThisContract(t *testing.T) {
	// Written once per language and spelled differently in each: a Go const, a
	// Python assignment, a Rust const with its type in the middle. The pattern
	// is what they have in common — the name, then the number.
	stated := regexp.MustCompile(`(?i)\bcontract\b[^=\n]*=\s*` + strconv.Itoa(Version) + `\b`)
	for _, sdk := range sdks(t) {
		if !stated.MatchString(read(t, sdk.dir)) {
			t.Errorf("the %s SDK does not state contract %d.\n"+
				"If the contract moved, this test moves with it — and so does every SDK, "+
				"in the same commit.", sdk.name, Version)
		}
	}
}

type sdk struct{ name, dir string }

func sdks(t *testing.T) []sdk {
	t.Helper()
	root := filepath.Join("..", "..", "sdk")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("the sdk directory is not readable: %v", err)
	}
	var found []sdk
	for _, e := range entries {
		if e.IsDir() {
			found = append(found, sdk{name: e.Name(), dir: filepath.Join(root, e.Name())})
		}
	}
	if len(found) == 0 {
		t.Fatal("no SDKs found, which means this test is checking nothing")
	}
	return found
}

// read concatenates an SDK's source. Every language here writes the wire names
// as string literals, so one search over the text answers the question without
// this test needing a parser per language.
func read(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// target/ is cargo's build output and holds a copy of every
			// dependency's source, which would make this test pass on
			// somebody else's strings.
			if d.Name() == "target" || d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".py", ".rs":
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.Write(body)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	return b.String()
}
