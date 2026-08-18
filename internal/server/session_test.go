package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The browser's credential, and the two properties it is chosen for: script on
// the page cannot read it, and another site cannot spend it.

// claim walks a fresh install through setup and returns the response, which is
// where the first cookie comes from.
func (in *install) claim(t *testing.T, client string) *httptest.ResponseRecorder {
	t.Helper()
	rec := in.postSetup(t, client, `{"password":"correct-horse-battery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("claiming the install: %d %s", rec.Code, rec.Body.String())
	}
	return rec
}

func cookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in the response headers: %v", sessionCookie, rec.Header())
	return nil
}

// TestTheSessionCookieCannotBeReadByScript is the whole reason for preferring a
// cookie to a token in localStorage. Without HttpOnly the two would be equally
// exposed to a cross-site scripting bug and there would be no argument for it.
func TestTheSessionCookieCannotBeReadByScript(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)
	c := cookieFrom(t, in.claim(t, onBox))

	if !c.HttpOnly {
		t.Error("the session cookie is readable by script, which is the one thing it exists to prevent")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite is %v, want Lax — without it the browser attaches this to another site's requests", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("cookie path is %q, want /", c.Path)
	}
	if c.Value == "" {
		t.Error("the cookie carries no token")
	}
}

// TestTheCookieIsRememberedLocallyAndNotOnAServer is "remember me", stated the
// way the browser understands it: an expiry means it survives the browser
// closing, and no expiry means it does not.
func TestTheCookieIsRememberedLocallyAndNotOnAServer(t *testing.T) {
	t.Parallel()

	local := newInstall(t, "127.0.0.1:8688", nil)
	if c := cookieFrom(t, local.claim(t, onBox)); c.MaxAge <= 0 {
		t.Errorf("a local install issued a cookie with MaxAge %d, so the operator would be asked "+
			"to sign in again every launch", c.MaxAge)
	}

	// A server is claimed with the admin token rather than anonymously.
	server := newInstall(t, "0.0.0.0:8688", nil)
	rec := server.postSetup(t, offBox, `{"password":"correct-horse-battery","token":"`+server.adminTok+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("claiming the server: %d %s", rec.Code, rec.Body.String())
	}
	if c := cookieFrom(t, rec); c.MaxAge != 0 {
		t.Errorf("a network-reachable server issued a cookie with MaxAge %d, want 0 so it dies "+
			"with the browser rather than outliving the person at it", c.MaxAge)
	}
}

// TestTheCookieIsSecureOnlyWhereItCanComeBack: Secure means "https only", and
// setting it on the http://127.0.0.1 most installs run on would produce a
// cookie the browser accepts and never returns — signing the operator out with
// a flag meant to protect them.
func TestTheCookieIsSecureOnlyWhereItCanComeBack(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)

	if c := cookieFrom(t, in.claim(t, onBox)); c.Secure {
		t.Error("a plaintext loopback install set Secure, so the browser would never send the cookie back")
	}

	// The same server behind something that terminated TLS in front of it.
	fresh := newInstall(t, "127.0.0.1:8688", nil)
	req := httptest.NewRequest("POST", "/api/v1/setup", strings.NewReader(`{"password":"correct-horse-battery"}`))
	req.RemoteAddr = onBox
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	fresh.srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("claiming behind a TLS terminator: %d %s", rec.Code, rec.Body.String())
	}
	if c := cookieFrom(t, rec); !c.Secure {
		t.Error("a request that arrived over https got a cookie without Secure, so it can leak to a plaintext hop")
	}
}

// ourPage is the Origin a browser states for a request the app's own page
// made. httptest gives a request built from a path the host "example.com", so
// this has to be derived from the request rather than written out — an Origin
// naming the listen address would be cross-site to the request under test and
// every case here would pass for the wrong reason.
func ourPage(r *http.Request) string { return "http://" + r.Host }

