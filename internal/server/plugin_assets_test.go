package server

import (
	"fmt"
	"os"
	"path/filepath"
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
