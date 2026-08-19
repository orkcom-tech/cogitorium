package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/orkcom-tech/cogitorium/internal/abi"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/store"
	"github.com/orkcom-tech/cogitorium/internal/update"
	"github.com/orkcom-tech/cogitorium/internal/work"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/orkcom-tech/cogitorium/internal/plugin"
	"github.com/orkcom-tech/cogitorium/internal/view"
)

func rt(t *testing.T) (*pluginRuntime, string) {
	t.Helper()
	bundle := t.TempDir()
	for _, name := range []string{"theme.css", "island.js", "NOTES.md"} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &pluginRuntime{assets: map[string]pluginAsset{}, pages: map[string]pluginPage{}}, bundle
}

// A plugin ships whatever its author zipped — notes, sources, whatever else was
// in the folder. Serving the directory would publish all of it because one file
// in it was referenced.
func TestOnlyDeclaredAssetsAreReachable(t *testing.T) {
	r, bundle := rt(t)
	r.declareAsset("midnight", bundle, "theme.css")
	r.declareAsset("midnight", bundle, "island.js")

	if !r.isAsset("/p/midnight/assets/theme.css") {
		t.Error("a declared stylesheet must be reachable")
	}
	if !r.isAsset("/p/midnight/assets/island.js") {
		t.Error("a declared module must be reachable")
	}
	if r.isAsset("/p/midnight/assets/NOTES.md") {
		t.Error("an undeclared file in the same bundle must not be reachable")
	}
	if r.isAsset("/p/midnight/assets/nope.css") {
		t.Error("a file that does not exist must not be reachable")
	}
}

// Containment is decided once, at boot. A check that runs per request is one
// somebody eventually forgets to run.
func TestAnAssetOutsideTheBundleIsNeverServed(t *testing.T) {
	r, bundle := rt(t)
	r.declareAsset("midnight", bundle, "../../etc/passwd")

	for path := range r.assets {
		t.Errorf("an escaping asset was allowlisted: %s", path)
	}
}

// A declared file that is missing is named at boot rather than discovered as a
// stylesheet that quietly never arrives.
func TestADeclaredButAbsentAssetIsNotAllowlisted(t *testing.T) {
	r, bundle := rt(t)
	url := r.declareAsset("midnight", bundle, "ghost.css")
	if r.isAsset(url) {
		t.Error("a file that is not in the bundle must not be served")
	}
}

// Sniffing turns a mislabelled file into whatever its first bytes resemble, and
// a stylesheet served as text/plain is ignored by the browser with no error.
func TestAssetTypesAreDeclaredNotSniffed(t *testing.T) {
	cases := map[string]string{
		"a.css":   "text/css; charset=utf-8",
		"a.js":    "text/javascript; charset=utf-8",
		"a.mjs":   "text/javascript; charset=utf-8",
		"a.svg":   "image/svg+xml",
		"a.woff2": "font/woff2",
		"a.bin":   "application/octet-stream",
	}
	for name, want := range cases {
		if got := assetType(name); got != want {
			t.Errorf("assetType(%q) = %q, want %q", name, got, want)
		}
	}
}

// The URL a plugin's asset gets is inside that plugin's own space, so two
// plugins can never collide over a file name.
func TestAssetURLsAreNamespaced(t *testing.T) {
	a := pluginAssetPath("alfa", "theme.css")
	b := pluginAssetPath("bravo", "theme.css")
	if a == b {
		t.Fatalf("two plugins collided on %s", a)
	}
	for _, u := range []string{a, b} {
		if got := u[:len(pluginPagePrefix)]; got != pluginPagePrefix {
			t.Errorf("%s is outside plugin space", u)
		}
	}
}

// A nil runtime answers false rather than panicking: the middleware asks
// before the server is fully built in some tests, and a crash there would be a
// worse failure than a refusal.
func TestANilRuntimeIsSafeToAsk(t *testing.T) {
	var none *pluginRuntime
	if none.isAsset("/p/x/assets/a.css") {
		t.Error("a nil runtime serves nothing")
	}
	if _, ok := none.pageAuth("/p/x/page"); ok {
		t.Error("a nil runtime declares nothing")
	}
}

