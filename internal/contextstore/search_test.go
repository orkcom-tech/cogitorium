package contextstore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These run against the REAL contextd if it is installed, because the whole
// point of this package is that contextd owns the space — a fake would test a
// second implementation of rules that live upstream.
func realSpace(t *testing.T) *Store {
	t.Helper()
	if _, err := exec.LookPath("contextd"); err != nil {
		t.Skip("contextd not installed")
	}
	dir := filepath.Join(t.TempDir(), "space")
	cmd := exec.Command("contextd", "--dir", dir, "init", "solo")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("contextd init solo failed here: %v\n%s", err, out)
	}
	// The Store drives one binary against its default space, so the space is
	// pointed at by wrapping the binary rather than by an option it does not
	// have.
	shim := filepath.Join(t.TempDir(), "contextd")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec contextd --dir "+dir+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(shim)
}

func TestSearchFindsWhatIsInsideAFile(t *testing.T) {
	s := realSpace(t)
	ctx := context.Background()
	if err := s.Put(ctx, "team/policy.md", "Retries stop after three attempts.\nThen we page.\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "team/style.md", "Write plainly.\n"); err != nil {
		t.Fatal(err)
	}

	res, err := s.Search(ctx, "three attempts", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatalf("a phrase that is in the space was not found: %+v", res)
	}
	hit := res.Hits[0]
	if hit.Path != "team/policy.md" {
		t.Fatalf("found it in %q", hit.Path)
	}
	if hit.Line != 1 || !strings.Contains(hit.Text, "three attempts") {
		t.Fatalf("a match must say where and what: %+v", hit)
	}
	// Nothing to do with the file's NAME — which is the whole point.
	if strings.Contains("team/policy.md", "three attempts") {
		t.Fatal("the fixture is wrong: the phrase must not be in the path")
	}
}

func TestSearchCanBeNarrowedToPathsAndFindsNothingHonestly(t *testing.T) {
	s := realSpace(t)
	ctx := context.Background()
	_ = s.Put(ctx, "team/policy.md", "Retries stop after three attempts.\n")
	_ = s.Put(ctx, "private/notes.md", "Retries stop after three attempts.\n")

	res, err := s.Search(ctx, "three attempts", "team/*", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range res.Hits {
		if !strings.HasPrefix(h.Path, "team/") {
			t.Fatalf("the path filter let %q through", h.Path)
		}
	}

	none, err := s.Search(ctx, "nothing here says this", "", 0)
	if err != nil {
		t.Fatalf("finding nothing is an answer, not an error: %v", err)
	}
	if none.Hits == nil {
		t.Fatal("no matches must be an empty list, not null — the difference is 'we looked'")
	}
	if len(none.Hits) != 0 {
		t.Fatalf("found %d matches for a phrase that is not there", len(none.Hits))
	}
}

func TestASearchCannotBecomeAnOption(t *testing.T) {
	// A contextd that always succeeds. It matters that this one WORKS: against
	// a missing binary every call fails, so `err != nil` would pass whether or
	// not anything was validated — which is exactly how this test first passed
	// with the guard deleted.
	dir := t.TempDir()
	bin := filepath.Join(dir, "contextd")
	if err := os.WriteFile(bin, []byte(`#!/bin/sh
echo "$@" >> `+filepath.Join(dir, "argv")+`
echo '{"query":"x","matches":[],"files_matched":0,"files_scanned":0,"truncated":false}'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(bin)
	ctx := context.Background()

	for _, bad := range []struct{ query, path, why string }{
		{"--dir /etc", "", "a query starting with a dash"},
		{"   ", "", "an empty search"},
		{"fine", "--dir", "a path filter starting with a dash"},
	} {
		_, err := s.Search(ctx, bad.query, bad.path, 0)
		if err == nil {
			t.Fatalf("%s must be refused", bad.why)
		}
		if errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s was passed to contextd and only failed there: %v", bad.why, err)
		}
	}
	// And nothing reached the binary at all.
	if _, err := os.ReadFile(filepath.Join(dir, "argv")); err == nil {
		argv, _ := os.ReadFile(filepath.Join(dir, "argv"))
		t.Fatalf("a refused search was still executed: %s", argv)
	}
}

// contextd today answers an empty search with "matches": [], but the JSON
// contract does not promise it, and a caller that ranged over a nil slice
// would read "we did not look" as "there is nothing".
func TestNoMatchesIsAlwaysAListNeverNull(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "contextd")
	script := "#!/bin/sh\necho '" + `{"query":"x","matches":null}` + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := New(bin).Search(context.Background(), "x", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Hits == nil {
		t.Fatal("a null matches list reached the caller as nil")
	}
}

func TestASaveIsRefusedWhenTheFileMovedUnderIt(t *testing.T) {
	s := realSpace(t)
	ctx := context.Background()
	if err := s.Put(ctx, "team/policy.md", "one\n"); err != nil {
		t.Fatal(err)
	}
	opened, exists, err := s.Version(ctx, "team/policy.md")
	if err != nil || !exists {
		t.Fatalf("version %q exists=%v err=%v", opened, exists, err)
	}

	// Somebody else saves while the first editor is still typing.
	if err := s.Put(ctx, "team/policy.md", "two\n"); err != nil {
		t.Fatal(err)
	}

	err = s.PutIfUnchanged(ctx, "team/policy.md", "one, edited\n", opened)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("the stale save was accepted (err %v) — the other person's work is gone", err)
	}
	// And the refusal has to be actionable: which version it is at, and which
	// one you opened.
	if !strings.Contains(err.Error(), opened) {
		t.Fatalf("the refusal must name the version you opened: %v", err)
	}

	body, err := s.Get(ctx, "team/policy.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(body) != "two" {
		t.Fatalf("the refused save wrote anyway: %q", body)
	}
}

func TestASaveGoesThroughWhenNobodyElseTouchedIt(t *testing.T) {
	s := realSpace(t)
	ctx := context.Background()
	_ = s.Put(ctx, "team/policy.md", "one\n")
	opened, _, _ := s.Version(ctx, "team/policy.md")

	if err := s.PutIfUnchanged(ctx, "team/policy.md", "one, edited\n", opened); err != nil {
		t.Fatalf("an ordinary save was refused: %v", err)
	}
	body, _ := s.Get(ctx, "team/policy.md")
	if strings.TrimSpace(body) != "one, edited" {
		t.Fatalf("the save did not land: %q", body)
	}

	// A new file has no version to have moved, and must still be writable.
	if err := s.PutIfUnchanged(ctx, "team/fresh.md", "new\n", ""); err != nil {
		t.Fatalf("writing a file that does not exist yet was refused: %v", err)
	}
}
