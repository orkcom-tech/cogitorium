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

// These three reach the WHOLE context space — every workspace's shared
// branch, every agent's private memory, and the instruction library — so they
// follow the terminal's split: the global surface is admin-only, and members
// reach context through their workspace's own bindings instead. Per-branch
// access for non-admins is the right finer-grained answer and is a separate
// piece of work; until it exists, an unrestricted global reader is a hole.
func (s *Server) handleContextList(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	files, err := s.context.List(r.Context())
	if err != nil {
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) handleContextGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	path := r.URL.Query().Get("path")
	content, err := s.context.Get(r.Context(), path)
	if err != nil {
		failContext(w, r, err)
		return
	}
	// The version travels with the body so that the editor can hand it back on
	// save, which is what lets the save be refused instead of silently
	// overwriting somebody else's. A version this call cannot determine comes
	// back empty, and an empty version means the save is unguarded — the same
	// behaviour as before, rather than a guard that pretends to hold.
	version, _, _ := s.context.Version(r.Context(), path)
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "content": content, "version": version})
}

// handleContextDelete removes a document from the space.
//
// Behind the same admin rule as writing one, and it is a SOFT delete —
// contextd keeps every version and `contextd file undelete` brings it back. The
// interface says so rather than implying an erasure that did not happen.
func (s *Server) handleContextDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	q := r.URL.Query()
	path := q.Get("path")
	// The version the caller read, so removing a document somebody has just
	// rewritten is refused for the same reason overwriting it is.
	if err := s.context.Delete(r.Context(), path, q.Get("version")); err != nil {
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "status": "deleted"})
}

// handleContextSearch looks INSIDE the space's files.
//
// Before this the only way to find a memory was to already know its path, in a
// product whose claim is that agents keep a durable, shared memory. Behind the
// same admin rule as reading a file, because it returns the same bytes.
func (s *Server) handleContextSearch(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	res, err := s.context.Search(r.Context(), q.Get("q"), q.Get("path"), limit)
	if err != nil {
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleContextPut(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	path := r.URL.Query().Get("path")
	// MaxBytesReader errors on oversized bodies — a silent LimitReader
	// truncation would write a corrupted document and report success.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "document exceeds the 4MB limit")
			return
		}
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	// version is what the editor read when it opened the file. Absent means an
	// unguarded write — a new file, or a client that predates this — and is
	// allowed, because refusing it would make it impossible to create a file.
	if err := s.context.PutIfUnchanged(r.Context(), path, string(body), r.URL.Query().Get("version")); err != nil {
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "status": "written"})
}

// failContext maps contextstore errors: CAS conflicts are 409, an
// unavailable contextd is 503 with the actionable message.
func failContext(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, contextstore.ErrConflict), errors.Is(err, contextstore.ErrStale):
		// 409 for both: they are the same event seen from two places —
		// contextd refusing a write it can see is out of date, and this server
		// refusing one it can see is out of date first.
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
	id, ok := s.workspaceScoped(w, r)
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
	id, ok := s.workspaceScoped(w, r)
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
	id, ok := s.nestedScoped(w, r, func(bindingID int64) (int64, error) {
		return s.workspaces.WorkspaceOfContextBinding(r.Context(), bindingID)
	})
	if !ok {
		return
	}
	if err := s.workspaces.DeleteContextBinding(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAgentMemory lists everything shaping an agent, with where each part
// came from and whether it can be changed. Memory an operator cannot see is
// memory they cannot correct — which is how an agent ends up steering by
// something nobody intended and nobody noticed.
func (s *Server) handleAgentMemory(w http.ResponseWriter, r *http.Request) {
	id, ok := s.nestedScoped(w, r, func(agentID int64) (int64, error) {
		return s.workspaces.WorkspaceOfAgent(r.Context(), agentID)
	})
	if !ok {
		return
	}
	items, err := s.engine.Memory(r.Context(), id)
	if err != nil {
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleForgetMessage removes one entry from the timeline — the other half
// of an agent's memory, since the timeline is replayed on every turn.
func (s *Server) handleForgetMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.nestedScoped(w, r, func(msgID int64) (int64, error) {
		return s.workspaces.WorkspaceOfMessage(r.Context(), msgID)
	})
	if !ok {
		return
	}
	if err := s.workspaces.DeleteMessage(r.Context(), id); err != nil {
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
