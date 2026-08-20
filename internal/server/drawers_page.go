package server

import (
	"github.com/orkcom-tech/cogitorium/internal/view"
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
	case "planboards":
		s.renderDrawer(w, r, "cog.drawer.planboards", func() any {
			return s.planboardsModel(r, "")
		})
	case "terminal":
		// The gate, never the session. What a template can render here is why
		// there is or is not a shell and what starting one costs — the live
		// PTY belongs to the socket.
		s.renderDrawer(w, r, "cog.drawer.terminal", func() any {
			return s.terminalModel(r)
		})
	case "memory":
		// The agent travels in the URL rather than being remembered here,
		// which keeps the panel a pure function of its request: a refresh, a
		// reload and a link all show the same thing.
		agentID, _ := strconv.ParseInt(r.URL.Query().Get("agent"), 10, 64)
		s.renderDrawer(w, r, "cog.drawer.memory", func() any {
			return s.memoryModel(r, agentID, r.URL.Query().Get("edit"))
		})
	case "agents":
		// The roster alone, for the poll. See agentsModel: a refresh that
		// replaced the panel would take the form with it.
		if r.URL.Query().Get("only") == "roster" {
			selected, _ := strconv.ParseInt(r.URL.Query().Get("selected"), 10, 64)
			s.renderDrawer(w, r, "cog.list.agents", func() any {
				return s.agentsModel(r, wsID, selected)
			})
			return
		}
		// The row somebody has open, so the roster comes back with it still
		// marked. Without it a poll every four seconds would clear the
		// selection under whoever was reading it.
		selected, _ := strconv.ParseInt(r.URL.Query().Get("selected"), 10, 64)
		s.renderDrawer(w, r, "cog.drawer.agents", func() any {
			return s.agentsModel(r, wsID, selected)
		})
	case "versions":
		s.renderDrawer(w, r, "cog.drawer.versions", func() any {
			return s.versionsModel(r, wsID, "", "", nil)
		})
	case "mcp":
		s.renderDrawer(w, r, "cog.drawer.mcp", func() any {
			return s.mcpModel(r, "", "")
		})
	case "receivers":
		s.renderDrawer(w, r, "cog.drawer.receivers", func() any {
			return s.inletsModel(r, wsID, "", "", nil)
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
	rt := s.pluginRT()
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

// handleStageSlot renders the strip a plugin may put above a canvas.
//
// The blueprint, the editor, the map and the terminal are the four screens
// this server does not render — they are drawn surfaces and a socket, and a
// template renders a thing that exists at a moment. This is how a plugin
// reaches them anyway: not by replacing them, which would be a lie, but by
// being given the space around them.
//
// Empty unless a plugin overrides cog.slot.stagehead, so an install with no
// plugins pays one request that returns nothing.
func (s *Server) handleStageSlot(w http.ResponseWriter, r *http.Request) {
	screen := r.URL.Query().Get("screen")
	// A closed list: this reaches a template as data, and a template is the one
	// place a stray value becomes markup on somebody else's screen.
	switch screen {
	case "chat", "blueprint", "workbench", "map", "terminal":
	default:
		screen = ""
	}
	wsID, _ := strconv.ParseInt(r.URL.Query().Get("ws"), 10, 64)
	if wsID != 0 && !s.requireWorkspace(w, r, wsID) {
		return
	}
	s.renderDrawer(w, r, "cog.slot.stagehead", func() any {
		return view.Slot{
			Ctx:       s.viewCtx(r, callerFrom(r.Context())),
			Screen:    screen,
			Workspace: wsID,
		}
	})
}
