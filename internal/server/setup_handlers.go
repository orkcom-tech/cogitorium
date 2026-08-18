package server

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/identity"
)

// The first run.
//
// Retiring loopback admin left a gap that had to be filled in the same change:
// if every request needs a token, and the only account on a fresh install has
// no password, then a new install is a locked room with the key inside. This
// is the way in — once, and only while nobody has been in yet.

// handleSetupState is what a client asks before it decides which screen to
// draw. Unauthenticated by necessity: the question is whether credentials can
// exist yet, so demanding one to ask it would be circular.
//
// It says as little as it can. Whether an install has been claimed is not a
// secret worth keeping — anyone who can reach the port learns it by trying —
// but the admin's name, the version, and the user list are not in here either.
func (s *Server) handleSetupState(w http.ResponseWriter, r *http.Request) {
	needs, err := s.identity.NeedsSetup(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup": needs,
		// Whether this install is only reachable from the machine it runs on.
		// The client needs it for two decisions a server must not make for it:
		// whether to offer to remember the session, and whether setup will
		// want the bootstrap token below.
		"local": s.localInstall,
	})
}

// handleSetup claims the install by giving the admin a password, and signs the
// caller in with it.
//
// THE THREE GUARDS, and none of them is optional:
//
//  1. It works exactly once. A second call is refused whether or not it
//     carries the right password, because "set the admin password" reachable
//     without credentials is a way to take an install over, not a way to
//     recover one. Recovery is a person with the database file, not a route.
//
//  2. On a server it demands the bootstrap token. Everything else here rests
//     on the caller being the operator, and on a listener the whole network
//     can reach, the first stranger to find the port would otherwise own this
//     install. Locally the token is not asked for, because anyone who can
//     reach loopback can already read the database this would be protecting.
//
//  3. It demands a JSON content type. Not pedantry: a plain cross-origin form
//     post is a request the browser sends without asking permission first, and
//     this endpoint is the one unauthenticated write in the API — every other
//     one needs an Authorization header, which such a form cannot set. A JSON
//     content type is not something a form can send either, so the browser has
//     to ask first, and this server never says yes. Without this line any web
//     page open in the operator's browser could claim their local install.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "setup must be sent as application/json")
		return
	}

	needs, err := s.identity.NeedsSetup(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if !needs {
		writeError(w, http.StatusConflict,
			"this install already has an administrator password: sign in with it, "+
				"or change it from the account menu once you are in")
		return
	}

	var in SetupBody
	if !decodeJSON(w, r, &in) {
		return
	}

	if !s.localInstall {
		token := in.Token
		if token == "" {
			token = bearerToken(r)
		}
		if token == "" {
			writeError(w, http.StatusUnauthorized,
				"this server is reachable over the network, so setting the first password needs the "+
					"admin token this server printed when it started")
			return
		}
		user, err := s.identity.Authenticate(r.Context(), token)
		if err != nil || !user.IsAdmin() {
			slog.Warn("setup refused: bad admin token", "remote", r.RemoteAddr)
			writeError(w, http.StatusUnauthorized, "that is not this install's admin token")
			return
		}
	}

	admin, err := s.identity.GetUserByName(r.Context(), identity.AdminName)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := s.identity.SetPassword(r.Context(), admin.ID, in.Password); err != nil {
		failIdentity(w, r, err)
		return
	}

	// Signed in with what was just set, rather than with a token minted here:
	// one path issues tokens, and a password that somehow did not take must
	// fail loudly now instead of at the next start.
	user, token, err := s.identity.Login(r.Context(), identity.AdminName, in.Password)
	if err != nil {
		fail(w, r, err)
		return
	}
	slog.Info("install claimed: the admin now has a password", "local", s.localInstall)
	s.setSession(w, r, token)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "token": token})
}
