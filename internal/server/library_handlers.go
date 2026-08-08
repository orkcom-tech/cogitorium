package server

import (
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/library"
)

func (s *Server) handleListInstructions(w http.ResponseWriter, r *http.Request) {
	items, err := s.library.List(r.Context(), r.URL.Query().Get("tag"), r.URL.Query().Get("q"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleGetInstruction returns the catalogue entry together with its text,
// which is fetched from Contextverse rather than stored here.
func (s *Server) handleGetInstruction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	in, err := s.library.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	text, err := s.context.Get(r.Context(), in.Path)
	if err != nil {
		failContext(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instruction": in, "text": text})
}

// handleSaveInstruction writes the text to Contextverse and records the
// index entry. Anyone signed in may add to the library: an instruction is
// text, and binding one is already a per-workspace decision.
func (s *Server) handleSaveInstruction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Text        string   `json:"text"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Text == "" {
		writeError(w, http.StatusBadRequest, "text must not be empty")
		return
	}
	if err := s.context.Put(r.Context(), library.PathFor(in.Name), in.Text); err != nil {
		failContext(w, r, err)
		return
	}
	saved, err := s.library.Save(r.Context(), in.Name, in.Description, in.Tags, 0, 0)
	if err != nil {
		failIdentity(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

// handleDeleteInstruction removes the catalogue entry. The text stays in
// Contextverse — unlisting is not a reason to destroy someone's writing.
func (s *Server) handleDeleteInstruction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.library.Delete(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
