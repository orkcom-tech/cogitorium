package server

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/mcpcatalog"
	"github.com/orkcom-tech/cogitorium/internal/mcpclient"
	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
	"github.com/orkcom-tech/cogitorium/internal/update"
)

// The operator's side of external MCP servers.
//
// Every write here is admin-only, and there is no agent-reachable path to any
// of it. That is not a convention — it is the whole boundary. An external MCP
// server is a command this install never saw the source of, running on the host
// with the server's own file access, so the one thing that must never happen is
// an agent talking its way into installing, approving or granting one.
//
// The three acts are separate on purpose and in this order: install (it exists,
// pending), probe (ask it what it offers, without giving it anything), approve
// the server and each tool it reported, then grant it to a workspace or an
// agent. An operator who only wanted to look has done the first two.

func (s *Server) mcpOff(w http.ResponseWriter) bool {
	if s.mcp == nil {
		writeError(w, http.StatusNotFound, "external MCP servers are not switched on for this install: "+
			"set mcp_clients: true in the configuration. It runs code this install never saw the source "+
			"of, on this host, so it is off unless asked for")
		return true
	}
	return false
}

// handleListMCPServers lists what is installed, REDACTED FOR ANYBODY BUT AN
// ADMINISTRATOR.
//
// This route was open and unredacted, which was survivable while nothing in the
// interface called it: an operator had to know the endpoint existed. Putting it
// in a drawer turns "reachable by curl" into "on everybody's screen", and what
// it carries is a full command line — `npx -y @acme/jira-mcp --site
// acme.atlassian.net` — plus the NAMES of every credential that server is
// handed. Neither is a value, and both are a map of this install's integrations
// and internal hostnames drawn for anybody with a login.
//
// So a member sees that a server exists, what it is called, what it is for and
// whether it is approved — everything needed to understand why an agent has a
// tool — and nothing about how it is spawned.
func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	servers, err := s.mcp.List(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if !callerFrom(r.Context()).IsAdmin() {
		for i := range servers {
			servers[i] = redactServer(servers[i])
		}
	}
	writeJSON(w, http.StatusOK, servers)
}

// redactServer removes what a non-administrator has no business reading.
//
// Blanked rather than omitted: a field that disappears makes a client guess
// whether it is absent or empty, and the one thing this must not do is let a
// reader mistake "you may not see this" for "there is nothing here". The
// fingerprint goes too — it is a hash OVER the command, and publishing a hash
// of a secret-ish string to everyone is a smaller version of the same mistake.
func redactServer(srv mcpstore.Server) mcpstore.Server {
	srv.Command = ""
	srv.Args = []string{}
	srv.Dir = ""
	srv.EnvNames = []string{}
	srv.Fingerprint = ""
	return srv
}

// handleMCPCatalog is the library: servers an operator adds by choosing rather
// than by knowing an npm package name.
//
// Admin-only, like everything else about MCP, and for the same reason the write
// routes are: this list's whole purpose is to be installed FROM, and a
// catalogue anybody can install from is a catalogue that spawns subprocesses on
// this server and sends this install's credentials to hosts it names.
//
// GATED ON THE UPDATE-CHECK CONSENT, which is the answer to the objection that
// stopped this being built the first time. An install that has not agreed to
// make outbound requests does not acquire a catalogue that does — it is told
// there is no library and why, and `add by hand` is untouched because that
// never left the machine.
func (s *Server) handleMCPCatalog(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if s.updates.Mode() == update.ModeOff {
		writeError(w, http.StatusConflict,
			"the library is the published MCP registry, read live, and this install is configured not to make "+
				"outbound requests (update_check: off). Add a server by hand instead — that reaches nothing.")
		return
	}
	entries, err := s.mcpLibrary.Search(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		// 502, not 500: the failure is somebody else's host, and an operator
		// reading "internal server error" would go looking in the wrong logs.
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		// Said once, here, so the interface renders the same sentence the
		// review screen does rather than inventing a softer one.
		"fetched_at_spawn": mcpcatalog.FetchedAtSpawn,
	})
}

func (s *Server) handleInstallMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var in CreateMCPServerBody
	if !decodeJSON(w, r, &in) {
		return
	}
	srv, err := s.mcp.Install(r.Context(), mcpstore.Server{
		Name: in.Name, Description: in.Description, Transport: in.Transport,
		Command: in.Command, Args: in.Args, Dir: in.Dir, EnvNames: in.EnvNames,
		URL: in.URL, HeaderNames: in.HeaderNames, TimeoutSeconds: in.TimeoutSeconds,
	}, &caller.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, srv)
}

