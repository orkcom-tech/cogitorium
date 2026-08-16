package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/mcpclient"
	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// runMCPTool calls a tool on an external MCP server.
//
// The order of the first three steps is the security of the feature, and it is
// deliberately the same order runGear uses. The agent's own grant is checked
// FIRST, before anything is spawned — because here spawning IS the dangerous
// act. A model that names a tool it was never granted must not cause somebody
// else's binary to start on this host, even if the call is then refused.
func (e *Engine) runMCPTool(ctx context.Context, wsID int64, agent workspace.Agent, offered, argsJSON string) (string, error) {
	if e.mcp == nil {
		return "", fmt.Errorf("this install does not have external MCP servers switched on")
	}

	// 1. Is this agent allowed this exact tool? Re-checked from the store
	// rather than trusted from the list that was offered: the list was built at
	// the start of a turn that may have run for minutes, and a grant taken away
	// in between must take effect.
	allowed, err := e.mcp.ToolsForAgent(ctx, wsID, agent.ID)
	if err != nil {
		return "", err
	}
	var tool mcpstore.Tool
	for _, t := range allowed {
		if t.OfferedName == offered {
			tool = t
			break
		}
	}
	if tool.ID == 0 {
		return "", fmt.Errorf("%q is not a tool this agent was granted, so nothing was started", offered)
	}

	// 2. Is the server still approved, and is it still the command that was
	// approved? A mismatch refuses and returns it to pending.
	srv, err := e.mcp.Spawnable(ctx, tool.ServerID)
	if err != nil {
		return "", err
	}

	// 3. Only now does anything run.
	env, err := e.mcpEnv(ctx, wsID, srv)
	if err != nil {
		return "", err
	}
	slog.Warn("starting an external MCP server to answer a tool call",
		"server", srv.Name, "tool", tool.RemoteName, "agent", agent.Name,
		"workspace_id", wsID, "note", "it runs on this host with this server's file access")

	conn, err := mcpclient.Dial(ctx, mcpclient.Spec{
		Name: srv.Name, Command: srv.Command, Args: srv.Args, Dir: srv.Dir, Env: env,
		Timeout: time.Duration(srv.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		return "", err
	}
	// One call per connection in this cut. A pool is the obvious next step and
	// deliberately not here: a connection kept between calls is a process kept
	// alive between turns, and that is a lifetime question rather than an
	// optimisation.
	defer conn.Close()

	res, err := conn.CallTool(ctx, tool.RemoteName, json.RawMessage(argsJSON))
	if err != nil {
		return "", err
	}
	out := res.Text
	// What could not be carried is named in the answer itself, so the model
	// reports a partial result as partial rather than as the whole of it.
	if len(res.Dropped) > 0 {
		out += "\n\n[" + strings.Join(res.Dropped, ", ") +
			" content was returned by this tool and this install cannot carry it yet.]"
	}
	if res.IsError {
		// The tool's own failure, not the call's: the model is told plainly so
		// it can react, and the turn continues.
		return "the tool reported a failure: " + out, nil
	}
	return out, nil
}

// mcpEnv resolves the names this server was granted, in this workspace.
//
// Workspace-scoped for the same reason a gear's are: one workspace's
// credentials must not answer another's turn.
func (e *Engine) mcpEnv(ctx context.Context, wsID int64, srv mcpstore.Server) (map[string]string, error) {
	env := map[string]string{
		// Not inherited from this process, which holds provider keys. A child
		// still needs somewhere to write and a way to find its own tools.
		// A child needs a PATH to find its own interpreter — an MCP server run
		// through npx or uvx is a wrapper that looks one up. This process's
		// PATH is the machine's, not a credential, and is the only part of the
		// environment carried across.
		"PATH": os.Getenv("PATH"),
		"HOME": "/tmp",
	}
	if len(srv.EnvNames) == 0 || e.mcpSecrets == nil {
		return env, nil
	}
	values, err := e.mcpSecrets.Resolve(ctx, &wsID, srv.EnvNames)
	if err != nil {
		return nil, fmt.Errorf("the MCP server %q cannot be given what it was granted: %w", srv.Name, err)
	}
	for _, v := range values {
		env[v.Name] = v.Value
	}
	return env, nil
}

// SetMCP switches external MCP servers on. Called by the server only when the
// operator configured it; nil is the default and means the whole path is
// unreachable rather than merely unused.
func (e *Engine) SetMCP(store *mcpstore.Store, resolver *secrets.Resolver) {
	e.mcp = store
	e.mcpSecrets = resolver
}
