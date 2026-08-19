package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

// The link the catalog exists for: a listing becomes a plugin on disk.
func TestInstallFromCatalogDownloadsAndInstalls(t *testing.T) {
	zipBytes, err := os.ReadFile(bundle(t, "radar", "1.0.0", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	c := NewCatalog(t.TempDir(), nil, func() bool { return true })
	e := stubEntry("radar", srv.URL)
	s := open(t)

	in, digest, err := c.InstallFromCatalog(context.Background(), s, e, "")
	if err != nil {
		t.Fatalf("installing: %v", err)
	}
	if in.Manifest.ID != "radar" || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("installed wrong: %+v %s", in, digest)
	}
	// Being in a catalog is not a decision anybody made about RUNNING it.
	if in.Enabled {
		t.Error("it must arrive switched off")
	}
	if why := s.Pending("radar"); why == "" {
		t.Error("and unapproved — listing it is somebody else's decision than running it")
	}
}

// The catalog and the bundle are written by the same author at different
// times, and a mismatch means one of them is stale.
func TestAMismatchedIDInstallsNothing(t *testing.T) {
	zipBytes, err := os.ReadFile(bundle(t, "actually-midnight", "1.0.0", "x"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	c := NewCatalog(t.TempDir(), nil, func() bool { return true })
	s := open(t)

	_, _, err = c.InstallFromCatalog(context.Background(), s, stubEntry("radar", srv.URL), "")
	if err == nil {
		t.Fatal("a bundle claiming another id must be refused")
	}
	if !strings.Contains(err.Error(), "nothing was installed") {
		t.Errorf("the refusal should say what did not happen: %v", err)
	}
	all, _ := s.List()
	if len(all) != 0 {
		t.Errorf("something was left behind: %+v", all)
	}
}

// "404" sends somebody to look at their own network. The commonest real
// failure is an author who did not attach the asset.
func TestAMissingReleaseAssetSaysWhatIsWrong(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewCatalog(t.TempDir(), nil, func() bool { return true })
	_, err := c.Download(context.Background(), stubEntry("radar", srv.URL), "")
	if err == nil {
		t.Fatal("a missing asset must be an error")
	}
	for _, want := range []string{"radar.zip", "may not have attached"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message omits %q: %v", want, err)
		}
	}
}

func TestDownloadWithoutConsentIsRefused(t *testing.T) {
	c := NewCatalog(t.TempDir(), nil, func() bool { return false })
	_, err := c.Download(context.Background(), entry("radar"), "")
	if err == nil {
		t.Fatal("without consent nothing may be downloaded")
	}
	if !strings.Contains(err.Error(), "releases") {
		t.Errorf("the refusal should name where it would have come from: %v", err)
	}
}

// stubEntry points an entry at a test server by overriding the repo host,
// which is the only part of the conventional URL a test needs to move.
func stubEntry(id, base string) Entry {
	e := entry(id)
	e.bundleBase = base
	return e
}

// The mechanism is who may merge the file, not a signature. Three states,
// because "we read this version", "we read another one" and "nobody looked"
// are three different things to tell somebody deciding whether to approve it.
func TestVerifyHasThreeStates(t *testing.T) {
	idx := Index{VerifiedList: []Verified{
		{ID: "radar", Version: "1.2.0", By: "someone", Note: "reads a feed, writes nothing"},
		{ID: "loose", By: "someone"},
	}}

	got := idx.Verify("radar", "1.2.0")
	if got.State != CheckVerified {
		t.Errorf("the exact version read = %q", got.State)
	}
	if got.By != "someone" || got.Note == "" {
		t.Errorf("who looked and what they said must survive: %+v", got)
	}

	// A badge that survives a version change is a badge about a name rather
	// than about code.
	if got := idx.Verify("radar", "1.4.0"); got.State != CheckOtherVersion {
		t.Errorf("a different version = %q, want %q", got.State, CheckOtherVersion)
	}
	if got.Version != "1.2.0" {
		t.Errorf("and it must say which one was read, got %q", got.Version)
	}

	// The ordinary state, and not an accusation.
	if got := idx.Verify("nobody-looked", "1.0.0"); got.State != CheckUnchecked {
		t.Errorf("unlisted = %q", got.State)
	}

	// An entry with no version is a statement about the plugin rather than
	// about a release, and it applies whatever is installed.
	if got := idx.Verify("loose", "9.9.9"); got.State != CheckVerified {
		t.Errorf("an unversioned entry should apply: %q", got.State)
	}
}

func TestTheVerifiedListTravelsWithTheCatalog(t *testing.T) {
	entries := []Entry{entry("radar")}
	verified := []Verified{{ID: "radar", Version: "1.0.0", By: "team"}}

	mux := http.NewServeMux()
	mux.HandleFunc("/plugins.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(entries)
	})
	mux.HandleFunc("/verified.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(verified)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewCatalog(t.TempDir(), nil, func() bool { return true })
	c.url, c.verifiedURL = srv.URL+"/plugins.json", srv.URL+"/verified.json"

	idx, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.Verify("radar", "1.0.0"); got.State != CheckVerified {
		t.Errorf("the list did not arrive: %+v", got)
	}

	// And it survives into the cache, so an offline install still knows.
	c.allow = func() bool { return false }
	cached, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := cached.Verify("radar", "1.0.0"); got.State != CheckVerified {
		t.Errorf("the cached copy lost it: %+v", got)
	}
}

// A missing verified list is not a failure to browse: every plugin reads as
// unchecked, which is true rather than a guess in either direction.
func TestAMissingVerifiedListLeavesEverythingUnchecked(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/plugins.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Entry{entry("radar")})
	})
	mux.HandleFunc("/verified.json", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewCatalog(t.TempDir(), nil, func() bool { return true })
	c.url, c.verifiedURL = srv.URL+"/plugins.json", srv.URL+"/verified.json"

	idx, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("a missing verified list must not stop a browse: %v", err)
	}
	if len(idx.Entries) != 1 {
		t.Fatalf("entries = %d", len(idx.Entries))
	}
	if got := idx.Verify("radar", "1.0.0"); got.State != CheckUnchecked {
		t.Errorf("without the list everything is unchecked, got %q", got.State)
	}
}
