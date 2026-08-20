package server

import (
	"net/http"
	"strconv"
	"strings"
)

// The receivers panel's write half.
//
// It had none. The panel drew "add a receiver", "rotate the key" and "delete",
// all three posting to paths nothing served, so every one of them answered a
// bare `404 page not found` — on a screen whose read side worked perfectly.
//
// These do exactly what the JSON handlers beside them do, and answer with the
// panel instead of a document, because that is what the drawer swaps in.

func (s *Server) handleCreateInletForm(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderInlets(w, r, wsID, "that form could not be read", "", "")
		return
	}
	address := strings.TrimSpace(r.PostFormValue("address"))
	if address == "" {
		s.renderInlets(w, r, wsID, "a receiver needs an address — it is the last part of the URL things post to", "", "")
		return
	}

	created, err := s.inlets.CreateInlet(r.Context(), wsID, address, r.PostFormValue("description"))
	if err != nil {
		s.renderInlets(w, r, wsID, err.Error(), "", "")
		return
	}
	// The key is issued with the receiver, because a receiver nothing can post
	// to is not yet a receiver. It is shown once, here, and kept only as a
	// hash — which the panel says before it appears rather than after.
	key, err := s.inlets.IssueKey(r.Context(), created.ID)
	if err != nil {
		s.renderInlets(w, r, wsID, err.Error(), "", "")
		return
	}
	s.renderInlets(w, r, wsID, "", address+" is listening", key)
}

func (s *Server) handleRotateInletKeyForm(w http.ResponseWriter, r *http.Request) {
	wsID, id, ok := s.inletScoped(w, r)
	if !ok {
		return
	}
	key, err := s.inlets.IssueKey(r.Context(), id)
	if err != nil {
		s.renderInlets(w, r, wsID, err.Error(), "", "")
		return
	}
	// The old one stops working the moment this one exists, which is the whole
	// point of rotating and is worth saying on the screen that did it.
	s.renderInlets(w, r, wsID, "", "a new key is issued; the previous one no longer works", key)
}

func (s *Server) handleDeleteInletForm(w http.ResponseWriter, r *http.Request) {
	wsID, id, ok := s.inletScoped(w, r)
	if !ok {
		return
	}
	if err := s.inlets.DeleteInlet(r.Context(), id); err != nil {
		s.renderInlets(w, r, wsID, err.Error(), "", "")
		return
	}
	s.renderInlets(w, r, wsID, "", "that receiver is gone; anything still posting to it gets a 404", "")
}

// inletScoped resolves a receiver to the workspace that owns it and checks the
// caller may reach that workspace, so a member of one cannot delete another's
// receiver by guessing an id.
func (s *Server) inletScoped(w http.ResponseWriter, r *http.Request) (wsID, id int64, ok bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "that is not a receiver", http.StatusBadRequest)
		return 0, 0, false
	}
	wsID, err = s.inlets.WorkspaceOfInlet(r.Context(), id)
	if err != nil {
		http.Error(w, "there is no such receiver", http.StatusNotFound)
		return 0, 0, false
	}
	if !s.requireWorkspace(w, r, wsID) {
		return 0, 0, false
	}
	return wsID, id, true
}

func (s *Server) renderInlets(w http.ResponseWriter, r *http.Request, wsID int64, problem, notice, key string) {
	model := s.inletsModel(r, wsID, problem, notice, nil)
	model.JustIssued = key
	s.renderDrawer(w, r, "cog.drawer.receivers", func() any { return model })
}
