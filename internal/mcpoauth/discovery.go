// Package mcpoauth signs this install in to a remote MCP server, so an operator
// picks one from the library and presses connect instead of finding a token.
//
// # Why this is the one place a live credential is stored
//
// Every other credential here is a NAME — a gear's env_names, an MCP server's
// header_names — resolved at the moment of use from a store the operator filled
// in. That works because the operator HAS the value. OAuth is the case where
// they do not and cannot: the token is minted by somebody else's authorization
// server, arrives through a browser redirect, expires, and is replaced by a
// refresh this server performs on its own. There is no name to resolve.
//
// So the tokens are sealed with the same AEAD and the same key the secrets
// table uses, and an install with no COGITORIUM_SECRET_KEY is REFUSED the flow
// rather than quietly writing somebody's refresh token to disk in the clear.
//
// # The four checks that make this safe, none of which is optional
//
//   - PKCE (S256) on every flow. OAuth 2.1 requires it, and without it an
//     authorization code intercepted on the way back is a token.
//   - The `resource` parameter (RFC 8707) on both the authorization request and
//     the token request, naming the MCP server this token is FOR. It is what
//     stops a token minted for one server being replayed against another — the
//     confused deputy this parameter exists for.
//   - The `iss` of the callback validated against the issuer recorded BEFORE
//     the redirect (RFC 9207), from the validated metadata document rather than
//     from anything the callback carried. An expected issuer taken from an
//     unvalidated source protects against nothing.
//   - `state`, generated with a CSPRNG and consumed exactly once, which is the
//     only thing tying a callback to the request that began it.
package mcpoauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A discovery request is bounded twice: one request, and the body it returns.
const (
	discoveryTimeout = 15 * time.Second
	maxMetadata      = 1 << 20
)

// Challenge is what a protected MCP server said when it refused.
//
// A 401 carries `WWW-Authenticate: Bearer resource_metadata="…", scope="…"`,
// and both halves matter: the first says where to look, the second says what to
// ask for. A 403 with `error="insufficient_scope"` carries the same shape and
// means something different — a token that is real but not enough.
type Challenge struct {
	// ResourceMetadata is the URL of the protected resource metadata document.
	ResourceMetadata string
	// Scope is what the server says this operation needs. AUTHORITATIVE for the
	// current request: the spec says a client must not assume any relationship
	// between it and scopes_supported.
	Scope string
	// Insufficient is a 403 rather than a 401 — the token works and does not
	// reach far enough, which is a step-up rather than a fresh sign-in.
	Insufficient bool
}

// ParseChallenge reads a WWW-Authenticate header.
//
// Hand-parsed rather than regexped: the header is a comma-separated list of
// key="value" after a scheme, values may contain commas inside their quotes,
// and a parser that split on commas first would truncate a scope list.
func ParseChallenge(status int, header string) (Challenge, bool) {
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return Challenge{}, false
	}
	rest := strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(rest), "bearer") {
		return Challenge{}, false
	}
	rest = strings.TrimSpace(rest[len("bearer"):])

	c := Challenge{Insufficient: status == http.StatusForbidden}
	for len(rest) > 0 {
		eq := strings.Index(rest, "=")
		if eq < 0 {
			break
		}
		key := strings.ToLower(strings.Trim(strings.TrimSpace(rest[:eq]), ","))
		rest = strings.TrimSpace(rest[eq+1:])

		var value string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				break
			}
			value, rest = rest[1:1+end], strings.TrimSpace(rest[end+2:])
		} else {
			end := strings.Index(rest, ",")
			if end < 0 {
				value, rest = rest, ""
			} else {
				value, rest = rest[:end], rest[end:]
			}
		}
		rest = strings.TrimPrefix(strings.TrimSpace(rest), ",")

		switch key {
		case "resource_metadata":
			c.ResourceMetadata = value
		case "scope":
			c.Scope = value
		case "error":
			c.Insufficient = c.Insufficient || value == "insufficient_scope"
		}
	}
	return c, c.ResourceMetadata != "" || c.Scope != ""
}

// resourceMetadata is RFC 9728: what a protected resource says about itself.
type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// ServerMetadata is what an authorization server says about itself, from either
// RFC 8414 or OpenID Connect Discovery. A client must support both, and they
// carry the same fields under the same names for everything used here.
type ServerMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
	// IssuerParameterSupported decides what an ABSENT `iss` on the callback
	// means: advertised and missing is a rejection, not advertised and missing
	// is fine. See the table in the specification.
	IssuerParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
	GrantTypesSupported      []string `json:"grant_types_supported"`
	CodeChallengeMethods     []string `json:"code_challenge_methods_supported"`
}

// Discovered is everything a flow needs, gathered before anything is sent to a
// browser.
type Discovered struct {
	Meta ServerMetadata
	// Resource is the canonical URI this token will be minted for, which the
	// resource metadata document names. Preferred over a URI derived from the
	// server's own address: the document is the resource's own statement of
	// what it is called, and RFC 8707 wants exactly that string.
	Resource string
	// Scopes is what to ask for, chosen by the specification's own priority:
	// the challenge first, because it is authoritative for this operation, then
	// scopes_supported, then nothing at all.
	Scopes []string
}

