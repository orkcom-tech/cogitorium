package server

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/library"
	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The instruction library, served as a template rather than by the application.
//
// The first screen of the product a plugin can take over. Until now the
// template surface rendered plugin pages only, so "override a screen the core
// never designated as extensible" was a promise with nothing behind it: there
// was nothing to override.
//
// Four names rather than one — page, list, row, empty — because a plugin that
// wants a different row should not have to reproduce the page around it, and
// one that wants a different empty state should not have to own the list.
//
// The controls are ordinary HTML that works with JavaScript switched off: the
// search is a GET form, the delete is a POST that answers with the page.
// htmx makes each of them swap the list instead of reloading, and takes
// nothing away when it is absent — which is also what makes this testable
// without a browser.

// handleInstructionsPage renders the library.
func (s *Server) handleInstructionsPage(w http.ResponseWriter, r *http.Request) {
	s.renderInstructions(w, r, "")
}

// handleCreateInstructionForm is the write form's target.
//
// It answers with the page rather than redirecting, so a failure keeps what
// somebody typed on the screen beside the reason — a redirect would throw the
// text away and show them an empty form and a message.
func (s *Server) handleCreateInstructionForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderInstructions(w, r, "that form could not be read")
		return
	}
	text := r.PostFormValue("text")
	if strings.TrimSpace(text) == "" {
		s.renderInstructions(w, r, "an instruction with no text is a name and nothing else")
		return
	}
	name := r.PostFormValue("name")
	if err := library.ValidateName(name); err != nil {
		s.renderInstructions(w, r, err.Error())
		return
	}

	if err := s.context.Put(r.Context(), library.PathFor(name), text); err != nil {
		s.renderInstructions(w, r, err.Error())
		return
	}
	saved, err := s.library.Save(r.Context(), name, r.PostFormValue("description"),
		splitTags(r.PostFormValue("tags")), 0, 0)
	if err != nil {
		s.renderInstructions(w, r, err.Error())
		return
	}
	// Back to the instruction, open, rather than rendering the list in place.
	//
	// Two reasons and both are the person's: somebody who just edited one
	// wants to see what they saved, not a list with it collapsed; and a
	// rendered POST leaves the form's own URL in the address bar, so a refresh
	// re-submits it.
	http.Redirect(w, r, fmt.Sprintf("/instructions?open=%d", saved.ID), http.StatusSeeOther)
}

// handleDeleteInstructionForm removes one entry.
func (s *Server) handleDeleteInstructionForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderInstructions(w, r, "that is not an instruction")
		return
	}
	if err := s.library.Delete(r.Context(), id); err != nil {
		s.renderInstructions(w, r, err.Error())
		return
	}
	s.renderInstructions(w, r, "")
}

func (s *Server) renderInstructions(w http.ResponseWriter, r *http.Request, problem string) {
	s.renderPage(w, r, "cog.page.instructions", "cog.frag.library", "Instructions",
		s.instructionsModel(r, problem))
}

