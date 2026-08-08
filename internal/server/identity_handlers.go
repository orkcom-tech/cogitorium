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
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	user, token, err := s.identity.CreateUser(r.Context(), in.Name, in.Role)
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
