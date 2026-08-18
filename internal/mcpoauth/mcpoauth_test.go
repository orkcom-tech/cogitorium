package mcpoauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Every test here is about a way a credential goes somewhere it should not.

// ── the challenge ────────────────────────────────────────────────────────

func TestAChallengeIsParsedWithoutTruncatingItsScopes(t *testing.T) {
	// A scope list contains spaces, and the header is comma-separated. A parser
	// that split on commas first would cut this in half.
	ch, ok := ParseChallenge(http.StatusUnauthorized,
		`Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource", scope="files:read files:write"`)
	if !ok {
		t.Fatal("a well-formed challenge was not recognised")
	}
	if ch.ResourceMetadata != "https://mcp.example.com/.well-known/oauth-protected-resource" {
		t.Fatalf("resource_metadata came back %q", ch.ResourceMetadata)
	}
	if ch.Scope != "files:read files:write" {
		t.Fatalf("the scope list was truncated: %q", ch.Scope)
	}
	if ch.Insufficient {
		t.Fatal("a 401 was read as an insufficient-scope error")
	}
}

// A 403 with insufficient_scope is a token that WORKS and does not reach far
// enough. It wants a step-up, not a fresh sign-in, and confusing the two makes
// an operator re-authorize from scratch for one extra permission.
func TestAnInsufficientScopeIsNotAFreshSignIn(t *testing.T) {
	ch, ok := ParseChallenge(http.StatusForbidden,
		`Bearer error="insufficient_scope", scope="files:write", resource_metadata="https://m.example.com/.well-known/oauth-protected-resource"`)
	if !ok || !ch.Insufficient {
		t.Fatalf("a 403 insufficient_scope was not recognised: %+v", ch)
	}
}

func TestAnythingThatIsNotAChallengeIsNotOne(t *testing.T) {
	for _, c := range []struct {
		status int
		header string
	}{
		{http.StatusOK, `Bearer resource_metadata="https://x.example.com/m"`},
		{http.StatusUnauthorized, `Basic realm="x"`},
		{http.StatusUnauthorized, ""},
	} {
		if _, ok := ParseChallenge(c.status, c.header); ok {
			t.Errorf("%d %q was read as a challenge", c.status, c.header)
		}
	}
}

// THE PRIORITY THE SPEC GIVES. The challenge is authoritative for the operation
// that failed, and a client must not assume any relationship between it and
// scopes_supported.
func TestTheChallengeScopeBeatsScopesSupported(t *testing.T) {
	got := chooseScopes(Challenge{Scope: "files:write"}, []string{"files:read"})
	if len(got) != 1 || got[0] != "files:write" {
		t.Fatalf("scopes_supported overrode the challenge: %v", got)
	}
	got = chooseScopes(Challenge{}, []string{"files:read", "issues:read"})
	if len(got) != 2 {
		t.Fatalf("scopes_supported was not used when there was no challenge: %v", got)
	}
	if len(chooseScopes(Challenge{}, nil)) != 0 {
		t.Fatal("a scope parameter was invented where there was nothing to send")
	}
}

// ── RFC 9207, the check that stops a mix-up ──────────────────────────────

