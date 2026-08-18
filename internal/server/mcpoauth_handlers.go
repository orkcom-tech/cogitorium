package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/mcpoauth"
	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
)

// Signing this install in to a remote MCP server.
//
// Two routes and they are not symmetrical. STARTING is an administrator's, like
// everything else about MCP: it registers this install with somebody else's
// authorization server and ends in a credential this server will hold. The
// CALLBACK cannot be, because it arrives on a browser redirect from that
// authorization server, and requiring a bearer token on it would mean requiring
// the operator's API token to survive a cross-site redirect.
//
// What stands in for authentication on the callback is the `state`: a value
// this server generated with a CSPRNG, stored, and consumes exactly once. A
// callback with a state nobody started is refused, which is the same protection
// under a different name.

// handleStartMCPOAuth begins the flow and hands back where to send the browser.
func (s *Server) handleStartMCPOAuth(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.mcpOAuth.Available() {
		writeError(w, http.StatusConflict, mcpoauth.ErrNoSecretKey.Error())
		return
	}
	srv, err := s.mcp.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if srv.Transport == mcpstore.TransportStdio {
		writeError(w, http.StatusBadRequest,
			"this server runs as a child process here, so it takes its credentials from named values rather "+
				"than from a sign-in. OAuth is for a hosted server reached over https.")
		return
	}

	redirect, err := s.oauthRedirectURI()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// The refusal is asked FOR: this server has not been signed in to, so the
	// first request is expected to be refused, and the refusal is what names
	// the authorization server and the scopes.
	ch := s.mcpChallenge(r, srv)
	d, err := mcpoauth.Discover(r.Context(), s.mcpOAuth.HTTPClient(), srv.URL, ch)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// A step-up keeps what was already granted: a challenge names only what the
	// failed operation needed, and asking for that alone silently drops the rest.
	if held, err := s.mcpOAuth.Get(r.Context(), id); err == nil {
		d.Scopes = mcpoauth.StepUpScopes(held.Scopes, ch)
	}

	start, err := mcpoauth.Begin(r.Context(), s.mcpOAuth.HTTPClient(), d, redirect, "", "")
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := s.mcpOAuth.SavePending(r.Context(), id, start); err != nil {
		fail(w, r, err)
		return
	}
	slog.Info("an MCP sign-in was started", "server", srv.Name, "issuer", start.Issuer,
		"scopes", strings.Join(start.Scopes, " "), "resource", start.Resource)

	writeJSON(w, http.StatusOK, map[string]any{
		"authorize_url": start.AuthorizeURL,
		"issuer":        start.Issuer,
		"scopes":        start.Scopes,
		"resource":      start.Resource,
	})
}

// handleMCPOAuthCallback completes the flow.
//
// Unauthenticated by necessity — see the file comment — and every check that
// makes it safe happens here: the state must be one this server started, the
// `iss` must match what was recorded BEFORE the redirect, and the exchange
// carries the PKCE verifier that only this server holds.
func (s *Server) handleMCPOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	q := r.URL.Query()

	// An error from the authorization server, which is a legitimate outcome:
	// the operator pressed cancel. Its `iss` is validated first, because the
	// spec says a client must not act on or display an error whose issuer does
	// not match.
	state := q.Get("state")
	if state == "" {
		oauthDone(w, "This sign-in carried no state, so it is not one this server started.")
		return
	}
	start, serverID, err := s.mcpOAuth.TakePending(r.Context(), state)
	if err != nil {
		oauthDone(w, err.Error())
		return
	}
	if err := mcpoauth.ValidateIssuer(start.Issuer, q.Get("iss"), start.IssAdvertised); err != nil {
		slog.Warn("an MCP sign-in was refused on issuer validation", "server_id", serverID, "err", err)
		oauthDone(w, "This sign-in did not come back from where it was sent: "+err.Error())
		return
	}
	if e := q.Get("error"); e != "" {
		oauthDone(w, "The authorization server refused: "+e+" "+q.Get("error_description"))
		return
	}
	code := q.Get("code")
	if code == "" {
		oauthDone(w, "The authorization server sent no code back.")
		return
	}

	tok, err := mcpoauth.Exchange(r.Context(), s.mcpOAuth.HTTPClient(), start, code)
	if err != nil {
		oauthDone(w, err.Error())
		return
	}
	if err := s.mcpOAuth.Save(r.Context(), serverID, start, tok); err != nil {
		oauthDone(w, err.Error())
		return
	}
	oauthDone(w, "")
}

// handleForgetMCPOAuth disconnects a server.
func (s *Server) handleForgetMCPOAuth(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.mcpOAuth.Forget(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mcpChallenge asks the server what it wants, expecting to be refused.
//
// A server that answers anything but a challenge is one that does not use
// OAuth, and discovery falls back to the well-known location — which is the
// right behaviour rather than a failure, because a server MAY publish its
// metadata without ever having refused this particular request.
func (s *Server) mcpChallenge(r *http.Request, srv mcpstore.Server) mcpoauth.Challenge {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, srv.URL, strings.NewReader("{}"))
	if err != nil {
		return mcpoauth.Challenge{}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := s.mcpOAuth.HTTPClient().Do(req)
	if err != nil {
		return mcpoauth.Challenge{}
	}
	defer res.Body.Close()
	ch, _ := mcpoauth.ParseChallenge(res.StatusCode, res.Header.Get("WWW-Authenticate"))
	return ch
}

// oauthRedirectURI is where the authorization server sends the browser back.
//
// It must be an address the OPERATOR'S BROWSER can reach and that this server
// answers on, and those are the same thing only on a laptop. On anything else
// public_url is the only place that knows it — which is why an install without
// one is refused rather than sent somewhere that will not answer.
func (s *Server) oauthRedirectURI() (string, error) {
	base := strings.TrimSuffix(s.publicURL, "/")
	if base == "" {
		if !s.trustLoopback {
			return "", errors.New("this install does not know how it is reached from outside, and an OAuth " +
				"redirect has to name an address the browser can return to: set public_url and restart")
		}
		base = "http://" + s.http.Addr
	}
	return base + "/api/v1/mcp-oauth/callback", nil
}

// oauthDone is what the operator's browser lands on. A page rather than JSON,
// because a person is looking at it: they were sent here by an authorization
// server and this is the last thing they see before going back to the tab they
// started in.
func oauthDone(w http.ResponseWriter, problem string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := `<!doctype html><meta charset=utf-8><title>Cogitorium</title>` +
		`<body style="font:16px/1.6 system-ui;margin:4rem auto;max-width:34rem;padding:0 1rem">`
	if problem == "" {
		w.WriteHeader(http.StatusOK)
		body += `<h1>Signed in</h1><p>This server is connected. It is still <strong>pending</strong> until an ` +
			`administrator approves what it runs — signing in is not approving.</p>`
	} else {
		w.WriteHeader(http.StatusBadRequest)
		// The message is escaped: some of it comes from an authorization
		// server, and a redirect that could write markup into this page would
		// be a cross-site scripting hole on this install's own origin.
		body += `<h1>That did not work</h1><p>` + escapeHTML(problem) + `</p>`
	}
	body += `<p>You can close this tab.</p>`
	_, _ = w.Write([]byte(body))
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}
