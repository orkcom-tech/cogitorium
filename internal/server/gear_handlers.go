package server

import (
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/gear"
)

func (s *Server) handleListGears(w http.ResponseWriter, r *http.Request) {
	gears, err := s.gears.List(r.Context(), r.URL.Query().Get("tag"), r.URL.Query().Get("q"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gears)
}

// handleGetGear returns the gear plus its current version's source, so the
// operator reviews exactly what approval would make runnable.
func (s *Server) handleGetGear(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	g, err := s.gears.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	files, err := s.gears.Files(r.Context(), g.ID, g.Version)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"gear": g, "files": files})
}

// handleSetGearStatus is the operator's approval gate — the only path that
// can make agent-authored code runnable.
func (s *Server) handleSetGearStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	switch in.Status {
	case gear.StatusPending, gear.StatusApproved, gear.StatusDisabled:
	default:
		writeError(w, http.StatusBadRequest, "status must be pending, approved or disabled")
		return
	}
	g, err := s.gears.SetStatus(r.Context(), id, in.Status)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleDeleteGear(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.gears.Delete(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListGearBindings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	bindings, err := s.gears.ListBindings(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bindings)
}

func (s *Server) handleCreateGearBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		GearID  int64  `json:"gear_id"`
		AgentID *int64 `json:"agent_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if _, err := s.gears.Get(r.Context(), in.GearID); err != nil {
		fail(w, r, err)
		return
	}
	b, err := s.gears.Bind(r.Context(), in.GearID, id, in.AgentID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) handleDeleteGearBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.gears.Unbind(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
