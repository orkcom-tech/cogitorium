package library

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/store"
)

// The library is an index over files that live somewhere else. Every test
// here is about the seam between those two stores: a name that the index
// accepts is a name the caller has already written a file for, so the
// interesting failures are "the row exists and the file does not" and the
// reverse.

func TestMain(m *testing.M) {
	// Save logs one Info line per call; discarding it keeps a failing run
	// readable instead of interleaved with the catalogue's own chatter.
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// newLibrary opens a real SQLite database — the same migrations the product
// runs — because the rules under test are enforced partly by Go and partly by
// the schema (UNIQUE name, UNIQUE path), and a fake would only prove the fake.
func newLibrary(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a real database failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db), db
}

// indexed counts the rows the catalogue actually holds. This is the half of
// the rule that a "did it return an error?" test misses: the bug this package
// documents was a row/file pair where one half survived a rejection.
func indexed(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM instructions`).Scan(&n); err != nil {
		t.Fatalf("counting instructions failed: %v", err)
	}
	return n
}

func TestInvalidNameIsRefusedAndNothingIsIndexed(t *testing.T) {
	t.Parallel()
	lib, db := newLibrary(t)
	ctx := t.Context()

	cases := []struct {
		name string
		why  string
	}{
		{"", "an empty name indexes the path library/.md, which is not an instruction"},
		{"   ", "whitespace-only is empty once trimmed and must not become a file called library/ .md"},
		{"Code-Review", "uppercase: two operators would create library/Code-Review.md and library/code-review.md and see one entry on a case-insensitive filesystem"},
		{"code review", "a space in the name becomes a space in the context path and in every CLI argument built from it"},
		{"library/secret", "a separator moves the written file out of the library root, where nothing lists it"},
		{"../escape", "traversal writes the instruction outside the context space entirely"},
		{"..", "the traversal name itself"},
		{"nested/../rule", "traversal in the middle is still traversal"},
		{`back\slash`, "a backslash is a separator on Windows even when it is not one here"},
		{"a", "a single character: the catalogue promises at least two"},
		{"1st-rule", "a leading digit — names must start with a letter so they read as identifiers"},
		{"-rule", "a leading dash turns the name into a flag for any CLI it is passed to"},
		{"_rule", "a leading underscore is not a letter"},
		{"rule.md", "PathFor already appends .md; accepting it produces library/rule.md.md"},
		{"rule name\nother", "a newline splits the name across lines in every log and path built from it"},
		{"rulé", "non-ASCII: the byte encoding of the path stops being predictable"},
		{"rule%2fname", "percent-encoding is not decoding — it is just a stranger name"},
		{strings.Repeat("a", 62), "62 characters — one past the documented maximum"},
	}

	for _, tc := range cases {
		got, err := lib.Save(ctx, tc.name, "some description", []string{"tag"}, 0, 0)
		if err == nil {
			t.Errorf("Save(%q) was accepted: %s", tc.name, tc.why)
		} else {
			// The handler puts this string straight into a 400 body, so it has
			// to name the offender and say what was wrong with it.
			if !strings.Contains(err.Error(), "invalid") {
				t.Errorf("Save(%q) failed with %q, which never tells the operator the name was the problem", tc.name, err)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", strings.TrimSpace(tc.name))) {
				t.Errorf("Save(%q) failed with %q, which does not quote the rejected name back", tc.name, err)
			}
		}
		if got.ID != 0 || got.Path != "" {
			t.Errorf("Save(%q) returned a populated instruction %+v; a refused name must produce nothing", tc.name, got)
		}
		// The point of the whole test: not merely that it complained, but that
		// it complained before writing.
		if n := indexed(t, db); n != 0 {
			t.Fatalf("Save(%q) left %d row(s) in the index; validation must happen before anything is written", tc.name, n)
		}
		// Cross-check via the API the UI actually calls, in case a row was
		// written somewhere COUNT(*) above would not look.
		if all, err := lib.List(ctx, "", ""); err != nil || len(all) != 0 {
			t.Fatalf("after refusing %q the catalogue lists %v (err %v); it must still be empty", tc.name, all, err)
		}
	}
}

// The bounds are asserted as literals rather than by re-deriving them from the
// pattern: a test that recomputes the rule from the same regexp passes no
// matter what the regexp becomes.
func TestNameLengthBoundsAreExact(t *testing.T) {
	t.Parallel()

	shortest := "ab"
	longest := "a" + strings.Repeat("b", 60)
	if len(longest) != 61 {
		t.Fatalf("the test built a %d-character name; it meant 61", len(longest))
	}

	if err := ValidateName("a"); err == nil {
		t.Error(`ValidateName("a") accepted a one-character name; the documented minimum is two`)
	}
	if err := ValidateName(shortest); err != nil {
		t.Errorf("ValidateName(%q) rejected the shortest legal name: %v", shortest, err)
	}
	if err := ValidateName(longest); err != nil {
		t.Errorf("ValidateName(a 61-character name) rejected the longest legal name: %v", err)
	}
	if err := ValidateName(longest + "c"); err == nil {
		t.Error("ValidateName(a 62-character name) accepted one past the documented maximum")
	}
}

// ValidateName exists so the caller can check *before* writing the text to
// Contextverse. If validation ever slid behind the database work, a rejected
// save would still have touched storage — so prove the ordering by taking the
// database away.
func TestSaveValidatesBeforeTouchingTheDatabase(t *testing.T) {
	t.Parallel()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a real database failed: %v", err)
	}
	db.Close()
	lib := NewStore(db)
	ctx := t.Context()

	_, err = lib.Save(ctx, "Bad Name", "d", nil, 0, 0)
	if err == nil {
		t.Fatal("Save accepted an invalid name")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("Save reported %q; with the database unusable it must still be the NAME that fails first, not the storage", err)
	}

	// Without this, the assertion above would pass on a Save that never
	// reached the database at all. A legal name must get as far as the
	// (broken) storage and fail there.
	if _, err := lib.Save(ctx, "good-name", "d", nil, 0, 0); err == nil {
		t.Fatal("Save succeeded against a closed database")
	} else if strings.Contains(err.Error(), "invalid") {
		t.Fatalf("a legal name was rejected as invalid: %v", err)
	}
}

// The path is a promise to Contextverse: rows written last year still point at
// it. Asserted as a literal, because computing it from Root would agree with
// any Root.
func TestPathForIsTheLibraryRoot(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"code-review": "library/code-review.md",
		"ab":          "library/ab.md",
	} {
		if got := PathFor(name); got != want {
			t.Errorf("PathFor(%q) = %q, want %q — moving the root orphans every instruction already written", name, got, want)
		}
	}
}

func TestValidNameRoundTrips(t *testing.T) {
	t.Parallel()
	lib, db := newLibrary(t)
	ctx := t.Context()

	saved, err := lib.Save(ctx, "code-review", "How to read a diff", []string{"review", "quality"}, 0, 0)
	if err != nil {
		t.Fatalf("saving a legal name failed: %v", err)
	}
	if saved.ID == 0 {
		t.Error("Save returned id 0; the caller has no handle to fetch or delete the entry")
	}
	if saved.Name != "code-review" {
		t.Errorf("Name = %q, want %q", saved.Name, "code-review")
	}
	if saved.Description != "How to read a diff" {
		t.Errorf("Description = %q; the catalogue lost the text that makes an instruction findable", saved.Description)
	}
	if !slices.Equal(saved.Tags, []string{"review", "quality"}) {
		t.Errorf("Tags = %v, want [review quality]", saved.Tags)
	}
	if saved.Path != "library/code-review.md" {
		t.Errorf("Path = %q, want %q — this is where the caller wrote the text", saved.Path, "library/code-review.md")
	}
	if _, err := time.Parse(time.RFC3339, saved.UpdatedAt); err != nil {
		t.Errorf("UpdatedAt = %q, which the UI cannot render as a time: %v", saved.UpdatedAt, err)
	}

	// Both lookups the API offers must find the same entry: /instructions/{id}
	// after a list, and by-name when a workspace binds an instruction it only
	// knows by name.
	byName, err := lib.GetByName(ctx, "code-review")
	if err != nil {
		t.Fatalf("GetByName could not find the instruction just saved: %v", err)
	}
	if byName.ID != saved.ID || byName.Path != saved.Path || byName.Description != saved.Description {
		t.Errorf("GetByName returned %+v, want the row just saved %+v", byName, saved)
	}
	byID, err := lib.Get(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Get(%d) could not find the instruction just saved: %v", saved.ID, err)
	}
	if byID.Name != "code-review" || !slices.Equal(byID.Tags, []string{"review", "quality"}) {
		t.Errorf("Get returned %+v, want name code-review with tags [review quality]", byID)
	}

	if n := indexed(t, db); n != 1 {
		t.Errorf("the index holds %d rows after one save, want 1", n)
	}
}

// A name arriving with stray whitespace must be stored trimmed AND its path
// must be the trimmed path, or the row points at a file whose name has spaces
// in it and Get returns "no such path" forever.
func TestSaveTrimsTheNameItIndexes(t *testing.T) {
	t.Parallel()
	lib, _ := newLibrary(t)
	ctx := t.Context()

	saved, err := lib.Save(ctx, "  code-review\n", "d", nil, 0, 0)
	if err != nil {
		t.Fatalf("a legal name with surrounding whitespace was rejected: %v", err)
	}
	if saved.Name != "code-review" {
		t.Errorf("Name = %q, want %q", saved.Name, "code-review")
	}
	if saved.Path != "library/code-review.md" {
		t.Errorf("Path = %q, want %q", saved.Path, "library/code-review.md")
	}
	if _, err := lib.GetByName(ctx, "code-review"); err != nil {
		t.Errorf("the trimmed name does not find the entry it created: %v", err)
	}
}

// The API marshals Instruction straight to JSON. Tags must be [] and never
// null, or every client that iterates them breaks on an instruction saved
// without tags.
func TestTagsAreNeverNullInJSON(t *testing.T) {
	t.Parallel()
	lib, _ := newLibrary(t)

	saved, err := lib.Save(t.Context(), "no-tags", "d", nil, 0, 0)
	if err != nil {
		t.Fatalf("saving failed: %v", err)
	}
	if saved.Tags == nil {
		t.Error("Tags is nil in Go, so the JSON body carries null")
	}
	body, err := json.Marshal(saved)
	if err != nil {
		t.Fatalf("marshalling the API response failed: %v", err)
	}
	if !strings.Contains(string(body), `"tags":[]`) {
		t.Errorf("response body is %s, want tags as an empty array", body)
	}
}

// Re-saving is documented as an update: the text is versioned by Contextverse
// at one path, so a second row for the same name would give the catalogue two
// entries pointing at one file.
func TestResavingTheSameNameUpdatesInPlace(t *testing.T) {
	t.Parallel()
	lib, db := newLibrary(t)
	ctx := t.Context()

	first, err := lib.Save(ctx, "code-review", "first take", []string{"draft"}, 0, 0)
	if err != nil {
		t.Fatalf("first save failed: %v", err)
	}
	second, err := lib.Save(ctx, "code-review", "second take", []string{"review", "quality"}, 0, 0)
	if err != nil {
		t.Fatalf("re-saving an existing name failed instead of updating: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("re-save produced id %d, want the existing %d — anything bound to the old id now points at a stale entry", second.ID, first.ID)
	}
	if second.Description != "second take" {
		t.Errorf("Description = %q, want %q — the edit was silently dropped", second.Description, "second take")
	}
	if !slices.Equal(second.Tags, []string{"review", "quality"}) {
		t.Errorf("Tags = %v, want [review quality]", second.Tags)
	}
	if second.Path != "library/code-review.md" {
		t.Errorf("Path = %q; an update must keep pointing at the same file", second.Path)
	}
	if n := indexed(t, db); n != 1 {
		t.Errorf("the index holds %d rows after two saves of one name, want 1", n)
	}
}

func TestListFindsInstructionsByTagAndText(t *testing.T) {
	t.Parallel()
	lib, _ := newLibrary(t)
	ctx := t.Context()

	// Inserted out of alphabetical order so that "sorted" cannot be satisfied
	// by insertion order alone.
	for _, in := range []struct {
		name, desc string
		tags       []string
	}{
		{"gamma-rule", "nothing in particular", nil},
		{"alpha-rule", "about tests", []string{"Testing"}},
		{"beta-rule", "about deploys", []string{"ops"}},
	} {
		if _, err := lib.Save(ctx, in.name, in.desc, in.tags, 0, 0); err != nil {
			t.Fatalf("saving %q failed: %v", in.name, err)
		}
	}

	names := func(t *testing.T, tag, query string) []string {
		t.Helper()
		items, err := lib.List(ctx, tag, query)
		if err != nil {
			t.Fatalf("List(%q, %q) failed: %v", tag, query, err)
		}
		if items == nil {
			t.Fatalf("List(%q, %q) returned nil; the JSON body would be null instead of []", tag, query)
		}
		out := make([]string, 0, len(items))
		for _, in := range items {
			out = append(out, in.Name)
		}
		return out
	}

	// The catalogue is a browsable list; unsorted it reshuffles on every save.
	if got := names(t, "", ""); !slices.Equal(got, []string{"alpha-rule", "beta-rule", "gamma-rule"}) {
		t.Errorf("List() = %v, want the three names in alphabetical order", got)
	}
	// Tag matching is case-insensitive: the tag was saved as "Testing" and the
	// UI sends whatever the operator typed.
	if got := names(t, "testing", ""); !slices.Equal(got, []string{"alpha-rule"}) {
		t.Errorf("List(tag=testing) = %v, want [alpha-rule]", got)
	}
	if got := names(t, "ops", ""); !slices.Equal(got, []string{"beta-rule"}) {
		t.Errorf("List(tag=ops) = %v, want [beta-rule]", got)
	}
	if got := names(t, "", "DEPLOY"); !slices.Equal(got, []string{"beta-rule"}) {
		t.Errorf("List(q=DEPLOY) = %v, want [beta-rule] — search covers the description", got)
	}
	if got := names(t, "", "ALPHA"); !slices.Equal(got, []string{"alpha-rule"}) {
		t.Errorf("List(q=ALPHA) = %v, want [alpha-rule] — search covers the name", got)
	}
	if got := names(t, "ops", "tests"); len(got) != 0 {
		t.Errorf("List(tag=ops, q=tests) = %v, want nothing — the filters must both apply", got)
	}
	if got := names(t, "no-such-tag", ""); len(got) != 0 {
		t.Errorf("List(tag=no-such-tag) = %v, want nothing", got)
	}
}

// Delete drops the catalogue row only; the text stays in Contextverse. And a
// second delete must report ErrNotFound, because that is what the API turns
// into a 404 rather than a 500.
func TestDeleteUnindexesAndReportsUnknownIDs(t *testing.T) {
	t.Parallel()
	lib, db := newLibrary(t)
	ctx := t.Context()

	saved, err := lib.Save(ctx, "code-review", "d", nil, 0, 0)
	if err != nil {
		t.Fatalf("saving failed: %v", err)
	}
	if err := lib.Delete(ctx, saved.ID); err != nil {
		t.Fatalf("deleting the instruction just saved failed: %v", err)
	}
	if n := indexed(t, db); n != 0 {
		t.Errorf("the index still holds %d row(s) after Delete", n)
	}
	if _, err := lib.GetByName(ctx, "code-review"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByName after Delete returned %v, want ErrNotFound", err)
	}
	if err := lib.Delete(ctx, saved.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting an already-deleted instruction returned %v, want ErrNotFound", err)
	}
	if _, err := lib.Get(ctx, 424242); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get on an unknown id returned %v, want ErrNotFound", err)
	}

	// The name is free again afterwards — an unlisted instruction must not
	// block re-publishing under the same name.
	if _, err := lib.Save(ctx, "code-review", "d", nil, 0, 0); err != nil {
		t.Errorf("re-saving a deleted name failed: %v", err)
	}
}
