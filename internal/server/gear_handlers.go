package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/orkcom-tech/cogitorium/internal/gear"
)

// failGear maps gear validation errors (bad timeout, bad runtime) to 400
// rather than letting them surface as a server fault.
func failGear(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, gear.ErrNotFound) || errors.Is(err, gear.ErrConflict) {
		fail(w, r, err)
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

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

// handleCreateGear lets the operator author or correct a gear directly.
// Without it a fumbled forge is unrecoverable and a nearly-right gear
// cannot be fixed — only an agent could ever produce one.
func (s *Server) handleCreateGear(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Runtime     string   `json:"runtime"`
		Code        string   `json:"code"`
		Entrypoint  string   `json:"entrypoint"`
		ArgsSchema  string   `json:"args_schema"`
		Files       []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	files := make([]gear.File, 0, len(in.Files))
	for _, f := range in.Files {
		files = append(files, gear.File{Path: f.Path, Content: f.Content})
	}
	entrypoint := in.Entrypoint
	if in.Code != "" && len(files) == 0 {
		entrypoint = gear.DefaultEntrypoint(in.Runtime)
		files = []gear.File{{Path: entrypoint, Content: in.Code}}
	}
	if entrypoint == "" && len(files) == 1 {
		entrypoint = files[0].Path
	}

	// wsID/agentID 0: authored by the operator, not forged by an agent.
	g, err := s.gears.Forge(r.Context(), in.Name, in.Description, in.Tags, in.Runtime, entrypoint, in.ArgsSchema, files, 0, 0)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// handleRunGear is the operator's dry run: execute a gear — including a
// pending one — to see what it actually does before approving it.
func (s *Server) handleRunGear(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Args json.RawMessage `json:"args"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	g, err := s.gears.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	args := "{}"
	if len(in.Args) > 0 {
		args = string(in.Args)
	}
	res, runErr := s.gearExec.Run(r.Context(), g, args, gear.Caller{DryRun: true})
	out := map[string]any{
		"stdout": res.Stdout, "stderr": res.Stderr,
		"exit_code": res.ExitCode, "timed_out": res.TimedOut,
	}
	if runErr != nil {
		out["error"] = runErr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListGearRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.gears.ListRuns(r.Context(), id, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// handleSetGearStatus is the operator's approval gate — the only path that
// can make agent-authored code runnable.
func (s *Server) handleSetGearStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Status         *string `json:"status"`
		TimeoutSeconds *int    `json:"timeout_seconds"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.TimeoutSeconds != nil {
		g, err := s.gears.SetTimeout(r.Context(), id, *in.TimeoutSeconds)
		if err != nil {
			failGear(w, r, err)
			return
		}
		if in.Status == nil {
			writeJSON(w, http.StatusOK, g)
			return
		}
	}
	if in.Status == nil {
		writeError(w, http.StatusBadRequest, "nothing to change: send status and/or timeout_seconds")
		return
	}
	switch *in.Status {
	case gear.StatusPending, gear.StatusApproved, gear.StatusDisabled:
	default:
		writeError(w, http.StatusBadRequest, "status must be pending, approved or disabled")
		return
	}
	g, err := s.gears.SetStatus(r.Context(), id, *in.Status)
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
	id, ok := s.workspaceScoped(w, r)
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
	id, ok := s.workspaceScoped(w, r)
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
