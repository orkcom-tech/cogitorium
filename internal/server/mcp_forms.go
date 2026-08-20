package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
)

// The MCP panel's write half, which did not exist either.
//
// "add it", "probe", "delete" and "approve" all posted to paths nothing
// served. An external MCP server is an administrator's decision — it runs a
// command on this machine or calls out to somebody else's — so every one of
// these is admin-only, exactly as the JSON handlers are.

func (s *Server) handleCreateMCPServerForm(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if s.mcp == nil {
		s.renderMCP(w, r, "external MCP servers are switched off on this install", "")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderMCP(w, r, "that form could not be read", "")
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	command := strings.TrimSpace(r.PostFormValue("command"))
	address := strings.TrimSpace(r.PostFormValue("address"))
	if command == "" && address == "" {
		// One or the other, never neither: a server is something this machine
		// starts, or something it calls.
		s.renderMCP(w, r, "say how to reach it: a command to run, or an address to call", "")
		return
	}

	server := mcpstore.Server{
		Name:        name,
		Description: r.PostFormValue("description"),
		Command:     command,
		// One argument per line, because a single string would have to be
		// split, and splitting is a shell's opinion about quoting rather than
		// the author's.
		Args:     lines(r.PostFormValue("args")),
		URL:      address,
		EnvNames: splitTags(r.PostFormValue("secrets")),
	}
	if address != "" {
		server.Transport = "streamable-http"
	}

	created, err := s.mcp.Install(r.Context(), server, &caller.ID)
	if err != nil {
		s.renderMCP(w, r, err.Error(), "")
		return
	}
	// Nothing it offers may be called yet: a server arrives with its tools
	// unapproved, and probing is how you find out what they are.
	s.renderMCP(w, r, "", created.Name+" is installed. Probe it to see what it offers; nothing it offers can be called until you approve it")
}

func (s *Server) handleProbeMCPServerForm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mcpAdminScoped(w, r)
	if !ok {
		return
	}
	found, err := s.probeMCP(r.Context(), id)
	if err != nil {
		s.renderMCP(w, r, err.Error(), "")
		return
	}
	s.renderMCP(w, r, "", "it answered: "+plural(len(found.Tools), "tool")+" offered, each waiting for your approval")
}

func (s *Server) handleDeleteMCPServerForm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.mcpAdminScoped(w, r)
	if !ok {
		return
	}
	if err := s.mcp.Delete(r.Context(), id); err != nil {
		s.renderMCP(w, r, err.Error(), "")
		return
	}
	s.renderMCP(w, r, "", "that server is gone, with every grant that pointed at it")
}

func (s *Server) handleApproveMCPToolForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if s.mcp == nil {
		s.renderMCP(w, r, "external MCP servers are switched off on this install", "")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderMCP(w, r, "that is not a tool", "")
		return
	}
	if err := s.mcp.ApproveTool(r.Context(), id, true); err != nil {
		s.renderMCP(w, r, err.Error(), "")
		return
	}
	s.renderMCP(w, r, "", "approved — it may now be called by an agent you grant it to")
}

func (s *Server) mcpAdminScoped(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if _, ok := requireAdmin(w, r); !ok {
		return 0, false
	}
	if s.mcp == nil {
		s.renderMCP(w, r, "external MCP servers are switched off on this install", "")
		return 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderMCP(w, r, "that is not a server", "")
		return 0, false
	}
	return id, true
}

func (s *Server) renderMCP(w http.ResponseWriter, r *http.Request, problem, notice string) {
	s.renderDrawer(w, r, "cog.drawer.mcp", func() any { return s.mcpModel(r, problem, notice) })
}

// lines splits a textarea into one value per line, dropping the blank ones
// somebody's trailing newline leaves behind.
func lines(raw string) []string {
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
