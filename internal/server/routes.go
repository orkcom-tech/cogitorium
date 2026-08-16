package server

import (
	"net/http"
	"slices"
	"strings"
)

// The route inventory.
//
// Every endpoint is registered through s.route rather than mux.HandleFunc
// directly, for one reason: a published API description that is maintained
// beside the code drifts from it. This is the same list the mux is built from,
// so a route cannot exist without appearing in the document, and one that is
// deleted cannot linger in it.
//
// It records the shape of the surface — method, path, and which credential
// opens it — not the shape of each body. That distinction is deliberate: this
// much is true by construction, and anything beyond it has to be kept true by
// something else.

// Route is one registered endpoint.
type Route struct {
	Method string
	Path   string
	// Auth says what a caller needs. Derived from the path rather than
	// declared, because the middleware derives it the same way — two
	// independent statements of the same rule would be two things to keep in
	// step, and the one that is wrong would be the documentation.
	Auth string
}

const (
	// AuthNone is open: the health check, and the UI's own assets.
	AuthNone = "none"
	// AuthInletKey is a receiver's own key rather than a user's token. These
	// are the only paths exempt from normal authentication, and the exemption
	// matches by prefix so nothing but delivery can ever land under it.
	AuthInletKey = "inlet-key"
	// AuthToken is a signed-in user, which on a loopback listen address is
	// satisfied by being local.
	AuthToken = "token"
)

func authFor(path string) string {
	switch {
	case path == "/health":
		return AuthNone
	case strings.HasPrefix(path, "/i/"):
		return AuthInletKey
	default:
		return AuthToken
	}
}

// route registers a handler and remembers that it exists.
func (s *Server) route(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		// A pattern with no verb is a catch-all — "/api/" exists so a typo'd
		// API call answers JSON instead of falling through to the single-page
		// app and getting 200 and an HTML document. It is a fallback rather
		// than an endpoint, so it is registered and deliberately left out of
		// the inventory: describing it would invent a route that answers
		// nothing but 404.
		mux.HandleFunc(pattern, h)
		return
	}
	s.routes = append(s.routes, Route{Method: method, Path: path, Auth: authFor(path)})
	mux.HandleFunc(pattern, h)
}

// Routes returns every registered endpoint, sorted so the order is the
// document's rather than the source file's.
func (s *Server) Routes() []Route {
	out := slices.Clone(s.routes)
	slices.SortFunc(out, func(a, b Route) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Method, b.Method)
	})
	return out
}