// withCookie is a request carrying a session cookie, and an Origin header
// chosen by whether the page that caused it was the app's own. crossSite names
// somebody else's page; empty means the app's.
func (in *install) withCookie(t *testing.T, method, path, cookie, crossSite, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var req *http.Request
	if rdr == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, rdr)
	}
	req.RemoteAddr = onBox
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	if crossSite != "" {
		req.Header.Set("Origin", crossSite)
	} else {
		req.Header.Set("Origin", ourPage(req))
	}
	rec := httptest.NewRecorder()
	in.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// TestACookieIsRefusedOnACrossSiteWrite is the chain behind SameSite's lock.
//
// SameSite=Lax should mean a browser never attaches this cookie to another
// site's write in the first place. This is what happens if it does anyway — an
// older browser, a sibling subdomain SameSite does not separate, or a bug.
func TestACookieIsRefusedOnACrossSiteWrite(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)
	c := cookieFrom(t, in.claim(t, onBox))

	// Reading is allowed with the cookie, from the app's own page.
	rec := in.withCookie(t, "GET", "/api/v1/whoami", c.Value, "", "")
	if rec.Code != http.StatusOK || asMap(t, rec)["name"] != "admin" {
		t.Fatalf("the app's own page was refused: %d %s", rec.Code, rec.Body.String())
	}

	// The same cookie on a write another site caused.
	rec = in.withCookie(t, "POST", "/api/v1/teams", c.Value, "https://evil.example", `{"name":"theirs"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a cross-site write with a session cookie: got %d, want 403\nbody: %s", rec.Code, rec.Body.String())
	}
	// And it did not half-happen.
	teams, err := in.users.ListTeams(t.Context())
	if err != nil {
		t.Fatalf("list teams: %v", err)
	}
	for _, tm := range teams {
		if tm.Name == "theirs" {
			t.Fatal("the cross-site write went through behind a 403")
		}
	}

	// From the app's own page the identical write is fine, so the refusal above
	// is about where it came from and not about the request being malformed.
	rec = in.withCookie(t, "POST", "/api/v1/teams", c.Value, "", `{"name":"ours"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("the app's own write: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}
}

// TestABearerTokenIsNotOriginChecked: a browser cannot attach an Authorization
// header on somebody else's behalf, so there is nothing to forge — and a script
// that sends one has no Origin to state. Checking it would refuse every API
// client that does not invent a header.
func TestABearerTokenIsNotOriginChecked(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)

	req := httptest.NewRequest("POST", "/api/v1/teams", strings.NewReader(`{"name":"scripted"}`))
	req.RemoteAddr = onBox
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+in.adminTok)
	req.Header.Set("Origin", "https://somewhere.else")
	rec := httptest.NewRecorder()
	in.srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("a bearer-authenticated write with an Origin header: got %d, want 201\nbody: %s",
			rec.Code, rec.Body.String())
	}
}

// TestSigningOutTakesTheCookieBack: revoking the token without clearing the
// cookie would leave a browser presenting a credential the server refuses, on
// every request, without ever showing the sign-in card.
func TestSigningOutTakesTheCookieBack(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)
	c := cookieFrom(t, in.claim(t, onBox))

	rec := in.withCookie(t, "POST", "/api/v1/logout", c.Value, "", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("signing out: %d %s", rec.Code, rec.Body.String())
	}
	if gone := cookieFrom(t, rec); gone.MaxAge >= 0 {
		t.Errorf("signing out left the cookie in place (MaxAge %d, want -1)", gone.MaxAge)
	}
	// The token behind it is revoked too, so a copy taken beforehand is dead.
	rec = in.withCookie(t, "GET", "/api/v1/whoami", c.Value, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a signed-out session still resolves: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAStaleCookieIsTakenBack: a database replaced under a browser leaves it
// holding a cookie naming a token nobody issued. Refusing it forever without
// clearing it would wedge that browser out of an install it has the password
// for.
func TestAStaleCookieIsTakenBack(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)
	in.claim(t, onBox)

	rec := in.withCookie(t, "GET", "/api/v1/whoami", "cg-admin-from-some-other-install", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a cookie naming an unknown token: got %d, want 401", rec.Code)
	}
	if gone := cookieFrom(t, rec); gone.MaxAge >= 0 {
		t.Errorf("the unusable cookie was left in the browser (MaxAge %d, want -1)", gone.MaxAge)
	}
}