func (s *Server) handleUpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in UpdateMCPServerBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Status == nil && in.Command == nil && in.Args == nil && in.EnvNames == nil &&
		in.Description == nil && in.Dir == nil && in.TimeoutSeconds == nil &&
		in.Transport == nil && in.URL == nil && in.HeaderNames == nil {
		writeError(w, http.StatusBadRequest,
			"nothing to change: send status, or any of transport, command, args, cwd, env_names, "+
				"url, header_names, description, timeout_seconds")
		return
	}

	// The edit lands before the status, and never both in one act. Everything
	// editable here is inside the fingerprint, so an edit returns the server to
	// pending — and an operator who sent an edit AND an approval in one request
	// would be approving what they had just changed without seeing it.
	srv, err := s.mcp.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	edited := in.Status == nil
	if edited {
		next := srv
		if in.Description != nil {
			next.Description = *in.Description
		}
		if in.Command != nil {
			next.Command = *in.Command
		}
		if in.Args != nil {
			next.Args = *in.Args
		}
		if in.Dir != nil {
			next.Dir = *in.Dir
		}
		if in.EnvNames != nil {
			next.EnvNames = *in.EnvNames
		}
		if in.Transport != nil {
			next.Transport = *in.Transport
		}
		if in.URL != nil {
			next.URL = *in.URL
		}
		if in.HeaderNames != nil {
			next.HeaderNames = *in.HeaderNames
		}
		if in.TimeoutSeconds != nil {
			next.TimeoutSeconds = *in.TimeoutSeconds
		}
		if srv, err = s.mcp.Update(r.Context(), id, next); err != nil {
			fail(w, r, err)
			return
		}
	} else {
		if srv, err = s.mcp.SetStatus(r.Context(), id, *in.Status); err != nil {
			fail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, srv)
}

func (s *Server) handleDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.mcp.Delete(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleProbeMCPServer starts the server once and asks what it offers.
//
// This is the one place a pending server runs, and it is what an operator has
// instead of source to read. It is given NO named values and no grant: the
// question being asked is "what does this claim to be", and a server that needs
// a credential to answer it is a server to be suspicious of.
//
// Every tool it reports arrives unapproved, including ones seen before that
// have changed — so a server that grows a tool after approval has grown an
// inert one.
func (s *Server) handleProbeMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	srv, err := s.mcp.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	spec := mcpclient.Spec{
		Name: srv.Name, Transport: srv.Transport, Timeout: 30 * time.Second,
	}
	if spec.Remote() {
		slog.Warn("an operator is probing a remote MCP server: this install will contact it, unapproved",
			"server", srv.Name, "url", srv.URL)
		spec.URL = srv.URL
		// Deliberately no headers: see above. A server that will not say what
		// it offers without a credential is one to be suspicious of, and the
		// answer to "what is this" must not cost a token before the operator
		// has agreed to anything.
		spec.Headers = map[string]string{}
	} else {
		slog.Warn("an operator is probing an external MCP server: it will run on this host, unapproved",
			"server", srv.Name, "command", srv.Command)
		spec.Command, spec.Args, spec.Dir = srv.Command, srv.Args, srv.Dir
		// Deliberately nothing: see above.
		spec.Env = map[string]string{"PATH": osPath(), "HOME": "/tmp"}
	}

	conn, err := mcpclient.Dial(r.Context(), spec)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer conn.Close()

	tools, capped, err := conn.Tools(r.Context(), mcpstore.MaxToolsPerServer)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := s.mcp.RecordTools(r.Context(), id, tools); err != nil {
		fail(w, r, err)
		return
	}
	recorded, err := s.mcp.Tools(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tools": recorded,
		// Said rather than silently applied: an operator who cannot see the tool
		// they were looking for should know the list was cut.
		"capped":     capped,
		"cap":        mcpstore.MaxToolsPerServer,
		"identified": srv.Name,
	})
}

func (s *Server) handleListMCPTools(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tools, err := s.mcp.Tools(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tools)
}

func (s *Server) handleApproveMCPTool(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in ApproveMCPToolBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.mcp.ApproveTool(r.Context(), id, in.Approved); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListMCPBindings(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	bindings, err := s.mcp.Bindings(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bindings)
}

// handleCreateMCPBinding grants a server to a workspace or to one agent in it.
//
// Admin-only, unlike a gear binding. A gear's source is in this install and an
// operator read it to approve it; granting one is a decision inside a workspace
// somebody already has. An MCP server is a host process, and handing it to an
// agent is closer to the approval than to the wiring.
func (s *Server) handleCreateMCPBinding(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	var in CreateMCPBindingBody
	if !decodeJSON(w, r, &in) {
		return
	}
	b, err := s.mcp.Bind(r.Context(), in.ServerID, id, in.AgentID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

// handleDeleteMCPBinding is scoped to the binding's own workspace, so a member
// of one cannot remove another's grant by guessing an id.
func (s *Server) handleDeleteMCPBinding(w http.ResponseWriter, r *http.Request) {
	if s.mcpOff(w) {
		return
	}
	id, ok := s.nestedScoped(w, r, func(bindingID int64) (int64, error) {
		return s.mcp.WorkspaceOfBinding(r.Context(), bindingID)
	})
	if !ok {
		return
	}
	if err := s.mcp.Unbind(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// osPath is the machine's PATH, which a child needs to find its own interpreter
// and which is not a credential. Nothing else of this process's environment is
// passed on.
func osPath() string { return os.Getenv("PATH") }
