package server

import (
	"net/http"
	"strconv"
)

// Drawers: a screen the server renders, swapped into a page the client still
// owns.
//
// This is the seam the conversion is actually happening on. The workspace —
// its chat, its blueprint, its editor — is the application's, and converting
// all of it before anything inside it could be overridden would mean nothing
// was overridable for a long time. A drawer is a self-contained panel that
// crawls out over the work, which makes it exactly the right size to move
// first.
//
// So the client renders a container and this fills it. The panel is a
// template, goes through the composed stack, and a plugin overriding
// cog.drawer.gears changes what somebody sees inside a workspace — without
// this server having to own the workspace yet.

// handleWorkspaceDrawer renders one panel for the workspace it belongs to.
func (s *Server) handleWorkspaceDrawer(w http.ResponseWriter, r *http.Request) {
	wsID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "that is not a workspace", http.StatusBadRequest)
		return
	}

	// Named rather than derived from the path: a drawer is a fixed set this
	// server knows, and turning a URL segment into a template name would let
	// a request render anything the stack happens to define.
	switch r.PathValue("name") {
	case "instructions":
		s.renderDrawer(w, r, "cog.drawer.instructions", func() any {
			return s.instructionsModel(r, "")
		})
	case "gears":
		s.renderDrawer(w, r, "cog.drawer.gears", func() any {
			return s.gearsModel(r, "", "", nil)
		})
	case "variables":
		s.renderDrawer(w, r, "cog.drawer.variables", func() any {
			return s.envModel(r, &wsID, "")
		})
	case "queue":
		s.renderDrawer(w, r, "cog.drawer.queue", func() any {
			return s.queueModel(r, wsID, "", "")
		})
	case "context":
		// Admin-only, like the page: it reads and writes every document in the
		// install. What THIS workspace's agents read is Memory, on each agent,
		// which is a different drawer and a different question.
		if !callerFrom(r.Context()).IsAdmin() {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<p class="hint">The context space is an administrator's. ` +
				`What THIS workspace's agents read is in Memory, on each agent.</p>`))
			return
		}
		s.renderDrawer(w, r, "cog.drawer.context", func() any {
			return s.contextModel(r, "", "")
		})
	default:
		http.NotFound(w, r)
	}
}

// renderDrawer writes one panel, through the composed stack.
//
// Always a fragment: a drawer is never a document, so there is no shell branch
// here and no way for one to be added by accident.
func (s *Server) renderDrawer(w http.ResponseWriter, r *http.Request, name string, model func() any) {
	rt := s.plugins
	if rt == nil || rt.set == nil {
		http.Error(w, "this install has no composed templates", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := rt.set.Execute(w, name, model()); err != nil {
		// The header is already written, so there is nothing to say in a
		// status. The log is where this is answered.
		return
	}
}
