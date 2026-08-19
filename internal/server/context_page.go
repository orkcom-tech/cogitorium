package server

import (
	"net/http"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The context space, served as a template.
//
// This page has a state most do not: contextd unreachable. Its text is not
// stored here — Contextverse holds it, and every write goes through contextd
// so versioning stays where the versions are. An install without it has no
// space to show, and that has to read differently from an empty one or
// somebody concludes their memory is gone.
//
// Saving carries the version the text was READ at. A save carrying only the
// text would silently overwrite whatever landed in between; carrying the
// version turns that into a refusal naming both, which is the one thing the
// person about to lose work needs.

func (s *Server) handleContextPage(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	s.renderContext(w, r, "", "")
}

// handleSaveContextForm writes a new version, or refuses.
func (s *Server) handleSaveContextForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderContext(w, r, "that form could not be read", "")
		return
	}
	path := r.PostFormValue("path")
	if path == "" {
		s.renderContext(w, r, "a save needs to know which file", "")
		return
	}

	// PutIfUnchanged rather than Put: the version the text was opened at is
	// the whole reason the field exists, and a refusal here is the guard
	// working rather than an error.
	err := s.context.PutIfUnchanged(r.Context(), path,
		r.PostFormValue("text"), r.PostFormValue("version"))
	if err != nil {
		// The server's own sentence — which version it is at and which one was
		// opened — rather than "save failed".
		s.renderContext(w, r, err.Error(), "")
		return
	}
	s.renderContext(w, r, "", "saved a new version of "+path)
}

func (s *Server) renderContext(w http.ResponseWriter, r *http.Request, problem, notice string) {
	model := view.Context{
		Ctx:    s.viewCtx(r, callerFrom(r.Context())),
		Error:  problem,
		Notice: notice,
	}

	status := s.context.CheckStatus(r.Context())
	model.Available, model.Unusable, model.SpaceRoot = status.Available, status.Error, status.SpaceRoot
	if !model.Available {
		// Nothing else here is meaningful. Drawing an empty file list beside a
		// broken contextd would look exactly like a space with nothing in it.
		s.renderPage(w, r, "cog.page.context", "", "Context", model)
		return
	}

	open := r.URL.Query().Get("open")
	if r.Method == http.MethodPost {
		open = r.PostFormValue("path")
	}
	model.Open = open

	files, err := s.context.List(r.Context())
	if err != nil && model.Error == "" {
		model.Error = err.Error()
	}
	for _, f := range files {
		model.Files = append(model.Files, view.ContextFile{
			Path: f.Path, Version: f.Version, Selected: f.Path == open,
		})
	}

	if open != "" {
		text, err := s.context.Get(r.Context(), open)
		if err != nil {
			if model.Error == "" {
				model.Error = err.Error()
			}
			model.Open = ""
		} else {
			model.Text = text
			// Read back rather than carried from the form: after a save the
			// version has moved, and echoing the old one would make the next
			// save fail for no reason the person could see.
			if v, _, err := s.context.Version(r.Context(), open); err == nil {
				model.OpenedAt = v
			}
		}
	}

	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		model.Searched, model.Query = true, q
		hits, err := s.context.Search(r.Context(), q, "", contextSearchLimit)
		if err != nil {
			if model.Error == "" {
				model.Error = err.Error()
			}
		} else {
			model.FilesScanned, model.FilesMatched = hits.Total, hits.Files
			model.Truncated, model.Matches = hits.Cut, len(hits.Hits)
			for _, m := range hits.Hits {
				model.Hits = append(model.Hits, view.ContextHit{
					Path: m.Path, Line: m.Line, Text: strings.TrimSpace(m.Text),
				})
			}
		}
	}

	s.renderPage(w, r, "cog.page.context", "cog.list.context", "Context", model)
}

// contextSearchLimit bounds one answer. A search that returned everything
// would be a page nobody can read; the model says when it was cut, which is
// the part that matters.
const contextSearchLimit = 100
