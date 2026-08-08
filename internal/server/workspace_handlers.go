package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/orkcom-tech/cogitorium/internal/engine"
)

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaces.ListWorkspaces(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name                string `json:"name"`
		Description         string `json:"description"`
		OrchestratorModelID int64  `json:"orchestrator_model_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if _, err := s.catalog.GetModel(r.Context(), in.OrchestratorModelID); err != nil {
		fail(w, r, err)
		return
	}
	ws, err := s.workspaces.CreateWorkspace(r.Context(), in.Name, in.Description, in.OrchestratorModelID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ws)
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ws, err := s.workspaces.GetWorkspace(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.workspaces.DeleteWorkspace(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	agents, err := s.workspaces.ListAgents(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name    string `json:"name"`
		Role    string `json:"role"`
		ModelID int64  `json:"model_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if _, err := s.catalog.GetModel(r.Context(), in.ModelID); err != nil {
		fail(w, r, err)
		return
	}
	agent, err := s.workspaces.CreateAgent(r.Context(), id, in.Name, in.Role, in.ModelID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, agent)
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name    *string  `json:"name"`
		Role    *string  `json:"role"`
		ModelID *int64   `json:"model_id"`
		PosX    *float64 `json:"pos_x"`
		PosY    *float64 `json:"pos_y"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.ModelID != nil {
		if _, err := s.catalog.GetModel(r.Context(), *in.ModelID); err != nil {
			fail(w, r, err)
			return
		}
	}
	if in.PosX != nil && in.PosY != nil {
		if err := s.workspaces.SetAgentPosition(r.Context(), id, *in.PosX, *in.PosY); err != nil {
			fail(w, r, err)
			return
		}
	}
	agent, err := s.workspaces.UpdateAgent(r.Context(), id, in.Name, in.Role, in.ModelID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.workspaces.DeleteAgent(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListWires(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	wires, err := s.workspaces.ListWires(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wires)
}

func (s *Server) handleCreateWire(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		FromAgentID int64  `json:"from_agent_id"`
		ToAgentID   int64  `json:"to_agent_id"`
		Label       string `json:"label"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	wire, err := s.workspaces.CreateWire(r.Context(), id, in.FromAgentID, in.ToAgentID, in.Label)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, wire)
}

func (s *Server) handleDeleteWire(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.workspaces.DeleteWire(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListWSMessages returns the timeline; ?agent_id= filters to one
// agent (the per-agent activity view).
func (s *Server) handleListWSMessages(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var agentID *int64
	if v := r.URL.Query().Get("agent_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid agent_id")
			return
		}
		agentID = &n
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := s.workspaces.ListMessages(r.Context(), id, agentID, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleWorkspaceStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	statuses, err := s.engine.Statuses(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, statuses)
}

// handleWorkspaceChat runs one orchestrator turn, streaming engine events
// as SSE. Everything streamed is already persisted to the timeline.
func (s *Server) handleWorkspaceChat(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Text == "" {
		writeError(w, http.StatusBadRequest, "text must not be empty")
		return
	}
	if _, err := s.workspaces.GetWorkspace(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	emit := func(ev engine.Event) {
		raw, err := json.Marshal(ev)
		if err != nil {
			slog.Error("workspace chat: marshal event", "err", err)
			return
		}
		if _, err := w.Write([]byte("data: " + string(raw) + "\n\n")); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			slog.Warn("workspace chat: flush failed", "err", err)
		}
	}

	if err := s.engine.HandleUserMessage(r.Context(), id, in.Text, emit); err != nil {
		// Pre-stream failures (busy workspace, no orchestrator) surface as
		// an SSE error event since headers are already out.
		emit(engine.Event{Type: "error", Error: err.Error()})
	}
}