func TestIssuerValidationFollowsTheTable(t *testing.T) {
	const want = "https://as.example.com"
	for _, c := range []struct {
		name       string
		got        string
		advertised bool
		ok         bool
	}{
		{"advertised and matching", want, true, true},
		{"advertised and absent is a rejection", "", true, false},
		{"advertised and different", "https://evil.example.com", true, false},
		{"not advertised but present is still compared", "https://evil.example.com", false, false},
		{"not advertised and absent proceeds", "", false, true},
		// No normalisation of any kind: every one of these would let a
		// lookalike compare equal.
		{"a trailing slash is a different issuer", want + "/", true, false},
		{"case folding is not applied", "https://AS.example.com", true, false},
		{"a default port is not elided", "https://as.example.com:443", true, false},
	} {
		err := ValidateIssuer(want, c.got, c.advertised)
		if (err == nil) != c.ok {
			t.Errorf("%s: got err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// ── the flow ─────────────────────────────────────────────────────────────

func TestTheAuthorizationURLCarriesPKCEAndTheResource(t *testing.T) {
	d := Discovered{
		Meta: ServerMetadata{
			Issuer:                "https://as.example.com",
			AuthorizationEndpoint: "https://as.example.com/authorize",
			TokenEndpoint:         "https://as.example.com/token",
		},
		Resource: "https://mcp.example.com/mcp",
		Scopes:   []string{"files:read"},
	}
	s, err := Begin(context.Background(), http.DefaultClient, d,
		"https://cog.example.com/api/v1/mcp-oauth/callback", "client-123", "")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	u, err := url.Parse(s.AuthorizeURL)
	if err != nil {
		t.Fatalf("the authorize url is unparseable: %v", err)
	}
	q := u.Query()

	// S256 only: OAuth 2.1 removed `plain`, and offering it would let a server
	// pick the weaker one.
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method is %q", q.Get("code_challenge_method"))
	}
	// The challenge must actually be the hash of the verifier that was kept.
	sum := sha256.Sum256([]byte(s.Verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("the code challenge is not the hash of the verifier, so the exchange would fail")
	}
	// RFC 8707: what this token is FOR. Without it a token minted for one
	// server can be replayed against another.
	if q.Get("resource") != "https://mcp.example.com/mcp" {
		t.Fatalf("resource is %q", q.Get("resource"))
	}
	if q.Get("state") == "" || q.Get("state") != s.State {
		t.Fatal("state is missing or does not match what was recorded")
	}
	if q.Get("scope") != "files:read" {
		t.Fatalf("scope is %q", q.Get("scope"))
	}
	if q.Get("response_type") != "code" {
		t.Fatalf("response_type is %q", q.Get("response_type"))
	}
}

// Two flows must never share a verifier or a state, which is what makes one
// callback unable to complete another's exchange.
func TestEveryFlowGetsItsOwnSecrets(t *testing.T) {
	d := Discovered{Meta: ServerMetadata{
		Issuer: "https://as.example.com", AuthorizationEndpoint: "https://as.example.com/a",
		TokenEndpoint: "https://as.example.com/t",
	}, Resource: "https://m.example.com"}

	seen := map[string]bool{}
	for range 20 {
		s, err := Begin(context.Background(), http.DefaultClient, d, "https://c.example.com/cb", "id", "")
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if seen[s.State] || seen[s.Verifier] {
			t.Fatal("two flows shared a state or a verifier")
		}
		seen[s.State], seen[s.Verifier] = true, true
	}
}

// The redirect is where the authorization code lands. In the clear, to
// somewhere that is not this machine, it is a code anybody on the path can
// spend.
func TestACleartextRedirectIsRefused(t *testing.T) {
	d := Discovered{Meta: ServerMetadata{
		Issuer: "https://as.example.com", AuthorizationEndpoint: "https://as.example.com/a",
		TokenEndpoint: "https://as.example.com/t",
	}}
	if _, err := Begin(context.Background(), http.DefaultClient, d, "http://cog.example.com/cb", "id", ""); err == nil {
		t.Fatal("a cleartext redirect to another host was accepted")
	}
	// Loopback is the exception, and it has to work: a laptop install has no
	// certificate and the code never leaves the machine.
	if _, err := Begin(context.Background(), http.DefaultClient, d, "http://127.0.0.1:8688/cb", "id", ""); err != nil {
		t.Fatalf("a loopback redirect was refused: %v", err)
	}
}

// The exchange must carry the verifier and the resource, or PKCE and the
// audience binding are decoration.
func TestTheExchangeCarriesTheVerifierAndTheResource(t *testing.T) {
	var form url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "refresh_token": "rt", "expires_in": 3600, "scope": "files:read",
		})
	}))
	t.Cleanup(srv.Close)

	s := Start{
		TokenURL: srv.URL, ClientID: "id", Verifier: "the-verifier",
		Resource: "https://mcp.example.com/mcp", RedirectURI: "https://c.example.com/cb",
	}
	tok, err := Exchange(context.Background(), srv.Client(), s, "the-code")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if form.Get("code_verifier") != "the-verifier" {
		t.Fatalf("code_verifier was %q", form.Get("code_verifier"))
	}
	if form.Get("resource") != "https://mcp.example.com/mcp" {
		t.Fatalf("resource was %q on the token request", form.Get("resource"))
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "the-code" {
		t.Fatalf("the grant was %q / %q", form.Get("grant_type"), form.Get("code"))
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" {
		t.Fatalf("the token did not come back: %+v", tok)
	}
	if tok.ExpiresAt.IsZero() || tok.ExpiresAt.Before(time.Now()) {
		t.Fatalf("expires_at is %v", tok.ExpiresAt)
	}
}

// A refresh that omitted the resource could come back with a token for a
// different audience than the one being refreshed — the same confused deputy in
// a quieter place.
func TestARefreshAlsoCarriesTheResource(t *testing.T) {
	var form url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new"})
	}))
	t.Cleanup(srv.Close)

	if _, err := Refresh(context.Background(), srv.Client(), srv.URL, "id", "", "rt",
		"https://mcp.example.com/mcp", []string{"files:read"}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "rt" {
		t.Fatalf("the refresh was %q / %q", form.Get("grant_type"), form.Get("refresh_token"))
	}
	if form.Get("resource") != "https://mcp.example.com/mcp" {
		t.Fatalf("a refresh omitted the resource: %q", form.Get("resource"))
	}
}