// instructionsModel is the page and the drawer's shared shape. Split out so a
// panel and a page cannot drift into showing different things.
func (s *Server) instructionsModel(r *http.Request, problem string) view.Instructions {
	caller := callerFrom(r.Context())
	q := r.URL.Query()

	model := view.Instructions{
		Ctx:      s.viewCtx(r, caller),
		Query:    q.Get("q"),
		Tag:      q.Get("tag"),
		Error:    problem,
		Narrowed: q.Get("q") != "" || q.Get("tag") != "",
	}
	// A POST carries its narrowing in the form rather than the URL, so what
	// comes back is the list somebody was looking at rather than all of it.
	if r.Method == http.MethodPost {
		model.Query, model.Tag = r.PostFormValue("q"), r.PostFormValue("tag")
		model.Narrowed = model.Query != "" || model.Tag != ""
	}

	items, err := s.library.List(r.Context(), model.Tag, model.Query)
	if err != nil {
		// Said on the page rather than as a status code: somebody is looking
		// at a screen, and a bare 500 tells them nothing about what to do.
		model.Error = err.Error()
	}

	// Every tag in the library, not only the ones surviving the filter — a
	// filter you cannot get out of because the option vanished is a trap.
	all, _ := s.library.List(r.Context(), "", "")
	model.Tags = tagsOf(all, model.Tag)

	open, _ := strconv.ParseInt(q.Get("open"), 10, 64)
	for _, in := range items {
		row := view.Instruction{
			ID: in.ID, Name: in.Name, Description: in.Description, Path: in.Path,
			Tags: in.Tags, UpdatedAt: in.UpdatedAt,
		}
		if in.ID == open && open != 0 {
			row.Open = true
			// The text lives in Contextverse and is fetched only for the row
			// being read: a list carrying every body would carry every version
			// of every instruction to draw a page of names.
			if body, err := s.context.Get(r.Context(), in.Path); err == nil {
				row.Text = body
			} else {
				row.Text = "Its text could not be read: " + err.Error()
			}
		}
		model.Items = append(model.Items, row)
	}

	// The list alone when htmx asked, the whole page otherwise. Rendering the
	// page and letting htmx cut the list out of it works and is wasteful: the
	// server would draw a form, a filter and a rail on every keystroke for a
	// client that throws all of it away.
	return model
}

// renderPage puts a template through the composed stack and into the shell.
//
// The one place a converted screen is turned into a document, so that adding
// the next screen is a model and a template rather than a handler that also
// has to remember the shell, the rail and the layer order.
//
// An htmx request gets the fragment alone: htmx asks for a piece and swaps it,
// and wrapping that piece in a whole document would make the browser parse a
// page to use one div of it.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, page, fragment, title string, model any) {
	rt := s.plugins
	if rt == nil || rt.set == nil {
		http.Error(w, "this install has no composed templates", http.StatusInternalServerError)
		return
	}

	// htmx asked for a piece, so it gets the piece. Both names go through the
	// same composed stack, so a plugin's override of the fragment is what
	// swaps in — a partial rendered outside the stack would be the one place
	// overrides quietly did not apply.
	name := page
	partial := r.Header.Get("HX-Request") == "true" && fragment != ""
	if partial {
		name = fragment
	}

	var body bytes.Buffer
	if err := rt.set.Execute(&body, name, model); err != nil {
		// A template that validated at boot and fails now is a bug worth
		// seeing rather than a blank region.
		http.Error(w, "this page could not be rendered", http.StatusInternalServerError)
		return
	}

	if partial {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body.Bytes())
		return
	}

	var out bytes.Buffer
	shell := view.Shell{
		Ctx:     s.viewCtx(r, callerFrom(r.Context())),
		AppHead: s.appHead(),
		Look:    s.viewCtx(r, callerFrom(r.Context())).Theme,
		// From what the checker already knows. Never a request: drawing a rail
		// must not depend on reaching the internet.
		UpdateWaiting: s.updates != nil && s.updates.Report().Any(),
		Title:         title,
		Body:          template.HTML(body.String()),
		Nav:           rt.navFor(r.URL.Path, callerFrom(r.Context()).IsAdmin()),
		Styles:        rt.styles,
		Scripts:       rt.scripts,
	}
	if err := rt.set.Execute(&out, "cog.shell.document", shell); err != nil {
		http.Error(w, "this page could not be rendered", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(out.Bytes())
}

func splitTags(raw string) []string {
	var out []string
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func tagsOf(items []library.Instruction, selected string) []view.Tag {
	seen := map[string]bool{}
	for _, in := range items {
		for _, t := range in.Tags {
			seen[t] = true
		}
	}
	out := make([]view.Tag, 0, len(seen))
	for t := range seen {
		out = append(out, view.Tag{Name: t, Selected: t == selected})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
