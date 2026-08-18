package contextstore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/update"
)

// These run against the REAL contextd if it is installed, because the whole
// point of this package is that contextd owns the space — a fake would test a
// second implementation of rules that live upstream.
func realSpace(t *testing.T) *Store {
	t.Helper()
	bin := contextdBinary(t)
	dir := filepath.Join(t.TempDir(), "space")
	cmd := exec.Command(bin, "--dir", dir, "init", "solo", "--name", "test", "--role", "test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("contextd init solo failed here: %v\n%s", err, out)
	}
	// The Store drives one binary against its default space, so the space is
	// pointed at by wrapping the binary rather than by an option it does not
	// have.
	shim := filepath.Join(t.TempDir(), "contextd")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec "+bin+" --dir "+dir+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(shim)
}

// contextdBinary is the contextd these tests run against.
//
// COGITORIUM_CONTEXTD wins, so a developer can point this at a build of
// Contextverse they are working on — which is how `file delete` and
// `--if-version` were tested before either had been released. Otherwise the one
// on PATH. Skipped, never failed, when there is none: contextd is a separate
// product and its absence is a fact about the machine, not a defect here.
//
// A contextd that is PRESENT AND TOO OLD is a different fact, and it is not
// skipped — see requireSupportedContextd.
func contextdBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("COGITORIUM_CONTEXTD"); bin != "" {
		return bin
	}
	bin, err := exec.LookPath("contextd")
	if err != nil {
		t.Skip("contextd not installed and COGITORIUM_CONTEXTD not set")
		// Unreachable; Skip stops the test. Kept so the signature is honest
		// about always returning a path when it returns at all.
		return ""
	}
	return bin
}

// requireSupportedContextd FAILS on a contextd older than this build declares
// it works with, rather than skipping.
//
// The distinction against contextdBinary is the whole point. No contextd is a
// fact about the machine, so those tests skip. A contextd BELOW MinContextd is
// a machine that cannot run this product correctly — the server says so at
// startup, in as many words, and refusing quietly here would let a developer
// believe a green suite meant a working install.
//
// It failed before this existed, just not usefully: `--if-version` reached an
// old binary, cobra answered "unknown flag: --if-version", and the test
// reported that the other person's work had been lost. Which is what would
// happen — the save really does become last-write-wins — but the message sent
// the reader hunting through this package for a bug that was a version.
func requireSupportedContextd(t *testing.T) {
	t.Helper()
	out, err := exec.Command(contextdBinary(t), "file", "put", "--help").CombinedOutput()
	if err == nil && strings.Contains(string(out), "--if-version") {
		return
	}
	t.Fatalf("this contextd predates --if-version, so a save here is last-write-wins rather than "+
		"compare-and-swap. Cogitorium needs Contextverse %s or newer, and says so at startup.\n"+
		"  install it:  scripts/ci/install-contextd.sh ~/.local/bin\n"+
		"  or point at a build you are working on:  COGITORIUM_CONTEXTD=/path/to/contextd go test ./...\n"+
		"  using: %s", update.MinContextd, contextdBinary(t))
}

// requireDelete is requireSupportedContextd. `file delete` and `--if-version`
// shipped in the same Contextverse release, so the two were always one gate
// wearing two names — and they behaved differently, which is the part that
// mattered: one skipped and one failed, on the same machine, for the same
// reason.
//
// Kept as a name because the tests that call it are about deletion, and
// reading `requireDelete` at the top of one says which capability is at stake.
func requireDelete(t *testing.T) {
	t.Helper()
	requireSupportedContextd(t)
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
	requireSupportedContextd(t)
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
	requireSupportedContextd(t)
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

// Forgetting used to mean emptying a document, because contextd had no delete
// and its storage layer's SoftDelete was unreachable. It has one now.
func TestForgettingActuallyRemovesTheDocument(t *testing.T) {
	requireDelete(t)
	s := realSpace(t)
	ctx := context.Background()
	if err := s.Put(ctx, "team/policy.md", "Retries stop after three attempts.\n"); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, "team/policy.md", ""); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Gone from the space, so gone from every prompt assembled from it — which
	// is the difference between forgetting and hiding.
	files, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == "team/policy.md" {
			t.Fatalf("the document is still in the space after being forgotten")
		}
	}

	// And searching no longer finds what it said, which is the other half: a
	// memory that is gone from the listing and still turns up in a search is
	// not gone.
	res, err := s.Search(ctx, "three attempts", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range res.Hits {
		if h.Path == "team/policy.md" {
			t.Fatalf("a forgotten document is still searchable: %+v", h)
		}
	}
}