// The library screen is built from what the plugins actually did, not from
// what their manifests claimed — so this view cannot be made to say something
// flattering by writing a nicer manifest.
func TestThePluginViewReportsUndeclaredOverrides(t *testing.T) {
	s := &Server{}
	in := plugin.Installed{
		ID:      "midnight",
		Version: "1.0.0",
		Enabled: true,
		Order:   0,
		Manifest: plugin.Manifest{
			ID: "midnight", Name: "Midnight", Version: "1.0.0",
			// Declares one override and quietly performs another.
			Overrides: []string{"cog.shell.tokens"},
		},
	}

	core := fstest.MapFS{"c.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.shell.tokens"}}{{end}}{{define "cog.row.nav"}}{{end}}`)}}
	mine := fstest.MapFS{"m.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.shell.tokens"}}a{{end}}` +
			`{{define "cog.row.nav"}}b{{end}}` +
			`{{define "midnight.page.own"}}c{{end}}` +
			`{{define "cog.slot.head"}}d{{end}}`)}}

	set, report, err := view.Boot(view.Funcs(), core,
		[]view.Source{{ID: "midnight", FS: mine}},
		view.Models{"cog.shell.tokens": view.Shell{}, "cog.row.nav": view.NavItem{},
			"cog.slot.head": view.Shell{}})
	if err != nil {
		t.Fatal(err)
	}
	s.plugins = &pluginRuntime{set: set, report: report}

	v := s.pluginView(in, plugin.Capabilities{})
	if !v.Live {
		t.Fatalf("the plugin should be live: %+v", v)
	}
	if len(v.Undeclared) != 1 || v.Undeclared[0] != "cog.row.nav" {
		t.Errorf("the undeclared override must be reported, got %v", v.Undeclared)
	}
	if len(v.Adds) != 1 || v.Adds[0] != "midnight.page.own" {
		t.Errorf("adds = %v", v.Adds)
	}
	if len(v.Extends) != 1 || v.Extends[0] != "cog.slot.head" {
		t.Errorf("extends = %v", v.Extends)
	}
	if v.Order != 1 {
		t.Errorf("order should be 1-based for a screen, got %d", v.Order)
	}
}

// Enabled and live are different questions, and the difference is the whole
// reason somebody is looking at this screen.
func TestAnEnabledButBrokenPluginIsNotLive(t *testing.T) {
	s := &Server{}
	in := plugin.Installed{
		ID: "bad", Version: "1.0.0", Enabled: true,
		Manifest: plugin.Manifest{ID: "bad", Name: "Bad", Version: "1.0.0"},
	}
	core := fstest.MapFS{"c.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.row.nav"}}{{.Label}}{{end}}`)}}
	bad := fstest.MapFS{"b.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.row.nav"}}{{.Gone}}{{end}}`)}}

	set, report, err := view.Boot(view.Funcs(), core,
		[]view.Source{{ID: "bad", FS: bad}}, view.Models{"cog.row.nav": view.NavItem{}})
	if err != nil {
		t.Fatal(err)
	}
	s.plugins = &pluginRuntime{set: set, report: report}

	v := s.pluginView(in, plugin.Capabilities{})
	if v.Live {
		t.Error("a plugin whose templates cannot render is not live")
	}
	if !v.Enabled {
		t.Error("it is still enabled — that is what makes the difference worth showing")
	}
	if v.Problem == "" {
		t.Error("the screen has to be able to say why")
	}
}

// Nothing is claimed about an install that could not be read.
func TestABrokenInstallClaimsOnlyItsID(t *testing.T) {
	s := &Server{}
	v := s.pluginView(plugin.Installed{ID: "wrecked", Broken: errFake}, plugin.Capabilities{})
	if v.ID != "wrecked" || v.Problem == "" {
		t.Fatalf("expected an id and a reason, got %+v", v)
	}
	if v.Name != "" || v.Version != "" || v.Live {
		t.Errorf("nothing else is reliable for a broken install: %+v", v)
	}
}

var errFake = fmt.Errorf("no installed version recorded")

// nav: was accepted, validated and shown on the plugins page while rendering
// nothing at all. This is the wiring that stops it being an inert field.
func TestNavContributionsReachTheBrowser(t *testing.T) {
	rt := &pluginRuntime{
		nav: []NavItem{
			{Label: "Releases", Href: "/p/radar/guide", Order: 500, When: "always", From: "radar"},
		},
		styles:  []string{"/p/radar/assets/theme.css"},
		scripts: []view.Asset{{Src: "/p/radar/assets/island.js"}},
	}
	c := rt.Contribution()
	if len(c.Nav) != 1 || c.Nav[0].Label != "Releases" {
		t.Fatalf("nav = %+v", c.Nav)
	}
	// Which plugin contributed an entry has to survive, or an operator
	// debugging a rail entry has to go read manifests to find out where it
	// came from.
	if c.Nav[0].From != "radar" {
		t.Errorf("the contributing plugin must be named: %+v", c.Nav[0])
	}
	if len(c.Styles) != 1 || len(c.Scripts) != 1 {
		t.Errorf("styles and scripts must travel too: %+v", c)
	}
}

