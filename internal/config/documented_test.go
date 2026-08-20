package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// docsPath is the reference this test defends.
const docsPath = "../../docs/configuration.md"

// Every setting has to be written down somewhere a person can find it.
//
// It was not. The guide explained a dozen of them where they happened to come
// up and the rest existed only in this file, so the honest answer to "what can
// I set" was "read the source" — which is not an answer for somebody deploying
// this.
//
// A list maintained by hand rots on the first commit that adds a field. So it
// is checked instead: this reads the struct and the environment lookups, and
// fails if any of them is missing from the reference. A setting added without
// a line in the documentation does not merge.
func TestEveryConfigurationKeyIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("the configuration reference could not be read: %v", err)
	}
	text := string(doc)

	var missing []string
	for _, key := range yamlKeys() {
		// Backticked, because a bare word can appear in a sentence by
		// coincidence and a key in a table cannot.
		if !strings.Contains(text, "`"+key+"`") {
			missing = append(missing, key+" (a config.yaml key)")
		}
	}
	for _, env := range envLookups(t) {
		if !strings.Contains(text, "`"+env+"`") {
			missing = append(missing, env+" (an environment variable)")
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("these settings exist and are not in %s:\n  %s\n\n"+
			"Add a row for each. Somebody deploying this reads that file and nothing else.",
			docsPath, strings.Join(missing, "\n  "))
	}
}

// The documentation must not describe settings this server does not have — a
// documented key that silently does nothing is worse than an undocumented one,
// because somebody writes it and believes it took effect.
//
// Both pages, not only the reference. docs/index.md carries the same settings
// with the reasoning attached, and it is where `COGITORIUM_QUEUE_WORKERS` and
// `COGITORIUM_QUEUE_MAX_PER_WORKSPACE` sat for three releases: two variables
// this server has never read, in a table headed "Environment", beside two
// settings that are real and file-only.
func TestTheConfigurationReferenceInventsNothing(t *testing.T) {
	var doc []byte
	for _, page := range []string{docsPath, "../../docs/index.md"} {
		b, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("%s could not be read: %v", page, err)
		}
		doc = append(doc, b...)
	}

	// Everything the REPOSITORY reads, not only what this package does: the
	// documentation covers the command line too, and COGITORIUM_URL and
	// COGITORIUM_TOKEN are read by internal/client rather than here.
	real := readEverywhere(t)
	// Named in prose rather than read by any package, and real: it is what
	// --config falls back to, resolved before a Config exists.
	real["COGITORIUM_CONFIG"] = true

	var invented []string
	for _, m := range regexp.MustCompile("`(COGITORIUM_[A-Z_]+)`").FindAllStringSubmatch(string(doc), -1) {
		if !real[m[1]] {
			invented = append(invented, m[1])
		}
	}
	if len(invented) > 0 {
		sort.Strings(invented)
		t.Fatalf("the documentation names environment variables this server never reads:\n  %s\n\n"+
			"Checked in %s and docs/index.md. Somebody sets one of these and believes it took effect.",
			strings.Join(invented, "\n  "), docsPath)
	}
}

// yamlKeys is every top-level key a config.yaml may carry.
func yamlKeys() []string {
	var keys []string
	t := reflect.TypeOf(Config{})
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("yaml")
		name, _, _ := strings.Cut(tag, ",")
		// "-" is a field with no key at all: a credential that must never be
		// written in a file. Those are documented as environment variables.
		if name == "" || name == "-" {
			continue
		}
		keys = append(keys, name)
	}
	return keys
}

// envLookups is every COGITORIUM_* this package actually reads, taken from the
// source rather than from a list beside it — a list beside it would be the
// thing that drifts.
func envLookups(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("config.go could not be read: %v", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range regexp.MustCompile(`os\.Getenv\("(COGITORIUM_[A-Z_]+)"\)`).FindAllStringSubmatch(string(src), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatal("no environment lookups were found in config.go, which means this test is " +
			"checking nothing — the pattern it greps for must have changed")
	}
	sort.Strings(out)
	return out
}

// readEverywhere is every COGITORIUM_* any Go file in this repository looks up.
//
// Walked rather than listed, for the same reason the rest of this test walks
// the struct: a list beside the code is the thing that drifts. Test files are
// included deliberately — a variable only a test reads is still a variable the
// documentation may honestly describe.
func readEverywhere(t *testing.T) map[string]bool {
	t.Helper()
	pattern := regexp.MustCompile(`os\.Getenv\("(COGITORIUM_[A-Z_]+)"\)`)
	found := map[string]bool{}
	root := "../.."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range pattern.FindAllStringSubmatch(string(b), -1) {
			found[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("no environment lookups were found anywhere in the repository, which means this " +
			"test is checking nothing — the pattern it greps for must have changed")
	}
	return found
}