func TestForgettingIsRefusedWhenSomebodyJustRewroteIt(t *testing.T) {
	requireDelete(t)
	s := realSpace(t)
	ctx := context.Background()
	if err := s.Put(ctx, "team/policy.md", "one\n"); err != nil {
		t.Fatal(err)
	}
	opened, _, err := s.Version(ctx, "team/policy.md")
	if err != nil {
		t.Fatal(err)
	}

	// Somebody else saves while the operator is deciding.
	if err := s.Put(ctx, "team/policy.md", "two, and this matters\n"); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, "team/policy.md", opened); !errors.Is(err, ErrStale) {
		t.Fatalf("a stale delete was accepted (err %v) — their work is gone", err)
	}
	body, err := s.Get(ctx, "team/policy.md")
	if err != nil {
		t.Fatalf("the document was removed anyway: %v", err)
	}
	if !strings.Contains(body, "this matters") {
		t.Fatalf("the refused delete changed the document: %q", body)
	}
}

// The save guard is a real compare-and-swap now: contextd itself refuses,
// inside one call, rather than this package checking and then writing.
func TestTheSaveGuardIsContextdsOwnRefusal(t *testing.T) {
	requireDelete(t) // the same release added --if-version
	s := realSpace(t)
	ctx := context.Background()
	if err := s.Put(ctx, "team/policy.md", "one\n"); err != nil {
		t.Fatal(err)
	}
	opened, _, _ := s.Version(ctx, "team/policy.md")
	if err := s.Put(ctx, "team/policy.md", "two\n"); err != nil {
		t.Fatal(err)
	}

	err := s.PutIfUnchanged(ctx, "team/policy.md", "one, edited\n", opened)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("the stale save was accepted (err %v)", err)
	}
	body, _ := s.Get(ctx, "team/policy.md")
	if strings.TrimSpace(body) != "two" {
		t.Fatalf("the refused save wrote anyway: %q", body)
	}

	// And a save with the right expectation still lands.
	cur, _, _ := s.Version(ctx, "team/policy.md")
	if err := s.PutIfUnchanged(ctx, "team/policy.md", "two, edited\n", cur); err != nil {
		t.Fatalf("an ordinary save was refused: %v", err)
	}
	body, _ = s.Get(ctx, "team/policy.md")
	if strings.TrimSpace(body) != "two, edited" {
		t.Fatalf("the save did not land: %q", body)
	}
}

// EnsureSpace is what makes an ordinary install work. Without it a person who
// installed contextd — by any route — still met "no context space initialized"
// on every context screen, because a binary is not a space.
func TestEnsureSpaceCreatesOneAndThenLeavesItAlone(t *testing.T) {
	requireSupportedContextd(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := New(contextdBinary(t))
	ctx := context.Background()

	if st := s.CheckStatus(ctx); st.Available {
		t.Fatalf("the fixture is wrong: this HOME already has a space (%+v)", st)
	}

	s.EnsureSpace(ctx)
	st := s.CheckStatus(ctx)
	if !st.Available {
		t.Fatalf("no space after EnsureSpace: %+v", st)
	}

	// A file written into it survives a second EnsureSpace. Re-initialising an
	// existing space would be the worst possible bug in this function: it runs
	// on every start, so it would eat the operator's memory on the next one.
	if err := s.Put(ctx, "team/policy.md", "keep me\n"); err != nil {
		t.Fatalf("put: %v", err)
	}
	s.EnsureSpace(ctx)
	body, err := s.Get(ctx, "team/policy.md")
	if err != nil || strings.TrimSpace(body) != "keep me" {
		t.Fatalf("a second EnsureSpace disturbed the space: %q %v", body, err)
	}
}

// With no contextd there is nothing to initialise, and inventing something
// would be worse than the honest "unavailable" every surface already reports.
func TestEnsureSpaceDoesNothingWithoutContextd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := New(filepath.Join(t.TempDir(), "contextd-that-is-not-there"))

	s.EnsureSpace(context.Background())

	if _, err := os.Stat(filepath.Join(home, ".context")); !os.IsNotExist(err) {
		t.Fatalf("something was created without a contextd to create it: %v", err)
	}
}
