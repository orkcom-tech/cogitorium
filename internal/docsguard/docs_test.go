// Package docsguard holds no code. It holds the two checks that would have
// caught the documentation rotting the last time it rotted.
//
// What happened: a redesign deleted the left rail, the floating panels, the
// layout presets and the fourteen-dial palette. Thirteen screenshots went on
// picturing all four for three releases, and the guide went on describing
// them, because nothing anywhere could tell the difference between a
// screenshot of this product and a screenshot of the one it used to be.
//
// Neither check reads the prose — no test can tell whether a sentence is still
// true. They check the two things that are mechanically checkable and that
// were, both times, the actual symptom: a picture the docs point at that is
// not there, and a picture that is older than the interface it claims to show.
package docsguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// root is the repository root, from this package's directory.
func root(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	return abs
}

// docs are the files whose image references are checked. README.md is in the
// list because it is the page most people see and the one nobody re-reads.
var docs = []string{"README.md", "docs/index.md", "docs/guide.md"}

var imageRef = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

// TestEveryImageReferenceResolves fails when a document points at a picture
// that is not in the tree. This is the check that catches a screenshot deleted
// during a re-shoot and a filename typed by hand.
func TestEveryImageReferenceResolves(t *testing.T) {
	r := root(t)
	for _, doc := range docs {
		body, err := os.ReadFile(filepath.Join(r, doc))
		if err != nil {
			t.Fatalf("reading %s: %v", doc, err)
		}
		for _, m := range imageRef.FindAllStringSubmatch(string(body), -1) {
			ref := m[1]
			if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
				continue
			}
			// A reference is relative to the file that makes it. README.md
			// sits at the root and writes docs/assets/x.png; the pages under
			// docs/ write assets/x.png for the same file.
			at := filepath.Join(r, filepath.Dir(doc), ref)
			if _, err := os.Stat(at); err != nil {
				t.Errorf("%s points at %s, which is not in the tree", doc, ref)
			}
		}
	}
}
