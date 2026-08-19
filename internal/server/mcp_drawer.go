package server

import (
	"net/http"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The MCP drawer: somebody else's tools, granted to an agent.
//
// Off unless asked for, and the default is the point. Everything else this
// product runs is either its own code or a gear whose complete source is here,
// versioned, approved line by line and run in a container. An MCP server is a
// command — the source is never seen, and the child runs on this host as this
// server's user.

func (s *Server) mcpModel(r *http.Request, problem, notice string) view.MCP {
	caller := callerFrom(r.Context())
	model := view.MCP{
		Ctx:     s.viewCtx(r, caller),
		IsAdmin: caller.IsAdmin(),
		Error:   problem,
		Notice:  notice,
	}
	// Nil means this install was never asked for external servers, which is
	// not the same as having none — and the screen says which.
	if s.mcp == nil {
		return model
	}
	model.Enabled = true

	servers, err := s.mcp.List(r.Context())
	if err != nil {
		model.Error = err.Error()
		return model
	}
	for _, srv := range servers {
		row := view.MCPServer{
			ID: srv.ID, Name: srv.Name, Status: srv.Status,
			Approved: srv.Status == "approved",
			Address:  srv.URL,
			// Hosted and packaged are genuinely different risks, and the
			// approval screen says four different things about them. A URL is
			// what tells them apart: a hosted server has one and runs nothing
			// here.
			Hosted: srv.URL != "",
			Kind:   kindOf(srv.URL),
		}
		if srv.Command != "" {
			row.Command = strings.TrimSpace(srv.Command + " " + strings.Join(srv.Args, " "))
		}
		row.Secrets = append(row.Secrets, srv.EnvNames...)
		for _, name := range srv.HeaderNames {
			// A header maps to a NAMED value, resolved at connect time, so no
			// credential is ever in the row — only which name it will ask for.
			row.Secrets = append(row.Secrets, name)
		}

		tools, err := s.mcp.Tools(r.Context(), srv.ID)
		if err == nil {
			for _, t := range tools {
				row.Tools = append(row.Tools, view.MCPTool{
					// The name an agent calls it by, which is the offered one
					// — a remote can call it anything, and what matters here
					// is what appears in a prompt.
					ID: t.ID, Name: t.OfferedName, Description: t.Description, Approved: t.Approved,
				})
				row.TotalTools++
				if t.Approved {
					row.ApprovedTools++
				}
			}
		}
		model.Servers = append(model.Servers, row)
	}
	return model
}

func kindOf(url string) string {
	if url != "" {
		return "hosted"
	}
	return "packaged"
}
