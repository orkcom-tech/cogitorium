package server

import "testing"

// The rule this file exists to stop.
//
// openToAnyone's last term exempts every non-/api/ path, because that is how
// the single-page app is served. So any NEW path space added outside /api/ is
// anonymous by construction, with no opt-in and nothing to notice. A plugin
// page reachable by anyone who knew the URL — while its manifest said nothing
// about being open — is the failure this test prevents from ever shipping.
func TestPluginPagesAreNotAnonymousByConstruction(t *testing.T) {
	paths := []string{
		"/p/release-radar/guide",
		"/p/release-radar/",
		"/p/a/b/c",
	}
	for _, p := range paths {
		if openToAnyone(p) {
			t.Errorf("%s is reachable without a credential. Every non-/api/ path is anonymous "+
				"unless it is carved out, and a plugin page must be carved out.", p)
		}
		if got := authFor(p); got != AuthToken {
			t.Errorf("authFor(%s) = %q, want %q", p, got, AuthToken)
		}
	}
}

// The carve-out must not reach past the prefix it owns. A path that merely
// starts with the same letters is somebody else's.
func TestThePluginCarveOutDoesNotOverreach(t *testing.T) {
	stillOpen := []string{"/people", "/pages", "/prod", "/", "/index.html"}
	for _, p := range stillOpen {
		if !openToAnyone(p) {
			t.Errorf("%s stopped being served anonymously; the SPA and its assets depend on that", p)
		}
	}
}

// The exemptions that were there before must be exactly as they were. This is
// the check that a change to one prefix did not quietly move another.
func TestTheExistingExemptionsAreUnchanged(t *testing.T) {
	cases := map[string]string{
		"/health":            AuthNone,
		"/api/v1/login":      AuthNone,
		"/api/v1/setup":      AuthNone,
		"/i/somekey":         AuthInletKey,
		"/i/":                AuthInletKey,
		"/api/v1/workspaces": AuthToken,
		"/index.html":        AuthNone,
	}
	for path, want := range cases {
		if got := authFor(path); got != want {
			t.Errorf("authFor(%s) = %q, want %q", path, got, want)
		}
	}
}

// Delivery is named separately from the catch-all on purpose, and adding a
// carve-out above it must not have changed that.
func TestDeliveryIsStillExemptOnItsOwnTerms(t *testing.T) {
	if !openToAnyone(inletDeliveryPrefix + "key") {
		t.Fatal("inlet delivery must stay reachable without a user token")
	}
	if authFor(inletDeliveryPrefix+"key") != AuthInletKey {
		t.Fatal("delivery must be answered by its own key, not by a user token")
	}
}