// A nil runtime is the ordinary case on an install with no plugins, and the
// browser must get empty lists rather than nulls it has to defend against.
func TestAnEmptyContributionIsEmptyNotNull(t *testing.T) {
	var none *pluginRuntime
	c := none.Contribution()
	if c.Nav == nil || c.Styles == nil || c.Scripts == nil {
		t.Errorf("empty lists, never nulls: %+v", c)
	}
	if len(c.Nav) != 0 {
		t.Errorf("nav = %+v", c.Nav)
	}
}

// An author who says 500 means to sit beside the other 500s, not behind
// whichever plugin the operator happened to install first.
func TestNavIsOrderedByWhatAuthorsAskedFor(t *testing.T) {
	rt := &pluginRuntime{nav: []NavItem{
		{Label: "last", Order: 900, From: "a"},
		{Label: "first", Order: 100, From: "b"},
		{Label: "middle", Order: 500, From: "c"},
	}}
	sortNav(rt)
	got := []string{rt.nav[0].Label, rt.nav[1].Label, rt.nav[2].Label}
	want := []string{"first", "middle", "last"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestAPluginThatOnlyMountsAPanelReachesTheDocument covers the early return in
// indexWithPlugins.
//
// The condition listed nav, styles and scripts and was written before mounts
// existed. A plugin whose entire contribution is a workspace panel therefore
// matched "contributes nothing", and the document was served untouched — so
// the panel's button never appeared and no error was raised anywhere, which is
// the worst shape a bug can take on a screen whose job is explaining itself.
func TestAPluginThatOnlyMountsAPanelReachesTheDocument(t *testing.T) {
	c := Contribution{Mounts: []Mount{{
		Point: "workspace.drawer", Title: "Pulse", Page: "/p/pulse/panel", From: "pulse",
	}}}
	if len(c.Nav) != 0 || len(c.Styles) != 0 || len(c.Scripts) != 0 {
		t.Fatal("this test is meaningless unless the mount is the only contribution")
	}
	if contributesNothing(c) {
		t.Fatal("a plugin contributing only a mount was treated as contributing nothing")
	}
}

// An install with no gate must not carry a plugin's request out anyway.
//
// The gate is what writes a row before the socket opens. Going out without one
// would be exactly the unrecorded path it exists to remove — and it would be
// the path used precisely on the installs where nobody is watching.
func TestWithoutTheGateAnOutboundRequestIsRefusedRatherThanMade(t *testing.T) {
	g := newHostGateway(
		map[string]plugin.Grants{"p": mustGrants(t, plugin.Manifest{ID: "p", Hosts: []string{"example.com"}})},
		nil, nil, nil, nil, nil,
	)

	reply := g.Call("p", abi.HostRequest{
		Call:  abi.CallHTTP,
		Input: json.RawMessage(`{"url":"https://example.com/"}`),
	})
	if reply.Err == "" {
		t.Fatal("a request went out on an install with no gate")
	}
	if !strings.Contains(reply.Err, "gate") {
		t.Fatalf("the refusal does not say why: %q", reply.Err)
	}
}

// And the grant is checked before anything else, so the refusal names the
// plugin's own declaration rather than a proxy the author never wrote down.
func TestAnUngrantedHostIsRefusedByName(t *testing.T) {
	g := newHostGateway(
		map[string]plugin.Grants{"p": mustGrants(t, plugin.Manifest{ID: "p", Hosts: []string{"api.github.com"}})},
		nil, nil, nil, nil, nil,
	)

	reply := g.Call("p", abi.HostRequest{
		Call:  abi.CallHTTP,
		Input: json.RawMessage(`{"url":"https://example.com/"}`),
	})
	if !strings.Contains(reply.Err, "example.com") || !strings.Contains(reply.Err, "api.github.com") {
		t.Fatalf("the refusal names neither what was asked for nor what was granted: %q", reply.Err)
	}
}

func mustGrants(t *testing.T, m plugin.Manifest) plugin.Grants {
	t.Helper()
	g, err := plugin.ResolveGrants(m)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// The scope a call needs is derived from its method and subject, not looked up
// in a table — a table mapping every route to a scope would be a second route
// list to keep in step, and the one that drifted would be the one deciding
// what a plugin may do.
func TestTheScopeACallNeedsFollowsFromItsMethodAndSubject(t *testing.T) {
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/api/v1/workspaces", "workspaces:read"},
		{"GET", "/api/v1/workspaces/4/agents", "workspaces:read"},
		{"POST", "/api/v1/workspaces", "workspaces:write"},
		{"DELETE", "/api/v1/gears/2", "gears:write"},
		{"PATCH", "/api/v1/models/9", "models:write"},
	} {
		got, err := apiScope(c.method, c.path)
		if err != nil || got != c.want {
			t.Errorf("%s %s -> %q, %v; want %q", c.method, c.path, got, err, c.want)
		}
	}

	// A method nobody granted a meaning to is refused rather than mapped to
	// the gentler of the two.
	if _, err := apiScope("CONNECT", "/api/v1/workspaces"); err == nil {
		t.Error("CONNECT was given a scope")
	}
}

// A plugin may call the described API and nothing else. Reaching the
// interface's own routes would be using a scope grant to be a browser.
func TestAPluginMayOnlyCallTheDescribedAPI(t *testing.T) {
	g := newHostGateway(
		map[string]plugin.Grants{"p": mustGrants(t, plugin.Manifest{ID: "p", API: []string{"workspaces:read"}})},
		nil, nil, nil, nil, nil,
	)
	for _, path := range []string{"/", "/workspaces", "/p/other/page", "/metrics"} {
		reply := g.Call("p", abi.HostRequest{
			Call:  abi.CallAPI,
			Input: json.RawMessage(`{"method":"GET","path":"` + path + `"}`),
		})
		if reply.Err == "" {
			t.Errorf("a plugin reached %q", path)
		}
	}
}

// The read implied by a write, and nothing wider. A plugin granted
// workspaces:write may read them back without a second line on the approval
// screen; it may not read anything else.
func TestAWriteGrantImpliesTheMatchingReadAndNoOther(t *testing.T) {
	gr := mustGrants(t, plugin.Manifest{ID: "p", API: []string{"workspaces:write"}})
	if err := gr.AllowScope("workspaces:read"); err != nil {
		t.Errorf("a write grant did not imply its own read: %v", err)
	}
	if err := gr.AllowScope("gears:read"); err == nil {
		t.Error("a workspaces grant allowed reading gears")
	}
}

// An idempotency key is scoped to the plugin that chose it.
//
// Two plugins both enqueuing "nightly" is two tasks. Without the scope one of
// them would silently win, and the loser would be a plugin whose background
// work simply never ran — with nothing anywhere to read about why.
func TestAnEnqueueKeyIsScopedToItsPlugin(t *testing.T) {
	db := testDB(t)
	q := work.NewStore(db)
	grants := map[string]plugin.Grants{
		"a": mustGrants(t, plugin.Manifest{ID: "a"}),
		"b": mustGrants(t, plugin.Manifest{ID: "b"}),
	}
	g := newHostGateway(grants, db, nil, nil, nil, q)

	for _, id := range []string{"a", "b"} {
		reply := g.Call(id, abi.HostRequest{
			Call:  abi.CallEnqueue,
			Input: json.RawMessage(`{"export":"sweep","key":"nightly"}`),
		})
		if reply.Err != "" {
			t.Fatalf("%s could not enqueue: %s", id, reply.Err)
		}
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work WHERE kind = 'plugin'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("two plugins with the same key produced %d units, want 2", n)
	}
}

// And the same plugin twice is one, which is what lets a plugin re-enqueue on
// every start without accumulating a task per restart.
func TestOnePluginEnqueuingTwiceWithOneKeyIsOneUnit(t *testing.T) {
	db := testDB(t)
	g := newHostGateway(
		map[string]plugin.Grants{"a": mustGrants(t, plugin.Manifest{ID: "a"})},
		db, nil, nil, nil, work.NewStore(db),
	)
	for i := 0; i < 2; i++ {
		if reply := g.Call("a", abi.HostRequest{
			Call:  abi.CallEnqueue,
			Input: json.RawMessage(`{"export":"sweep","key":"nightly"}`),
		}); reply.Err != "" {
			t.Fatalf("enqueue %d: %s", i, reply.Err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work WHERE kind = 'plugin'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("one key produced %d units, want 1", n)
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Restart is admin-only, like every other route that changes what runs.
//
// It replaces the process image, so an ordinary member being able to call it
// would be an ordinary member being able to interrupt everybody's work.
func TestRestartIsRefusedToANonAdmin(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/restart", nil)
	req = req.WithContext(withCaller(req.Context(), identity.User{
		Name: "someone", Role: identity.RoleMember,
	}))

	s.handleRestart(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("a member restarted the server: %d %s", rec.Code, rec.Body)
	}
}

// The catalog HTTP surface, end to end, against a catalog served over HTTP.
//
// Every piece of this was tested in internal/plugin and none of it through the
// routes a client actually calls — which is where the id crosscheck, the
// verified computation and the paging all meet, and where a mistake shows up
// as a screen that is subtly wrong rather than a failing unit test.
func TestBrowsingTheCatalogOverTheAPI(t *testing.T) {
	entries := []map[string]string{}
	for i := 0; i < 7; i++ {
		entries = append(entries, map[string]string{
			"id": fmt.Sprintf("plugin-%02d", i), "name": fmt.Sprintf("Plugin %02d", i),
			"author": "someone", "description": "an entry", "repo": "someone/thing",
		})
	}
	catalogJSON, _ := json.Marshal(entries)
	verifiedJSON, _ := json.Marshal([]map[string]string{
		{"id": "plugin-03", "version": "9.9.9", "by": "eduard", "note": "read it"},
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "verified.json"):
			w.Write(verifiedJSON)
		default:
			w.Write(catalogJSON)
		}
	}))
	defer upstream.Close()

	s := newCatalogTestServer(t, upstream.URL)

	// A page, and the total behind it. Without the total a full page and a
	// last page look identical.
	var listing catalogView
	getJSON(t, s, "/api/v1/plugin-catalog?limit=3", &listing)
	if listing.Total != 7 {
		t.Fatalf("total is %d, want 7", listing.Total)
	}
	if len(listing.Entries) != 3 {
		t.Fatalf("a limit of 3 returned %d entries", len(listing.Entries))
	}

	getJSON(t, s, "/api/v1/plugin-catalog?limit=3&offset=6", &listing)
	if len(listing.Entries) != 1 {
		t.Fatalf("the last page has %d entries, want 1", len(listing.Entries))
	}

	// Search narrows the whole catalog, not the page somebody happens to be on.
	getJSON(t, s, "/api/v1/plugin-catalog?q=plugin-05", &listing)
	if listing.Total != 1 || listing.Entries[0].ID != "plugin-05" {
		t.Fatalf("search matched %d: %+v", listing.Total, listing.Entries)
	}

	// The verified list is consulted, and the state is about code rather than
	// about a name. Nothing here is installed, so there is no version to
	// disagree with and the answer is what the team read — the three-state
	// distinction bites once a version IS installed, which internal/plugin
	// covers.
	getJSON(t, s, "/api/v1/plugin-catalog?q=plugin-03", &listing)
	if listing.Entries[0].Verified == "unchecked" {
		t.Fatal("the verified list was not consulted")
	}
	if listing.Entries[0].VerifiedBy != "eduard" {
		t.Fatalf("who read it did not travel: %+v", listing.Entries[0])
	}

	// And a plugin nobody looked at reads unchecked, which is the ordinary
	// state and not an accusation.
	getJSON(t, s, "/api/v1/plugin-catalog?q=plugin-04", &listing)
	if got := listing.Entries[0].Verified; got != "unchecked" {
		t.Fatalf("an unread plugin reads %q", got)
	}
}

// newCatalogTestServer points the catalog fetch at a server this test controls.
//
// The catalog's URL is a compiled-in constant and must stay one — a catalog
// somebody can repoint is a catalog somebody can repoint — so the seam is the
// HTTP client rather than the address.
func newCatalogTestServer(t *testing.T, upstream string) *Server {
	t.Helper()
	base, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		dataDir:       t.TempDir(),
		updates:       update.New(update.ModeOn, "test", nil),
		catalogClient: &http.Client{Transport: rewriteHost{base: base}},
	}
}

type rewriteHost struct{ base *url.URL }

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host = r.base.Scheme, r.base.Host
	return http.DefaultTransport.RoundTrip(out)
}

func getJSON(t *testing.T, s *Server, path string, into any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(withCaller(req.Context(), identity.User{
		Name: "admin", Role: identity.RoleAdmin,
	}))
	s.handleBrowseCatalog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s -> %d %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
