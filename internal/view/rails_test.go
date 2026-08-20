package view

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// This product draws the same rail twice: into a document, by the templates,
// and into a live page, by the application. Two renderers is a fact about the
// runtimes. Two LISTS was a fact about nobody having joined them up — and
// every difference between them reached somebody as "why is this screen
// different from that one": a logo on half the screens, destinations on the
// other half, Plugins drawn twice where the two overlapped.
//
// There is one list now, HostNav, and the application asks for it. This test
// is what stops a second one growing back: it fails if Rail.tsx starts naming
// destinations of its own.
func TestTheApplicationDoesNotKeepItsOwnRail(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "Rail.tsx"))
	if err != nil {
		t.Skipf("the frontend source is not present: %v", err)
	}
	src := string(b)

	// Formatting-insensitive: prettier writes `api\n  .rail(...)` when the
	// chain is long enough, and a test that only knows one spelling of the
	// call is a test that fails for the wrong reason.
	if !regexp.MustCompile(`\.rail\(`).MatchString(src) {
		t.Error("Rail.tsx no longer asks the server what is in the rail — the list would then exist twice")
	}

	// Every host destination, as a literal href. One of these in the
	// application's source means the list is being restated there.
	for _, item := range HostNav("", true) {
		if item.Href == "/workspaces" {
			// The brand links there, and so does the way out of a workspace.
			// Neither is a destination list.
			continue
		}
		if regexp.MustCompile(`href[=:]\s*"` + regexp.QuoteMeta(item.Href) + `"`).MatchString(src) {
			t.Errorf("Rail.tsx names %q itself; the rail is described by the server so that it is one list", item.Href)
		}
	}
}
