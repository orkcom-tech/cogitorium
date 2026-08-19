package server

import (
	"os"
	"path/filepath"
	"testing"

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

var _ = view.Asset{}
