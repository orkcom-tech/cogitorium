package server

import (
	"net/http"
	"strings"
)

// The browser's credential.
//
// There are two ways to present a token to this server, and the split is not
// arbitrary — it is the standard one, because the two callers have opposite
// weaknesses.
//
// A SCRIPT sends `Authorization: Bearer`. It holds the token in a variable or
// an environment, nothing sends it automatically, and there is no third party
// who could make it send one.
//
// A BROWSER gets a cookie, and the cookie is the safer half of that trade:
//
//   - HttpOnly means no JavaScript on the page can read it. A token kept in
//     localStorage is one cross-site scripting bug away from being copied to
//     somebody else's server; this one cannot be read even by script running on
//     the page it belongs to.
//
//   - The cost of a cookie is that the browser attaches it BY ITSELF, including
//     to requests another site caused — which is what cross-site request forgery
//     is. SameSite=Lax is the answer: the browser withholds it from any
//     cross-site request that is not a top-level navigation, so no form post, no
//     fetch and no image from another origin ever carries it. checkOrigin below
//     is the second line, for the same reason a door has a lock and a chain.
//
// Which is why the two are not interchangeable: bearer is immune to forgery and
// exposed to script, cookies are the reverse, and each caller gets the one that
// fits how it stores things.
const sessionCookie = "cogitorium_session"

// A year. Long enough that a local install stops asking, short enough that an
// abandoned browser profile does not hold a working credential forever.
const rememberedFor = 365 * 24 * 60 * 60

// sessionToken is the token a browser presented, if it presented one.
func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(c.Value)
}

// setSession hands the browser its credential.
//
// Whether it OUTLIVES the browser is the difference between a laptop and a
// server, and it is the whole of what "remember me" means here. On a local
// install the cookie is given an expiry and the next launch walks straight in.
// On a server it is given none, which makes it a session cookie the browser
// drops when it closes — a borrowed or shared machine does not keep a working
// credential after the person walks away from it.
func (s *Server) setSession(w http.ResponseWriter, r *http.Request, token string) {
	c := &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
	}
	if s.localInstall {
		c.MaxAge = rememberedFor
	}
	http.SetCookie(w, c)
}

// clearSession is the other half of signing out. MaxAge -1 is how a cookie is
// deleted; the rest of the attributes have to match the ones it was set with or
// the browser treats it as a different cookie and keeps the original.
func clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
		MaxAge:   -1,
	})
}

// isHTTPS reports whether this request reached the server encrypted.
//
// Secure on a cookie means "only send this back over https", which is right
// everywhere except the address most installs run on: http://127.0.0.1. Setting
// it there would produce a cookie the browser accepts and never returns, and
// the operator would be signed out by a flag meant to protect them. The
// forwarded header is read because a real deployment terminates TLS in front of
// this process, so from here the request looks plaintext.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// checkOrigin is the chain behind SameSite's lock.
//
// A browser states, in Origin, which page caused a request. This compares it
// with the address the request arrived at, and refuses a mutation that came
// from anywhere else. SameSite=Lax should already have withheld the cookie in
// that case, so reaching here means either an old browser, a same-site
// subdomain SameSite does not separate, or a bug — and each is a reason to
// have the second check rather than to skip it.
//
// It runs ONLY for cookie-authenticated requests. A bearer token cannot be
// attached by a browser on somebody else's behalf, so there is nothing to forge
// and nothing to check; applying this to API clients would refuse every script
// that does not invent an Origin header.
//
// A missing Origin is allowed. Browsers send it on every request that could be
// forged — all of them here, since every mutation is a non-simple request — and
// non-browser clients send none at all.
func checkOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		// Nothing here changes state through one of these, and a top-level
		// navigation legitimately carries a cross-site Origin.
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return sameHost(origin, r.Host)
}
