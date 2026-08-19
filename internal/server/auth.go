package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/identity"
)

type ctxKey int

const userKey ctxKey = iota

// callerFrom returns the authenticated user. Handlers reach it only behind
// the auth middleware, so absence is a programming error, not a request
// error.
func callerFrom(ctx context.Context) identity.User {
	u, _ := ctx.Value(userKey).(identity.User)
	return u
}

// authenticate resolves every request to a user, and there are two ways to be
// resolved — a bearer token or a session cookie. See internal/server/session.go
// for why a browser gets a different one from a script, and why neither is
// simply better.
//
// It used to be two. A request arriving from 127.0.0.1 with no credentials was
// served as the admin, so that a single-operator install felt accountless. The
// cost of that convenience was that EVERY process on the machine was an
// administrator of this install — any script, any dependency's postinstall, any
// page in a browser that could reach the port. The account model was sound and
// the front door was propped open. It is closed: a person signs in, and what
// they get back is a token like everybody else's.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Login and setup are the API routes that must be reachable without
		// credentials — they are where credentials come from, and on a fresh
		// install there is no password to send yet. Setup carries its own
		// guards, which is where the reasoning for them lives. Inlet delivery is
		// exempt for the same reason: it proves itself against an inlet's own
		// key rather than against a token, so there is nothing here to resolve.
		// callerFrom then returns the zero user inside that handler, and any
		// path from it into requireAdmin or requireWorkspace is refused rather
		// than granted.
		//
		// The exemption matches by PREFIX, so /i/ must carry delivery and
		// nothing else. Inlet management lives under
		// /api/v1/workspaces/{id}/inlets and is authenticated here like the
		// rest of the API. Getting that wrong is the whole security failure.
		//
		// The last term already exempts every non-/api/ path, because that is
		// how the SPA and its assets are served. Delivery is named anyway: it
		// must not be exempt only as a side effect of a rule about static
		// files, which somebody tightening the SPA fallback would take away
		// without ever seeing an inlet.
		// Plugin space is decided by DECLARATION, not by the shape of the
		// path, and it is decided before the derived rules get a look in. A
		// plugin saying its page is open is somebody's decision about their
		// own plugin; a rule about where a URL sits cannot express that, and
		// letting the derived rule answer here would either close a page an
		// operator approved as open or open one nobody did.
		if strings.HasPrefix(r.URL.Path, pluginPagePrefix) {
			auth, declared := s.plugins.pageAuth(r.URL.Path)
			if !declared {
				// Not a page anybody declared. A 404 here rather than a walk
				// through the credential machinery, so an unknown plugin path
				// cannot be probed for whether it exists by watching which
				// refusal comes back.
				http.NotFound(w, r)
				return
			}
			if auth == "none" {
				next.ServeHTTP(w, r)
				return
			}
			// Everything else resolves a credential below, and "admin" is
			// answered by the handler once there is somebody to check.
		} else if openToAnyone(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Bearer first. A client that went to the trouble of naming a token
		// meant that token, and silently preferring a stale cookie sitting in
		// the same browser would be the confusing failure.
		token, byCookie := bearerToken(r), false
		if token == "" {
			token, byCookie = sessionToken(r), true
		}
		if token != "" {
			// Only the browser's credential can be attached to a request the
			// operator did not make, so only the browser's credential needs
			// this. See checkOrigin.
			if byCookie && !checkOrigin(r) {
				slog.Warn("cross-origin write refused",
					"origin", r.Header.Get("Origin"), "host", r.Host, "path", r.URL.Path)
				writeError(w, http.StatusForbidden,
					"this request came from another site, and a signed-in session is not usable from one")
				return
			}
			user, err := s.identity.Authenticate(r.Context(), token)
			if err != nil {
				if errors.Is(err, identity.ErrUnauthorized) {
					if byCookie {
						// The session was revoked or the database replaced.
						// Take the cookie back, or every request from this
						// browser fails the same way until it is cleared by
						// hand.
						clearSession(w, r)
					}
					writeError(w, http.StatusUnauthorized, "invalid token")
					return
				}
				fail(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
			return
		}

		writeError(w, http.StatusUnauthorized, "authentication required: send Authorization: Bearer <token>")
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	// A browser cannot set headers when opening a WebSocket, so the token
	// arrives as a subprotocol instead: ["bearer", "<token>"].
	if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		parts := strings.Split(proto, ",")
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "bearer" {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

// human is a caller together with HOW they proved who they were, which every
// egress record stores. Today there is one way and the field is always
// "bearer"; it is kept because rows written before loopback admin was retired
// say "loopback-implicit", and a reader of the audit trail needs to be able to
// tell those apart rather than have the distinction quietly rewritten.
type human struct {
	user identity.User
	auth string // "bearer" | "loopback-implicit" (historical)
}

// requireHuman gates the two actions that must be a person's: granting the
// internet gate, and approving one search.
//
// It used to also refuse an implicitly-admin loopback caller, under the
// egress_approval_bearer option. Both are gone: there is no implicit caller
// left to refuse, so every grant is now made by someone who signed in — which
// is what that option asked for, unconditionally.
func (s *Server) requireHuman(w http.ResponseWriter, r *http.Request) (human, bool) {
	u, ok := requireAdmin(w, r)
	if !ok {
		return human{}, false
	}
	return human{user: u, auth: "bearer"}, true
}

// requireWorkspaceCtx is requireWorkspace with a caller-supplied context, so
// a route that must not hang on a saturated database can bound its own check.
func (s *Server) requireWorkspaceCtx(ctx context.Context, w http.ResponseWriter, r *http.Request, wsID int64) bool {
	ok, err := s.workspaces.CanAccess(ctx, callerFrom(r.Context()), wsID)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusServiceUnavailable,
				"could not verify this approval — the database is busy. Try again.")
			return false
		}
		fail(w, r, err)
		return false
	}
	if !ok {
		// 403 here rather than 404: the caller is answering a prompt that
		// demonstrably exists, so hiding it would only confuse the operator
		// whose click lost a race with someone else's.
		writeError(w, http.StatusForbidden, "this approval belongs to a workspace you cannot reach")
		return false
	}
	return true
}

// requireWorkspace is the guard every workspace-scoped route runs first.
// Filtering the list alone would leave every other route open to a direct
// id, so access is checked where the work happens, not where it is listed.
func (s *Server) requireWorkspace(w http.ResponseWriter, r *http.Request, wsID int64) bool {
	ok, err := s.workspaces.CanAccess(r.Context(), callerFrom(r.Context()), wsID)
	if err != nil {
		fail(w, r, err)
		return false
	}
	if !ok {
		// 404, not 403: whether a workspace exists is not something a
		// stranger gets to learn from the status code.
		writeError(w, http.StatusNotFound, "no such workspace")
		return false
	}
	return true
}

// workspaceScoped reads the {id} path value as a workspace id and checks it.
func (s *Server) workspaceScoped(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return 0, false
	}
	return id, s.requireWorkspace(w, r, id)
}

// nestedScoped checks a resource identified by its own id — an agent, wire
// or binding — by resolving it back to the workspace that owns it.
func (s *Server) nestedScoped(w http.ResponseWriter, r *http.Request, resolve func(int64) (int64, error)) (int64, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return 0, false
	}
	wsID, err := resolve(id)
	if err != nil {
		fail(w, r, err)
		return 0, false
	}
	return id, s.requireWorkspace(w, r, wsID)
}

// requireAdmin gates the operations only an administrator may perform:
// users, teams, and anything that reconfigures the install itself.
func requireAdmin(w http.ResponseWriter, r *http.Request) (identity.User, bool) {
	u := callerFrom(r.Context())
	if !u.IsAdmin() {
		slog.Warn("admin action refused", "user", u.Name, "role", u.Role, "path", r.URL.Path)
		writeError(w, http.StatusForbidden, "this action requires the admin role")
		return u, false
	}
	return u, true
}