// Discover walks from a refusal to everything needed to start a flow.
func Discover(ctx context.Context, client *http.Client, serverURL string, ch Challenge) (Discovered, error) {
	metaURL := ch.ResourceMetadata
	if metaURL == "" {
		// A server that refused without saying where to look. The well-known
		// location is the fallback the RFC defines, built from the MCP server's
		// own URL.
		guess, err := wellKnownResource(serverURL)
		if err != nil {
			return Discovered{}, err
		}
		metaURL = guess
	}
	var prm resourceMetadata
	if err := fetchJSON(ctx, client, metaURL, &prm); err != nil {
		return Discovered{}, fmt.Errorf("could not read the protected resource metadata at %s: %w", metaURL, err)
	}
	if len(prm.AuthorizationServers) == 0 {
		return Discovered{}, fmt.Errorf("%s names no authorization server, so there is nothing to sign in to", metaURL)
	}

	// The first that answers. A resource may list several; the specification
	// leaves the choice to the client, and trying them in order is the honest
	// reading of "the client determines the AS to use".
	var lastErr error
	for _, as := range prm.AuthorizationServers {
		meta, err := serverMetadata(ctx, client, as)
		if err != nil {
			lastErr = err
			continue
		}
		// The issuer must be the one that was asked, or a metadata document
		// could name somebody else's authorization server and this client would
		// record a lie as the expected issuer.
		if !sameIssuer(meta.Issuer, as) {
			lastErr = fmt.Errorf("%s returned metadata for issuer %q, which is not itself", as, meta.Issuer)
			continue
		}
		if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
			lastErr = fmt.Errorf("%s does not name both an authorization and a token endpoint", as)
			continue
		}
		return Discovered{
			Meta:     meta,
			Resource: canonicalResource(prm.Resource, serverURL),
			Scopes:   chooseScopes(ch, prm.ScopesSupported),
		}, nil
	}
	return Discovered{}, fmt.Errorf("none of the authorization servers %s names could be used: %w",
		metaURL, lastErr)
}

// chooseScopes is the specification's priority, in order.
//
// The challenge wins because it is authoritative for the operation that just
// failed; a client must not assume any relationship between it and
// scopes_supported. Absent a challenge, scopes_supported is the minimal set for
// basic functionality. Absent both, the parameter is omitted entirely rather
// than sent empty.
func chooseScopes(ch Challenge, supported []string) []string {
	if s := strings.Fields(ch.Scope); len(s) > 0 {
		return s
	}
	return supported
}

// serverMetadata tries both discovery documents, in the order the specification
// gives, because a client must support each and servers implement one or the
// other.
func serverMetadata(ctx context.Context, client *http.Client, issuer string) (ServerMetadata, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" {
		// Cleartext to an authorization server would put an authorization code
		// on the wire in the open.
		return ServerMetadata{}, fmt.Errorf("%q is not an https authorization server", issuer)
	}
	path := strings.TrimSuffix(u.Path, "/")
	// RFC 8414 inserts the well-known segment BEFORE any path the issuer has,
	// which is the part everybody gets wrong: an issuer of
	// https://host/tenant1 discovers at https://host/.well-known/...  /tenant1,
	// not at https://host/tenant1/.well-known/...
	candidates := []string{
		u.Scheme + "://" + u.Host + "/.well-known/oauth-authorization-server" + path,
		u.Scheme + "://" + u.Host + "/.well-known/openid-configuration" + path,
		// And the OIDC form that appends instead, which deployed servers use.
		strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration",
	}
	var lastErr error
	for _, c := range candidates {
		var meta ServerMetadata
		if err := fetchJSON(ctx, client, c, &meta); err != nil {
			lastErr = err
			continue
		}
		if meta.Issuer == "" {
			lastErr = fmt.Errorf("%s returned a metadata document with no issuer", c)
			continue
		}
		return meta, nil
	}
	return ServerMetadata{}, lastErr
}

// sameIssuer compares by the RFC's rule: simple string comparison, with only a
// trailing slash forgiven because it is the one difference deployed servers
// disagree about harmlessly.
func sameIssuer(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// wellKnownResource is where a protected resource's metadata lives when the
// server did not say.
func wellKnownResource(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a usable MCP server URL", serverURL)
	}
	path := strings.TrimSuffix(u.Path, "/")
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + path, nil
}

// canonicalResource is the string that goes in the `resource` parameter.
//
// The document's own `resource` is preferred, because RFC 8707 wants the
// resource identifier as the resource states it. Falling back to the server's
// URL, canonicalised: no fragment, no trailing slash.
func canonicalResource(stated, serverURL string) string {
	if stated != "" {
		return stated
	}
	u, err := url.Parse(serverURL)
	if err != nil {
		return serverURL
	}
	u.Fragment = ""
	u.RawQuery = ""
	s := strings.TrimSuffix(u.String(), "/")
	return s
}

func fetchJSON(ctx context.Context, client *http.Client, endpoint string, into any) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" {
		// Every document in this flow decides where a credential goes. None of
		// them may be fetched in the clear.
		return fmt.Errorf("%q is not an https URL", endpoint)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cogitorium")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", endpoint, res.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxMetadata))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("%s answered something this server could not read: %w", endpoint, err)
	}
	return nil
}

// ErrNoSecretKey is what an install without COGITORIUM_SECRET_KEY gets instead
// of a flow. Refused rather than degraded: the alternative is writing somebody
// else's refresh token to disk in plaintext, which is worse than not supporting
// the feature.
var ErrNoSecretKey = errors.New("this install has no COGITORIUM_SECRET_KEY, and an OAuth grant is a live " +
	"credential that must be encrypted at rest — set one and restart, or give the server a token by name instead")
