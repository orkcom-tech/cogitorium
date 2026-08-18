package mcpoauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The authorization code flow, with the four checks the package comment names.

// Start is everything decided before the operator's browser is sent anywhere.
type Start struct {
	// AuthorizeURL is where to send them.
	AuthorizeURL string
	// State ties the callback back to this record. Generated with a CSPRNG and
	// consumed exactly once.
	State string
	// Verifier is the PKCE secret. Sealed before it is stored: whoever holds it
	// and the code can complete the exchange, which makes it a credential for
	// the length of the flow.
	Verifier string

	Issuer        string
	AuthorizeBase string
	TokenURL      string
	ClientID      string
	ClientSecret  string
	Scopes        []string
	Resource      string
	RedirectURI   string
	IssAdvertised bool
}

// Begin turns a discovery into an authorization URL.
//
// Registration happens here, when it has to: an authorization server this
// install has never met has no client id for it, and dynamic registration is
// the only way to get one without a human filling in a form on somebody else's
// website. A configured client id is used as-is when there is one.
func Begin(ctx context.Context, client *http.Client, d Discovered, redirectURI, clientID, clientSecret string) (Start, error) {
	if !strings.HasPrefix(redirectURI, "https://") && !isLoopback(redirectURI) {
		// The redirect is where the authorization code lands. In the clear, to
		// somewhere that is not this machine, it is a code anybody on the path
		// can spend.
		return Start{}, fmt.Errorf("the redirect %q must be https, or loopback for a local install; "+
			"set public_url to how this install is actually reached", redirectURI)
	}
	if clientID == "" {
		var err error
		clientID, clientSecret, err = register(ctx, client, d.Meta, redirectURI)
		if err != nil {
			return Start{}, err
		}
	}

	verifier, err := randomURLSafe(64)
	if err != nil {
		return Start{}, err
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return Start{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	// S256 only. OAuth 2.1 removed `plain`, and a client that offered it would
	// let a server choose the weaker one.
	q.Set("code_challenge_method", "S256")
	// MUST be sent whether or not the authorization server supports it. It is
	// what binds the token to one MCP server, and a server that ignores it
	// costs nothing while one that honours it prevents a replay.
	q.Set("resource", d.Resource)
	if len(d.Scopes) > 0 {
		q.Set("scope", strings.Join(d.Scopes, " "))
	}

	sep := "?"
	if strings.Contains(d.Meta.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return Start{
		AuthorizeURL:  d.Meta.AuthorizationEndpoint + sep + q.Encode(),
		State:         state,
		Verifier:      verifier,
		Issuer:        d.Meta.Issuer,
		AuthorizeBase: d.Meta.AuthorizationEndpoint,
		TokenURL:      d.Meta.TokenEndpoint,
		ClientID:      clientID,
		ClientSecret:  clientSecret,
		Scopes:        d.Scopes,
		Resource:      d.Resource,
		RedirectURI:   redirectURI,
		IssAdvertised: d.Meta.IssuerParameterSupported,
	}, nil
}

// ValidateIssuer is RFC 9207 section 2.4, as the specification's table states
// it.
//
// The table, and the reason each row is what it is:
//
//	advertised + present  -> compare, exactly, no normalisation
//	advertised + absent   -> REJECT: the server said it would send one
//	not advertised + present -> compare anyway. Local policy, because servers
//	                            emit `iss` before updating their metadata
//	not advertised + absent  -> proceed
//
// Simple string comparison: no scheme or host case folding, no default-port
// elision, no trailing-slash or percent-encoding normalisation. Every one of
// those would let a lookalike issuer compare equal.
func ValidateIssuer(expected, got string, advertised bool) error {
	switch {
	case got == "" && advertised:
		return fmt.Errorf("the authorization server said it sends `iss` and did not, so this response is not trusted")
	case got == "":
		return nil
	case got != expected:
		return fmt.Errorf("this response says it came from %q and the flow was started with %q", got, expected)
	default:
		return nil
	}
}

// Token is what an authorization server issued.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
}

// Exchange turns an authorization code into a token.
func Exchange(ctx context.Context, client *http.Client, s Start, code string) (Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.RedirectURI)
	form.Set("client_id", s.ClientID)
	form.Set("code_verifier", s.Verifier)
	// On the token request as well as the authorization request. The spec says
	// both, and a server that binds the audience reads it here.
	form.Set("resource", s.Resource)
	return postToken(ctx, client, s.TokenURL, s.ClientSecret, form)
}

// Refresh trades a refresh token for a new access token.
//
// The resource parameter goes on this request too: a refresh that omitted it
// could come back with a token for a different audience than the one being
// refreshed, which is the same confused deputy in a quieter place.
func Refresh(ctx context.Context, client *http.Client, tokenURL, clientID, clientSecret, refreshToken, resource string, scopes []string) (Token, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	form.Set("resource", resource)
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}
	return postToken(ctx, client, tokenURL, clientSecret, form)
}