// An authorization server's refusal is reported as its own words rather than as
// a status code, because "invalid_grant" is the thing an operator can act on.
func TestARefusalIsReportedInTheServersOwnWords(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "invalid_grant", "error_description": "the code has been used",
		})
	}))
	t.Cleanup(srv.Close)

	_, err := Exchange(context.Background(), srv.Client(),
		Start{TokenURL: srv.URL, ClientID: "id"}, "code")
	if err == nil {
		t.Fatal("a refused exchange came back as success")
	}
	if !strings.Contains(err.Error(), "invalid_grant") || !strings.Contains(err.Error(), "has been used") {
		t.Fatalf("the refusal lost the reason: %v", err)
	}
}

// A token endpoint reached in the clear would put the exchange on the wire.
func TestACleartextTokenEndpointIsRefused(t *testing.T) {
	_, err := Exchange(context.Background(), http.DefaultClient,
		Start{TokenURL: "http://as.example.com/token"}, "code")
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("a cleartext token endpoint produced %v", err)
	}
}

// ── the step-up ──────────────────────────────────────────────────────────

// THE BUG THIS PREVENTS. A server challenging for one operation names only the
// scopes THAT operation needs. Re-authorizing with just those silently drops
// every permission the token already had, and the next call to anything else
// fails — which reads as the new grant having broken something.
func TestAStepUpKeepsWhatWasAlreadyGranted(t *testing.T) {
	got := StepUpScopes([]string{"files:read", "issues:read"}, Challenge{Scope: "files:write"})
	if len(got) != 3 {
		t.Fatalf("the union dropped something: %v", got)
	}
	for _, want := range []string{"files:read", "issues:read", "files:write"} {
		if !contains(got, want) {
			t.Fatalf("%q is missing from %v", want, got)
		}
	}
	// And it does not duplicate what is already held.
	again := StepUpScopes([]string{"files:read"}, Challenge{Scope: "files:read"})
	if len(again) != 1 {
		t.Fatalf("a repeated scope was duplicated: %v", again)
	}
}

// ── discovery ────────────────────────────────────────────────────────────

// A metadata document that names somebody ELSE as its issuer would have this
// client record a lie as the expected issuer — after which the RFC 9207 check
// on the way back would pass against the wrong party.
func TestAMetadataDocumentCannotClaimAnotherIssuer(t *testing.T) {
	var asURL string
	as := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ServerMetadata{
			Issuer:                "https://someone-else.example.com",
			AuthorizationEndpoint: asURL + "/authorize",
			TokenEndpoint:         asURL + "/token",
		})
	}))
	t.Cleanup(as.Close)
	asURL = as.URL

	prm := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              "https://mcp.example.com/mcp",
			"authorization_servers": []string{as.URL},
		})
	}))
	t.Cleanup(prm.Close)

	// Both test servers are https with a self-signed certificate, so the client
	// has to be one that trusts them; what is under test is the issuer check
	// rather than the TLS.
	_, err := Discover(context.Background(), prm.Client(), "https://mcp.example.com/mcp",
		Challenge{ResourceMetadata: prm.URL})
	if err == nil {
		t.Fatal("a metadata document claiming another issuer was accepted")
	}
}

// Everything in this flow decides where a credential goes, so none of it may be
// fetched in the clear.
func TestDiscoveryRefusesCleartext(t *testing.T) {
	_, err := Discover(context.Background(), http.DefaultClient, "https://mcp.example.com/mcp",
		Challenge{ResourceMetadata: "http://mcp.example.com/.well-known/oauth-protected-resource"})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("cleartext discovery produced %v", err)
	}
}

// A resource that names no authorization server has nothing to sign in to, and
// says so rather than failing somewhere later with less context.
func TestAResourceWithNoAuthorizationServerSaysSo(t *testing.T) {
	prm := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": "https://mcp.example.com/mcp"})
	}))
	t.Cleanup(prm.Close)

	_, err := Discover(context.Background(), prm.Client(), "https://mcp.example.com/mcp",
		Challenge{ResourceMetadata: prm.URL})
	if err == nil || !strings.Contains(err.Error(), "no authorization server") {
		t.Fatalf("got %v", err)
	}
}

// The canonical resource: the document's own word for itself, and no fragment
// or trailing slash when it has to be derived.
func TestTheCanonicalResourceIsWhatRFC8707Wants(t *testing.T) {
	if got := canonicalResource("https://mcp.example.com/mcp", "https://other.example.com"); got != "https://mcp.example.com/mcp" {
		t.Fatalf("the document's own resource was ignored: %q", got)
	}
	for in, want := range map[string]string{
		"https://mcp.example.com/mcp/":         "https://mcp.example.com/mcp",
		"https://mcp.example.com/mcp#fragment": "https://mcp.example.com/mcp",
		"https://mcp.example.com/mcp?a=b":      "https://mcp.example.com/mcp",
	} {
		if got := canonicalResource("", in); got != want {
			t.Errorf("canonicalResource(%q) = %q, want %q", in, got, want)
		}
	}
}

func contains(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}
