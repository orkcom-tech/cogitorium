package server

import (
	"net/http"
	"strconv"

	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The memory drawer: everything one agent carries into every turn.
//
// The point is not tidiness. An agent that quietly carries something it picked
// up once will keep steering by it, and the only way to stop that is to be
// able to see it — so nothing here is behind a summary.
//
// It is the second drawer whose content depends on the client's state: which
// agent is selected. The agent travels in the URL rather than being remembered
// here, which keeps the panel a pure function of its request — and means a
// refresh, a reload and a link all show the same thing.

func (s *Server) memoryModel(r *http.Request, agentID int64, editing string) view.Memory {
	model := view.Memory{
		Ctx:     s.viewCtx(r, callerFrom(r.Context())),
		AgentID: agentID,
	}
	if agentID == 0 {
		model.Error = "pick an agent to see what it remembers"
		return model
	}

	if agent, err := s.workspaces.GetAgent(r.Context(), agentID); err == nil {
		model.AgentName = agent.Name
	}

	items, err := s.engine.Memory(r.Context(), agentID)
	if err != nil {
		model.Error = err.Error()
		return model
	}
	for _, it := range items {
		row := view.MemoryItem{
			Label: memoryLabel(it.Kind), Kind: it.Kind, Source: it.Source,
			Description: it.Description, Content: it.Content,
			Editable: it.Editable, Removable: it.Removable,
			IsRole:  it.Kind == "role",
			Editing: it.Editable && it.Source == editing,
		}
		if it.BindingID != nil {
			row.BindingID = *it.BindingID
		}
		// What this text is at right now, so a save or a delete can be refused
		// rather than clobbering whatever landed in between.
		if it.Editable || it.Kind != "role" {
			if v, _, err := s.context.Version(r.Context(), it.Source); err == nil {
				row.Version = v
			}
		}
		model.Items = append(model.Items, row)
	}
	return model
}

// memoryLabel is what a piece IS, in the words somebody reads rather than the
// word the engine stores it under.
func memoryLabel(kind string) string {
	switch kind {
	case "role":
		return "role"
	case "private":
		return "private to this agent"
	case "shared":
		return "shared with the workspace"
	case "bound":
		return "bound document"
	case "instruction":
		return "from the library"
	}
	return kind
}

// handleForgetMemoryForm drops one piece.
//
// Two different acts behind one button, and the confirmation says which: a
// bound document is unbound, so only this agent stops reading it, while
// anything else is removed from the space. The second is soft — Contextverse
// keeps every version — and the wording says so rather than promising an
// erasure that did not happen.
func (s *Server) handleForgetMemoryForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "that form could not be read", http.StatusBadRequest)
		return
	}
	agentID, _ := strconv.ParseInt(r.URL.Query().Get("agent"), 10, 64)

	if id, err := strconv.ParseInt(r.PostFormValue("binding_id"), 10, 64); err == nil && id != 0 {
		if err := s.workspaces.DeleteContextBinding(r.Context(), id); err != nil {
			s.renderDrawerModel(w, r, agentID, err.Error())
			return
		}
		s.renderDrawerModel(w, r, agentID, "")
		return
	}

	source := r.PostFormValue("source")
	if source == "" {
		s.renderDrawerModel(w, r, agentID, "that piece has no source to forget")
		return
	}
	// The version the panel read travels with the delete, for the same reason
	// it travels with a write: removing a document somebody has just rewritten
	// is exactly as destructive as overwriting it.
	if err := s.context.Delete(r.Context(), source, r.PostFormValue("version")); err != nil {
		s.renderDrawerModel(w, r, agentID, err.Error())
		return
	}
	s.renderDrawerModel(w, r, agentID, "")
}

// handleSaveMemoryForm writes a new version of one piece.
func (s *Server) handleSaveMemoryForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "that form could not be read", http.StatusBadRequest)
		return
	}
	agentID, _ := strconv.ParseInt(r.URL.Query().Get("agent"), 10, 64)
	source := r.PostFormValue("source")
	if source == "" {
		s.renderDrawerModel(w, r, agentID, "that piece has no source to write to")
		return
	}
	if err := s.context.PutIfUnchanged(r.Context(), source,
		r.PostFormValue("content"), r.PostFormValue("version")); err != nil {
		s.renderDrawerModel(w, r, agentID, err.Error())
		return
	}
	s.renderDrawerModel(w, r, agentID, "")
}

func (s *Server) renderDrawerModel(w http.ResponseWriter, r *http.Request, agentID int64, problem string) {
	model := s.memoryModel(r, agentID, "")
	if problem != "" {
		model.Error = problem
	}
	s.renderDrawer(w, r, "cog.drawer.memory", func() any { return model })
}
