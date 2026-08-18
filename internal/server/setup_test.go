package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// First-run setup is the one place in the API where something can be written
// without a credential, so these tests are about the guards rather than the
// happy path. Each one corresponds to a numbered guard over handleSetup.

// postSetup sends a well-formed setup request, which for these tests means one
// carrying the JSON content type — the absence of it is its own case below.
func (in *install) postSetup(t *testing.T, client, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/setup", strings.NewReader(body))
	req.RemoteAddr = client
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	in.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// TestAFreshLocalInstallIsClaimedByChoosingAPassword is the flow a person
// actually walks through: open the app, be told it needs setting up, pick a
// password, and be signed in with it.
func TestAFreshLocalInstallIsClaimedByChoosingAPassword(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)

	rec := in.requestFrom(t, onBox, "GET", "/api/v1/setup", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("asking whether this install needs setting up: %d %s", rec.Code, rec.Body.String())
	}
	state := asMap(t, rec)
	if state["needs_setup"] != true {
		t.Fatalf("a fresh install does not report needing setup: %v", state)
	}
	if state["local"] != true {
		t.Fatalf("a loopback listen address is not reported as local: %v", state)
	}

	rec = in.postSetup(t, onBox, `{"password":"correct-horse-battery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("claiming a fresh local install: %d %s", rec.Code, rec.Body.String())
	}
	out := asMap(t, rec)
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("setup did not sign the operator in: %v", out)
	}

	// The token works, and it is the admin's.
	rec = in.requestFrom(t, onBox, "GET", "/api/v1/whoami", token, "")
	if rec.Code != http.StatusOK || asMap(t, rec)["name"] != "admin" {
		t.Fatalf("the token setup returned is not the admin's: %d %s", rec.Code, rec.Body.String())
	}
	// And so does the password, which is the part that has to survive a
	// restart: a token minted here but a password that never landed would
	// look identical until the next start.
	rec = in.requestFrom(t, onBox, "POST", "/api/v1/login", "",
		`{"name":"admin","password":"correct-horse-battery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("signing in with the password just set: %d %s", rec.Code, rec.Body.String())
	}

	// Asked again, the install no longer offers to be claimed.
	rec = in.requestFrom(t, onBox, "GET", "/api/v1/setup", "", "")
	if asMap(t, rec)["needs_setup"] != false {
		t.Fatalf("a claimed install still reports needing setup: %s", rec.Body.String())
	}
}

// TestSetupWorksExactlyOnce is guard 1. An endpoint that sets the admin
// password without credentials is a takeover if it stays open, so a second
// call is refused however it is dressed up.
func TestSetupWorksExactlyOnce(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)

	if rec := in.postSetup(t, onBox, `{"password":"correct-horse-battery"}`); rec.Code != http.StatusOK {
		t.Fatalf("first setup: %d %s", rec.Code, rec.Body.String())
	}
	rec := in.postSetup(t, onBox, `{"password":"a-stranger-picks-this"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second setup: got %d, want 409\nbody: %s", rec.Code, rec.Body.String())
	}
	// Refused, and it did not half-happen: the original password still works
	// and the stranger's does not.
	rec = in.requestFrom(t, onBox, "POST", "/api/v1/login", "",
		`{"name":"admin","password":"a-stranger-picks-this"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("the second setup's password was accepted: %d %s", rec.Code, rec.Body.String())
	}
	rec = in.requestFrom(t, onBox, "POST", "/api/v1/login", "",
		`{"name":"admin","password":"correct-horse-battery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("the first password stopped working after a refused setup: %d %s", rec.Code, rec.Body.String())
	}
}

// TestSetupOnAServerNeedsTheAdminToken is guard 2. On a listener the network
// can reach, an anonymous claim is a takeover waiting for a port scan.
func TestSetupOnAServerNeedsTheAdminToken(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "0.0.0.0:8688", nil)

	rec := in.requestFrom(t, offBox, "GET", "/api/v1/setup", "", "")
	if state := asMap(t, rec); state["local"] != false {
		t.Fatalf("a server install reports itself as local: %v", state)
	}

	rec = in.postSetup(t, offBox, `{"password":"a-stranger-picks-this"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous setup on a network-reachable server: got %d, want 401\nbody: %s",
			rec.Code, rec.Body.String())
	}
	rec = in.postSetup(t, offBox, `{"password":"a-stranger-picks-this","token":"cg-admin-not-a-real-token"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("setup with a forged token: got %d, want 401\nbody: %s", rec.Code, rec.Body.String())
	}

	// The operator, who has the token this server printed at startup.
	rec = in.postSetup(t, offBox, `{"password":"correct-horse-battery","token":"`+in.adminTok+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("the operator claiming their own server: %d %s", rec.Code, rec.Body.String())
	}
}

// TestSetupRefusesAFormPost is guard 3, and the reason it is not pedantry: a
// cross-origin form post is a request a browser sends without asking first,
// and this is the only unauthenticated write in the API. Without the content
// type check, any page open in the operator's browser could claim their local
// install by pointing a form at 127.0.0.1.
func TestSetupRefusesAFormPost(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)

	// text/plain is a "simple" content type, so a browser sends it to another
	// origin with no preflight — and it can carry a body that parses as JSON.
	req := httptest.NewRequest("POST", "/api/v1/setup",
		strings.NewReader(`{"password":"a-stranger-picks-this"}`))
	req.RemoteAddr = onBox
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	rec := httptest.NewRecorder()
	in.srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("a form-shaped setup post: got %d, want 415\nbody: %s", rec.Code, rec.Body.String())
	}
	// And the install is still unclaimed, so the operator gets to do it.
	rec = in.requestFrom(t, onBox, "GET", "/api/v1/setup", "", "")
	if asMap(t, rec)["needs_setup"] != true {
		t.Fatalf("the refused post claimed the install anyway: %s", rec.Body.String())
	}
}

// TestSetupEnforcesThePasswordRules: the length floor lives in identity and
// this route must not be a way around it.
func TestSetupRefusesAWeakPassword(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)

	rec := in.postSetup(t, onBox, `{"password":"short"}`)
	if rec.Code < 400 || rec.Code > 499 {
		t.Fatalf("a five-character password: got %d, want a 4xx\nbody: %s", rec.Code, rec.Body.String())
	}
	rec = in.requestFrom(t, onBox, "GET", "/api/v1/setup", "", "")
	if asMap(t, rec)["needs_setup"] != true {
		t.Fatalf("a refused password left the install claimed: %s", rec.Body.String())
	}
}
