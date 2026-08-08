package server

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/orkcom-tech/cogitorium/internal/contextstore"
)

func (s *Server) handleContextStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.context.CheckStatus(r.Context()))
}

func (s *Server) handleContextList(w http.ResponseWriter, r *http.Request) {
	files, err := s.context.List(r.Context())
	if err != nil {
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) handleContextGet(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	content, err := s.context.Get(r.Context(), path)
	if err != nil {
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "content": content})
}

func (s *Server) handleContextPut(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if err := s.context.Put(r.Context(), path, string(body)); err != nil {
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "status": "written"})
}

// failContext maps contextstore errors: CAS conflicts are 409, an
// unavailable contextd is 503 with the actionable message.
func failContext(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, contextstore.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, contextstore.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, contextstore.ErrNoSuchPath):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		fail(w, r, err)
	}
}

func (s *Server) handleListContextBindings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	bindings, err := s.workspaces.ListContextBindings(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bindings)
}

func (s *Server) handleCreateContextBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Path    string `json:"path"`
		AgentID *int64 `json:"agent_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	// Verify the path exists in the space before binding it.
	if _, err := s.context.Get(r.Context(), in.Path); err != nil {
		failContext(w, r, err)
		return
	}
	b, err := s.workspaces.CreateContextBinding(r.Context(), id, in.Path, in.AgentID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) handleDeleteContextBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.workspaces.DeleteContextBinding(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAgentPrompt returns the assembled system prompt — the "what does
// this agent actually see" preview.
func (s *Server) handleAgentPrompt(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id in path")
		return
	}
	prompt, err := s.engine.AssembledPrompt(r.Context(), id)
	if err != nil {
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"prompt": prompt})
}
