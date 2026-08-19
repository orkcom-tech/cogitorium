package server

import "testing"

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
		if fallsBackToTheApp(p) {
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
		if !fallsBackToTheApp(p) {
			t.Errorf("%s must deep-link to the application", p)
		}
	}
}

// A client-side path segment can contain a dot, and refusing those would break
// deep links that work today.
func TestAnUnfamiliarExtensionIsTreatedAsARoute(t *testing.T) {
	for _, p := range []string{"/workspaces/my.workspace", "/instructions/notes.v2"} {
		if !fallsBackToTheApp(p) {
			t.Errorf("%s looks like a route with a dot in it and must still deep-link", p)
		}
	}
}

// Plugin space is answered by the plugin router. A miss there is a miss —
// never the application shell wearing a plugin's URL.
func TestPluginSpaceNeverFallsBackToTheApp(t *testing.T) {
	for _, p := range []string{"/p/midnight/guide", "/p/midnight/assets/x.js", "/p/"} {
		if fallsBackToTheApp(p) {
			t.Errorf("%s must not be answered with the application shell", p)
		}
	}
}
