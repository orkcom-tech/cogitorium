package config

import (
	"os"
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

// The reference must not describe settings this server does not have either —
// a documented key that silently does nothing is worse than an undocumented
// one, because somebody writes it and believes it took effect.
func TestTheConfigurationReferenceInventsNothing(t *testing.T) {
	doc, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("the configuration reference could not be read: %v", err)
	}

	real := map[string]bool{}
	for _, env := range envLookups(t) {
		real[env] = true
	}
	// Named in prose rather than read by this package, and real: the CLI reads
	// it, and the guide points at it.
	real["COGITORIUM_CONFIG"] = true

	var invented []string
	for _, m := range regexp.MustCompile("`(COGITORIUM_[A-Z_]+)`").FindAllStringSubmatch(string(doc), -1) {
		if !real[m[1]] {
			invented = append(invented, m[1])
		}
	}
	if len(invented) > 0 {
		sort.Strings(invented)
		t.Fatalf("%s documents environment variables this server never reads:\n  %s",
			docsPath, strings.Join(invented, "\n  "))
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