type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func postToken(ctx context.Context, client *http.Client, tokenURL, clientSecret string, form url.Values) (Token, error) {
	if !strings.HasPrefix(tokenURL, "https://") {
		return Token{}, fmt.Errorf("the token endpoint %q is not https", tokenURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cogitorium")
	if clientSecret != "" {
		// In the header rather than the body: a secret in a form field ends up
		// in more logs than one in an Authorization header, and basic is what
		// every authorization server accepts.
		req.SetBasicAuth(url.QueryEscape(form.Get("client_id")), url.QueryEscape(clientSecret))
	}

	res, err := client.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("reaching the token endpoint: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxMetadata))
	if err != nil {
		return Token{}, err
	}
	var ans tokenAnswer
	if err := json.Unmarshal(raw, &ans); err != nil {
		return Token{}, fmt.Errorf("the token endpoint answered something this server could not read (%s)", res.Status)
	}
	if ans.Error != "" {
		return Token{}, fmt.Errorf("the authorization server refused: %s%s", ans.Error, describe(ans.ErrorDesc))
	}
	if res.StatusCode != http.StatusOK || ans.AccessToken == "" {
		return Token{}, fmt.Errorf("the token endpoint answered %s with no access token", res.Status)
	}

	t := Token{AccessToken: ans.AccessToken, RefreshToken: ans.RefreshToken}
	if ans.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().UTC().Add(time.Duration(ans.ExpiresIn) * time.Second)
	}
	if ans.Scope != "" {
		t.Scopes = strings.Fields(ans.Scope)
	}
	return t, nil
}

func describe(s string) string {
	if s == "" {
		return ""
	}
	return " — " + s
}

// register is RFC 7591 dynamic client registration.
//
// Deprecated by the specification in favour of Client ID Metadata Documents,
// and implemented anyway, because it is what deployed authorization servers
// support today. It is the only way an install that has never met a server can
// obtain a client id without a human filling in a form on somebody's website.
func register(ctx context.Context, client *http.Client, meta ServerMetadata, redirectURI string) (string, string, error) {
	if meta.RegistrationEndpoint == "" {
		return "", "", fmt.Errorf("%s does not offer dynamic client registration, so this install needs a "+
			"client id registered with it by hand", meta.Issuer)
	}
	body, err := json.Marshal(map[string]any{
		"client_name":    "Cogitorium",
		"redirect_uris":  []string{redirectURI},
		"grant_types":    []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"},
		// public: this install cannot keep a secret that every copy of the
		// product would share, and PKCE is what OAuth 2.1 puts in its place.
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cogitorium")

	res, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("registering with %s: %w", meta.Issuer, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxMetadata))
	if err != nil {
		return "", "", err
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Error != "" {
		return "", "", fmt.Errorf("%s refused the registration: %s%s", meta.Issuer, out.Error, describe(out.ErrorDesc))
	}
	if out.ClientID == "" {
		return "", "", fmt.Errorf("%s answered the registration with no client id (%s)", meta.Issuer, res.Status)
	}
	return out.ClientID, out.ClientSecret, nil
}

// StepUpScopes is the union of what is held and what a challenge demands.
//
// A union rather than a replacement, and this is the bug it exists to prevent:
// a server challenging for one operation names only the scopes THAT operation
// needs, so re-authorizing with just those silently drops every permission the
// token already had, and the next call to anything else fails.
func StepUpScopes(held []string, ch Challenge) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, held...), strings.Fields(ch.Scope)...) {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("this server has no usable source of randomness: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func isLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}
