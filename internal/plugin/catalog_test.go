package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func entry(id string) Entry {
	return Entry{ID: id, Name: strings.ToUpper(id), Author: "someone",
		Description: "does a thing", Repo: "someone/cogitorium-" + id}
}

func serving(t *testing.T, body any, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func catalogAt(t *testing.T, url string, allow bool) *Catalog {
	t.Helper()
	c := NewCatalog(t.TempDir(), nil, func() bool { return allow })
	c.url = url
	return c
}

func TestFetchReadsThePublishedList(t *testing.T) {
	srv := serving(t, []Entry{entry("radar"), entry("midnight")}, 200)
	idx, err := catalogAt(t, srv.URL, true).Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetching: %v", err)
	}
	if len(idx.Entries) != 2 {
		t.Fatalf("entries = %d", len(idx.Entries))
	}
	if idx.Cached {
		t.Error("a fresh fetch is not cached")
	}
}

// A browse is a network call and it asks nobody's permission twice — the same
// gate the update check and the MCP registry already answer to.
func TestWithoutConsentNothingIsFetched(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode([]Entry{entry("radar")})
	}))
	defer srv.Close()

	_, err := catalogAt(t, srv.URL, false).Fetch(context.Background())
	if err == nil {
		t.Fatal("without consent and without a cache there is nothing to show")
	}
	if hits != 0 {
		t.Error("it reached the network anyway")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("the refusal should name where it would have come from: %v", err)
	}
}

// A cached list is not a current one, and pretending otherwise is how somebody
// installs a version that was yanked yesterday.
func TestTheCachedCopySaysSo(t *testing.T) {
	srv := serving(t, []Entry{entry("radar")}, 200)
	c := catalogAt(t, srv.URL, true)
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Now offline, by consent being withdrawn.
	c.allow = func() bool { return false }
	idx, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("a cached list should still be browsable: %v", err)
	}
	if !idx.Cached {
		t.Error("it must say it came off disk")
	}
	if idx.Fetched.IsZero() {
		t.Error("and when it was taken")
	}
	if len(idx.Entries) != 1 {
		t.Errorf("entries = %d", len(idx.Entries))
	}
}

func TestAnUnreachableCatalogFallsBackToTheCache(t *testing.T) {
	srv := serving(t, []Entry{entry("radar")}, 200)
	c := catalogAt(t, srv.URL, true)
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.Close()

	idx, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("an unreachable catalog should fall back: %v", err)
	}
	if !idx.Cached || len(idx.Entries) != 1 {
		t.Errorf("the cache should have answered: %+v", idx)
	}
}

// One bad row somebody merged must not take the other nine hundred with it,
// and a field an older client does not know must not stop it browsing.
func TestBadRowsAreDroppedNotFatal(t *testing.T) {
	good := entry("radar")
	rows := []map[string]any{
		{"id": good.ID, "name": good.Name, "author": good.Author,
			"description": good.Description, "repo": good.Repo, "unknown_field": "from the future"},
		{"id": "X", "name": "bad id", "author": "a", "description": "d", "repo": "a/b"},
		{"id": "noRepo", "name": "n", "author": "a", "description": "d", "repo": "not a repo"},
		{"id": good.ID, "name": "duplicate", "author": "a", "description": "d", "repo": "a/b"},
	}
	srv := serving(t, rows, 200)

	idx, err := catalogAt(t, srv.URL, true).Fetch(context.Background())
	if err != nil {
		t.Fatalf("a list with bad rows must still be browsable: %v", err)
	}
	if len(idx.Entries) != 1 || idx.Entries[0].ID != "radar" {
		t.Errorf("expected only the good row, got %+v", idx.Entries)
	}
	if idx.Entries[0].Name != "RADAR" {
		t.Errorf("an unknown field must not stop the known ones being read: %+v", idx.Entries[0])
	}
}

// A client that trusted the shape of what it downloaded would be trusting the
// submission process to have run.
func TestEntryValidation(t *testing.T) {
	ok := entry("radar")
	if err := ok.Validate(); err != nil {
		t.Errorf("a good entry: %v", err)
	}
	bad := map[string]Entry{
		"empty id":     {Name: "n", Author: "a", Description: "d", Repo: "a/b"},
		"reserved id":  {ID: "cog", Name: "n", Author: "a", Description: "d", Repo: "a/b"},
		"no name":      {ID: "radar", Author: "a", Description: "d", Repo: "a/b"},
		"no author":    {ID: "radar", Name: "n", Description: "d", Repo: "a/b"},
		"no descr":     {ID: "radar", Name: "n", Author: "a", Repo: "a/b"},
		"bad repo":     {ID: "radar", Name: "n", Author: "a", Description: "d", Repo: "https://x"},
		"repo w/ path": {ID: "radar", Name: "n", Author: "a", Description: "d", Repo: "a/b/c"},
	}
	for why, e := range bad {
		if err := e.Validate(); err == nil {
			t.Errorf("%s should be refused", why)
		}
	}
}

// Built by convention rather than by asking GitHub's API, which needs a token
// to be useful at any volume and would make browsing depend on a service being
// up to answer a question the URL already answers.
func TestBundleURLsAreConventional(t *testing.T) {
	e := entry("radar")
	latest := e.BundleURL("")
	if !strings.Contains(latest, "someone/cogitorium-radar") || !strings.HasSuffix(latest, "/radar.zip") {
		t.Errorf("latest = %s", latest)
	}
	if !strings.Contains(latest, "/releases/latest/download/") {
		t.Errorf("latest should use the moving release URL: %s", latest)
	}
	pinned := e.BundleURL("1.2.0")
	if !strings.Contains(pinned, "/releases/download/1.2.0/") {
		t.Errorf("a version should pin the release: %s", pinned)
	}
	if e.SourceURL() != "https://github.com/someone/cogitorium-radar" {
		t.Errorf("source = %s", e.SourceURL())
	}
}

func TestSearchMatchesWhatSomebodyTypes(t *testing.T) {
	idx := Index{Entries: []Entry{
		{ID: "radar", Name: "Release Radar", Author: "alfa", Description: "watches releases"},
		{ID: "midnight", Name: "Midnight", Author: "bravo", Description: "a dark theme"},
	}}
	for q, want := range map[string]string{
		"release": "radar", "alfa": "radar", "dark": "midnight", "MIDNIGHT": "midnight",
	} {
		got := idx.Search(q)
		if len(got) != 1 || got[0].ID != want {
			t.Errorf("search(%q) = %+v, want %s", q, got, want)
		}
	}
	if len(idx.Search("")) != 2 {
		t.Error("an empty query lists everything")
	}
	if len(idx.Search("nothing matches this")) != 0 {
		t.Error("a query that matches nothing returns nothing")
	}
}
