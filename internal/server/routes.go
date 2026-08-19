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
	// Body is the type this route decodes a request into, or nil where the
	// body is not yet named. The description is generated from it, so a field
	// renamed in the struct is renamed in the document by the same edit.
	Body any
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
	// AuthToken is a signed-in user. There is no second way to satisfy it:
	// a request from this machine used to count as the admin, and does not.
	AuthToken = "token"
)

// pluginPagePrefix is where a plugin's own pages live, and it is reserved
// before anything is served under it.
//
// The reservation matters more than it looks. openToAnyone's last term exempts
// every non-/api/ path, because that is how the single-page app and its assets
// are served — so a new path space added outside /api/ is anonymous BY
// CONSTRUCTION, with no opt-in and no warning. A plugin page landing in that
// term would be reachable by anyone who knew the URL, and nothing in the
// plugin's manifest would have said so.
//
// Closed here instead, before the first page exists. A plugin that genuinely
// wants an open page will say so in its manifest and the operator will see it
// on the approval screen; that is a declaration, and a declaration cannot be
// expressed by a rule derived from the shape of a path. Until the declared
// path exists, everything under /p/ needs a token.
const pluginPagePrefix = "/p/"

// hostAssetPrefix is where this server's own vendored assets live.
//
// Deliberately not /assets/: that is the application's build output, and a
// handler registered there shadows every script and stylesheet the interface
// needs.
const hostAssetPrefix = "/cog/"

// openToAnyone reports whether a path is reachable without a credential.
//
// The authentication middleware and the published description both call this,
// so the document cannot say a door is locked that is not. It said exactly
// that about /api/v1/login until this function existed — the middleware let
// login through, the description demanded a token for it, and both were
// derived "the same way" only in a comment.
//
// The last term covers every non-API path, which is how the single-page app
// and its assets are served. Delivery is named separately rather than left to
// that term, so tightening the asset rule cannot silently close it.
func openToAnyone(path string) bool {
	// Plugin pages are carved out of the catch-all first. Order matters only
	// for a reader: the point is that the general "everything outside /api/"
	// term must never be what decides a plugin route.
	if strings.HasPrefix(path, pluginPagePrefix) {
		return false
	}
	return path == "/health" ||
		path == "/api/v1/login" ||
		path == "/api/v1/setup" ||
		strings.HasPrefix(path, inletDeliveryPrefix) ||
		!strings.HasPrefix(path, "/api/")
}

func authFor(path string) string {
	switch {
	case strings.HasPrefix(path, inletDeliveryPrefix):
		return AuthInletKey
	case openToAnyone(path):
		return AuthNone
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

// page registers a screen this server renders, and keeps it out of the API
// inventory.
//
// Its own verb rather than a prefix rule, because "is this a page" is a
// decision somebody makes when they write the route, and a rule inferring it
// from the path would be wrong the first time a page lives somewhere
// unexpected. The inventory describes a JSON API; putting a document in it
// would tell every generated client to expect one.
func (s *Server) page(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	mux.HandleFunc(pattern, h)
}

// routeIn is route, for an endpoint that takes a body. The body argument is a
// zero value of the type the handler decodes into — passing the type rather
// than describing it is what keeps the two from drifting.
func (s *Server) routeIn(mux *http.ServeMux, pattern string, h http.HandlerFunc, body any) {
	before := len(s.routes)
	s.route(mux, pattern, h)
	if len(s.routes) > before {
		s.routes[len(s.routes)-1].Body = body
	}
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
