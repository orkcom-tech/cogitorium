package server

import (
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/identity"
)

// handleWhoami tells a client who it is and what it may do — the first call
// any UI makes.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, callerFrom(r.Context()))
}

// handleLogin exchanges a username and password for a token. This is the
// door the web, desktop and TUI clients all come through; after it, the
// token is the only credential on the wire.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	user, token, err := s.identity.Login(r.Context(), in.Name, in.Password)
	if err != nil {
		if isDomainError(err, identity.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "invalid username or password")
			return
		}
		fail(w, r, err)
		return
	}
	// Both, because both kinds of caller come through here. A browser uses the
	// cookie and ignores the field; a script reads the field and drops the
	// cookie on the floor. Returning the token to a browser costs nothing —
	// script that could read this response could have asked for it itself,
	// since it would need the password either way.
	s.setSession(w, r, token)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "token": token})
}

// handleLogout revokes only the token presented, so signing out on one
// client leaves the others alone.
//
// Whichever credential this request arrived with is the one revoked, and the
// cookie is taken back either way: a browser that kept a cookie naming a
// revoked token would be refused on every request afterwards without ever being
// shown the sign-in card.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		token = sessionToken(r)
	}
	clearSession(w, r)
	if token != "" {
		if err := s.identity.Logout(r.Context(), token); err != nil {
			fail(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetPassword lets a user set their own password, and an admin set
// anyone's — the flow that gives the seeded admin a password before the
// server is exposed beyond loopback.
func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	caller := callerFrom(r.Context())
	if !caller.IsAdmin() && caller.ID != id {
		writeError(w, http.StatusForbidden, "you may only change your own password")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.identity.SetPassword(r.Context(), id, in.Password); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	users, err := s.identity.ListUsers(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// handleCreateUser returns the new user's token — the only time it is ever
// visible, since only its hash is stored.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in struct {
		Name     string `json:"name"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	user, token, err := s.identity.CreateUser(r.Context(), in.Name, in.Role, in.Password)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":  user,
		"token": token,
		"notice": "This token is shown once and cannot be recovered — only its hash is stored. " +
			"Give it to the user now.",
	})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.identity.DeleteUser(r.Context(), id); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := s.identity.ListTeams(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, teams)
}

func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	team, err := s.identity.CreateTeam(r.Context(), in.Name)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, team)
}

func (s *Server) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.identity.DeleteTeam(r.Context(), id); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAddTeamMember(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		UserID int64 `json:"user_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.identity.AddTeamMember(r.Context(), id, in.UserID); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID, err := parseID(r.PathValue("userId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id in path")
		return
	}
	if err := s.identity.RemoveTeamMember(r.Context(), id, userID); err != nil {
		failIdentity(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// failIdentity maps validation errors to 400 rather than a server fault.
func failIdentity(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case isDomainError(err, identity.ErrNotFound), isDomainError(err, identity.ErrConflict):
		fail(w, r, err)
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
