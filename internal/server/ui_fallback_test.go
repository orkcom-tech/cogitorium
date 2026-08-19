package server

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Serving index.html at 200 for a missing .js is the worst failure this server
// can produce: the browser hands an HTML document to its module parser and
// reports a syntax error at line 1 of a file that looks fine on disk, pointing
// at the bundle rather than at the thing that is actually missing.
func TestAMissingAssetIsNotAnsweredWithTheApp(t *testing.T) {
	assets := []string{
		"/assets/index-D5NMYaXw.js",
		"/assets/index-Doin25bG.css",
		"/assets/thing.mjs",
		"/module.wasm",
		"/icon-96.png",
		"/manifest.webmanifest",
		"/fonts/x.woff2",
		"/map.js.map",
	}
	for _, p := range assets {
		if servesTheApp(p) {
			t.Errorf("%s would be served the application shell; a missing asset must 404", p)
		}
	}
}

// Deep-linking a client-side route has to keep working, which is why the
// fallback exists at all.
func TestClientRoutesStillFallBack(t *testing.T) {
	routes := []string{
		"/workspaces",
		"/workspaces/3",
		"/gears",
		"/people",
		"/instructions",
	}
	for _, p := range routes {
		if !servesTheApp(p) {
			t.Errorf("%s must deep-link to the application", p)
		}
	}
}

// A workspace id is a segment, and a dot in it is somebody's name rather than
// a file extension. The declared list decides, so nothing has to be inferred
// from how a path is spelled.
func TestAWorkspaceIDIsMatchedAsASegment(t *testing.T) {
	for _, p := range []string{"/workspaces/3", "/workspaces/my.workspace"} {
		if !servesTheApp(p) {
			t.Errorf("%s is a declared route and must deep-link", p)
		}
	}
	// One segment, not many.
	if servesTheApp("/workspaces/3/settings") {
		t.Error("/workspaces/3/settings is not a route this app has")
	}
}

// A path nobody declared is a mistake and must be answered as one. Guessing
// from the shape of a path is what made a typo look like a working page.
func TestAnUndeclaredPathIsNotAnsweredWithTheApp(t *testing.T) {
	for _, p := range []string{"/wrokspaces", "/admin", "/p", "/gears/extra", "/nope"} {
		if servesTheApp(p) {
			t.Errorf("%s is not a screen this app has and must 404", p)
		}
	}
}

// The declared list and the Route elements in web/src/App.tsx are one thing
// said twice, so they are checked against each other rather than trusted.
func TestTheDeclaredRoutesMatchTheApplication(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "App.tsx"))
	if err != nil {
		t.Skipf("the frontend source is not present: %v", err)
	}
	re := regexp.MustCompile(`<Route\s+path="([^"]+)"`)
	declared := map[string]bool{}
	for _, r := range clientRoutes {
		declared[r] = true
	}
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		// react-router writes :id; the server matches any single segment.
		want := regexp.MustCompile(`:[A-Za-z0-9_]+`).ReplaceAllString(m[1], ":")
		if !declared[want] {
			t.Errorf("App.tsx renders %q but the server does not serve it (looked for %q). "+
				"Add it to clientRoutes, or the screen 404s on a reload.", m[1], want)
		}
	}
}

// Plugin space is answered by the plugin router. A miss there is a miss —
// never the application shell wearing a plugin's URL.
func TestPluginSpaceNeverFallsBackToTheApp(t *testing.T) {
	for _, p := range []string{"/p/midnight/guide", "/p/midnight/assets/x.js", "/p/"} {
		if servesTheApp(p) {
			t.Errorf("%s must not be answered with the application shell", p)
		}
	}
}
