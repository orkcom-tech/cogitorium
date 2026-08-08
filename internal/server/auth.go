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
		if r.URL.Path == "/health" || !strings.HasPrefix(r.URL.Path, "/api/") {
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
