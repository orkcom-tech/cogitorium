package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
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

// authenticate resolves every request to a user. A loopback request with no
// credentials is treated as the admin: that is what makes a single-operator
// install feel like it has no accounts, without there being a second code
// path anywhere — the model is identical, only the way the caller proves
// who they are differs.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Login is the one API route that must be reachable without
		// credentials — it is where credentials come from.
		if r.URL.Path == "/health" || r.URL.Path == "/api/v1/login" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		if token := bearerToken(r); token != "" {
			user, err := s.identity.Authenticate(r.Context(), token)
			if err != nil {
				if errors.Is(err, identity.ErrUnauthorized) {
					writeError(w, http.StatusUnauthorized, "invalid token")
					return
				}
				fail(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
			return
		}

		if s.trustLoopback && isLoopback(r) {
			admin, err := s.identity.GetUserByName(r.Context(), identity.AdminName)
			if err != nil {
				fail(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, admin)))
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
	return ""
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
