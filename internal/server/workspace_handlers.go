package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaces.ListWorkspacesFor(r.Context(), callerFrom(r.Context()))
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
	caller := callerFrom(r.Context())
	ws, err := s.workspaces.CreateWorkspace(r.Context(), in.Name, in.Description, in.OrchestratorModelID, caller.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ws)
}

// handleCloneWorkspace copies a workspace's definition to the caller —
// agents, wires and their arrangement, never the timeline. This is how two
// people run the same setup without sharing one.
func (s *Server) handleCloneWorkspace(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	caller := callerFrom(r.Context())
	clone, err := s.workspaces.Clone(r.Context(), id, in.Name, caller.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, clone)
}

// handleSetWorkspaceTeam shares a workspace with a team, or withdraws it.
// Only the owner or an admin may change who else can reach their work.
func (s *Server) handleSetWorkspaceTeam(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	ws, err := s.workspaces.GetWorkspace(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	caller := callerFrom(r.Context())
	if !caller.IsAdmin() && (ws.OwnerID == nil || *ws.OwnerID != caller.ID) {
		writeError(w, http.StatusForbidden, "only the owner can share this workspace")
		return
	}
	var in struct {
		TeamID *int64 `json:"team_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	updated, err := s.workspaces.SetTeam(r.Context(), id, in.TeamID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
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
	id, ok := s.workspaceScoped(w, r)
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
	id, ok := s.workspaceScoped(w, r)
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
	id, ok := s.workspaceScoped(w, r)
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
	id, ok := s.nestedScoped(w, r, func(agentID int64) (int64, error) {
		return s.workspaces.WorkspaceOfAgent(r.Context(), agentID)
	})
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
	// A pure position patch must not trigger a read-modify-write of the
	// other fields — that write could resurrect values stale since read.
	if in.Name == nil && in.Role == nil && in.ModelID == nil {
		agent, err := s.workspaces.GetAgent(r.Context(), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, agent)
		return
	}
	agent, err := s.workspaces.UpdateAgent(r.Context(), id, in.Name, in.Role, in.ModelID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := s.nestedScoped(w, r, func(agentID int64) (int64, error) {
		return s.workspaces.WorkspaceOfAgent(r.Context(), agentID)
	})
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
	id, ok := s.workspaceScoped(w, r)
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
	id, ok := s.workspaceScoped(w, r)
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
	id, ok := s.nestedScoped(w, r, func(wireID int64) (int64, error) {
		return s.workspaces.WorkspaceOfWire(r.Context(), wireID)
	})
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
	id, ok := s.workspaceScoped(w, r)
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
	id, ok := s.workspaceScoped(w, r)
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

// handleWorkspaceUsage reports what every agent in the workspace has spent.
// One query rather than one call per agent, so the agent list can show spend
// without turning a page render into N round trips.
func (s *Server) handleWorkspaceUsage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	agents, err := s.workspaces.ListAgents(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	byAgent, err := s.workspaces.UsageForWorkspace(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	// Every agent appears, including ones that have never run: an absent row
	// reads as a loading bug, whereas an explicit zero reads as a fact.
	out := make([]workspace.Usage, 0, len(agents))
	for _, a := range agents {
		u, ok := byAgent[a.ID]
		if !ok {
			u = workspace.Usage{AgentID: a.ID}
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAgentUsage is the same figures for one agent.
func (s *Server) handleAgentUsage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.nestedScoped(w, r, func(agentID int64) (int64, error) {
		return s.workspaces.WorkspaceOfAgent(r.Context(), agentID)
	})
	if !ok {
		return
	}
	u, err := s.workspaces.UsageForAgent(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// handleWorkspaceChat runs one orchestrator turn, streaming engine events
// as SSE. Everything streamed is already persisted to the timeline.
func (s *Server) handleWorkspaceChat(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
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
