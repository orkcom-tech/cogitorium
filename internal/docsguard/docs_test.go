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
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
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

// TestNoScreenshotPredatesTheInterface fails when a numbered screenshot was
// last committed before the interface it pictures was last changed.
//
// It asks git, not the filesystem. A checkout stamps every file with the time
// it was written, in no useful order, so an mtime comparison is meaningful on
// the machine that made the change and pure coin-flip anywhere else. Commit
// times are the same on every clone that has the history.
//
// Which means it needs the history: on a shallow clone — what actions/checkout
// gives by default — it skips rather than guessing. That is the honest
// arrangement. This is a guard for the machine where the change is made, which
// is the only place the fix (re-shoot, one command) can happen anyway.
//
// It is deliberately loud. An interface change does not touch every screen, so
// it will sometimes name a picture that is still accurate. Re-shooting the set
// takes under a minute; the failure it replaces was thirteen screenshots of
// deleted software surviving three releases.
//
// What is checked is the shooter's own stamp rather than the pictures — see the
// body of the test for why comparing the PNGs is wrong in exactly the case that
// matters most.

func TestNoScreenshotPredatesTheInterface(t *testing.T) {
	r := root(t)

	if out, err := exec.Command("git", "-C", r, "rev-parse", "--is-shallow-repository").Output(); err != nil {
		t.Skip("no git history here, so commit times are unavailable; this guard runs where the change is made")
	} else if strings.TrimSpace(string(out)) == "true" {
		t.Skip("shallow clone: commit times are unavailable, and guessing from file times would be a coin flip")
	}

	ui := lastCommit(t, r, "web/src")
	if ui.IsZero() {
		t.Skip("web/src has no commit yet")
	}

	// WHAT IS COMPARED, and why it is not the pictures themselves.
	//
	// The first version of this test compared each PNG's last commit against
	// web/src's. That is wrong in the one case that matters most: a re-shoot
	// that produces a byte-identical picture — because that screen genuinely
	// did not change — leaves git with nothing to commit, so the file's commit
	// time stays where it was and the guard calls it stale. It named sixteen
	// pictures that had been re-shot minutes earlier and were correct, which is
	// how a guard teaches people to ignore it.
	//
	// An unchanged screenshot after a re-shoot is the PROOF that the screen did
	// not change, not evidence against it. What actually needs to be newer than
	// the interface is the ACT of shooting, so the shooter records when it ran
	// and this compares that.
	stamp := filepath.Join("docs", "assets", ".shot-at")
	if _, err := os.Stat(filepath.Join(r, stamp)); err != nil {
		t.Fatalf("docs/assets/.shot-at is missing — the screenshots have no record of when they were taken.\n" +
			"Re-shoot them against a seeded install:\n" +
			"  cd web && node scripts/shoot-docs.mjs http://127.0.0.1:8894")
	}
	shot := lastCommit(t, r, stamp)
	if shot.IsZero() {
		// Being added in this change, which is what a re-shoot leaves behind.
		return
	}
	if shot.Before(ui) {
		t.Errorf("the screenshots were last taken on %s and web/src changed on %s, "+
			"so they picture an interface that has since moved.\n"+
			"Re-shoot them against a seeded install:\n"+
			"  cd web && node scripts/shoot-docs.mjs http://127.0.0.1:8894",
			shot.Format(time.RFC3339), ui.Format(time.RFC3339))
	}
}

// lastCommit is when path was last committed, or the zero time if it never was.
func lastCommit(t *testing.T, repo, path string) time.Time {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "log", "-1", "--format=%ct", "--", path).Output()
	if err != nil {
		t.Fatalf("git log for %s: %v", path, err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("git returned %q as a commit time for %s", s, path)
	}
	return time.Unix(secs, 0)
}
