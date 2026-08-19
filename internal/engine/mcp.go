package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/mcpclient"
	"github.com/orkcom-tech/cogitorium/internal/mcpoauth"
	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
	"github.com/orkcom-tech/cogitorium/internal/metrics"
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

	// 3. Only now does anything run, or anything leave.
	spec, err := e.mcpSpec(ctx, wsID, srv)
	if err != nil {
		return "", err
	}
	if spec.Remote() {
		slog.Warn("reaching an external MCP server to answer a tool call",
			"server", srv.Name, "tool", tool.RemoteName, "agent", agent.Name, "workspace_id", wsID,
			"url", srv.URL, "note", "this agent's arguments leave this machine, to a host the operator approved")
	} else {
		slog.Warn("starting an external MCP server to answer a tool call",
			"server", srv.Name, "tool", tool.RemoteName, "agent", agent.Name,
			"workspace_id", wsID, "note", "it runs on this host with this server's file access")
	}

	// Reused when one is already open for this exact server — see mcppool.go.
	// The key carries the fingerprint, so a server edited since is a different
	// server and gets a fresh connection rather than one opened under what it
	// used to be.
	conn, release, err := e.mcpPool.take(ctx, srv, spec)
	if err != nil {
		return "", err
	}
	defer release()

	started := time.Now()
	res, err := conn.CallTool(ctx, tool.RemoteName, json.RawMessage(argsJSON))
	if e.metrics != nil {
		// The TRANSPORT is a label and the server's name is not: which kind of
		// thing was reached is a closed set this codebase defines, and which
		// one is an integration an operator named.
		labels := map[string]string{"transport": spec.Transport, "outcome": metrics.Outcome(err)}
		if labels["transport"] == "" {
			labels["transport"] = mcpstore.TransportStdio
		}
		e.metrics.MCPCalls.Inc(labels)
		e.metrics.MCPSeconds.Observe(map[string]string{"transport": labels["transport"]}, time.Since(started).Seconds())
	}
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
// mcpSpec turns a stored row into something to connect with, resolving the
// NAMES it holds into values at the last possible moment.
//
// One function for both transports because the rule is the same on each: the
// row names a credential and never holds one, and the value is fetched here,
// used, and never written anywhere. Where it goes differs — a child's
// environment or an HTTP header — and that is the only difference.
func (e *Engine) mcpSpec(ctx context.Context, wsID int64, srv mcpstore.Server) (mcpclient.Spec, error) {
	spec := mcpclient.Spec{
		Name:      srv.Name,
		Transport: srv.Transport,
		Timeout:   time.Duration(srv.TimeoutSeconds) * time.Second,
	}
	switch srv.Transport {
	case mcpstore.TransportStreamableHTTP, mcpstore.TransportSSE:
		headers, err := e.mcpHeaders(ctx, wsID, srv)
		if err != nil {
			return mcpclient.Spec{}, err
		}
		spec.URL, spec.Headers = srv.URL, headers
	default:
		env, err := e.mcpEnv(ctx, wsID, srv)
		if err != nil {
			return mcpclient.Spec{}, err
		}
		spec.Command, spec.Args, spec.Dir, spec.Env = srv.Command, srv.Args, srv.Dir, env
	}
	return spec, nil
}

// mcpHeaders resolves the header NAMES a remote server was granted into the
// headers it is actually sent.
//
// The map is header name -> named value, so `{"Authorization": "JIRA_TOKEN"}`
// becomes `Authorization: <whatever JIRA_TOKEN resolves to>`. A name that
// resolves to nothing is left out rather than sent empty: an Authorization
// header with no value reads to the far end as a malformed request, and the
// operator gets a 401 that says nothing about the missing variable.
func (e *Engine) mcpHeaders(ctx context.Context, wsID int64, srv mcpstore.Server) (map[string]string, error) {
	if len(srv.HeaderNames) == 0 || e.env == nil {
		// Still gives the sign-in a chance: a server reached purely through
		// OAuth names no header at all.
		if e.mcpOAuth != nil {
			if token, err := e.mcpOAuth.Bearer(ctx, srv.ID); err == nil {
				return map[string]string{"Authorization": "Bearer " + token}, nil
			}
		}
		return map[string]string{}, nil
	}
	wanted := make([]string, 0, len(srv.HeaderNames))
	for _, named := range srv.HeaderNames {
		wanted = append(wanted, named)
	}
	values, err := e.env.Resolve(ctx, &wsID, wanted)
	if err != nil {
		return nil, fmt.Errorf("the MCP server %q cannot be given what it was granted: %w", srv.Name, err)
	}
	byName := map[string]string{}
	for _, v := range values {
		byName[v.Name] = v.Value
	}
	headers := map[string]string{}
	// A SIGN-IN WINS over a header by name, and is applied first so a
	// configured Authorization header can still override it deliberately. Most
	// servers have one or the other; a server that has both is one somebody set
	// up by hand and then signed in to, and their explicit header is the later
	// decision.
	if e.mcpOAuth != nil {
		switch token, err := e.mcpOAuth.Bearer(ctx, srv.ID); {
		case err == nil:
			headers["Authorization"] = "Bearer " + token
		case errors.Is(err, mcpoauth.ErrNoGrant):
			// Ordinary: this server was never signed in to.
		default:
			// A grant that exists and cannot be used is worth failing on rather
			// than falling through to an unauthenticated call that will be
			// refused with less explanation.
			return nil, fmt.Errorf("the MCP server %q is signed in but its token could not be used: %w",
				srv.Name, err)
		}
	}
	for header, named := range srv.HeaderNames {
		if v, ok := byName[named]; ok && v != "" {
			headers[header] = v
		} else {
			return nil, fmt.Errorf("the MCP server %q needs %s for its %s header, and this install has no such value",
				srv.Name, named, header)
		}
	}
	return headers, nil
}

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
	if len(srv.EnvNames) == 0 || e.env == nil {
		return env, nil
	}
	values, err := e.env.Resolve(ctx, &wsID, srv.EnvNames)
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
// SetMCPOAuth gives the engine the grants for remote MCP servers. Nil means no
// server was ever signed in to, which is most installs.
func (e *Engine) SetMCPOAuth(s *mcpoauth.Store) { e.mcpOAuth = s }

// SetMetrics gives the engine somewhere to publish. Called by the server; nil
// until then, so nothing here depends on it existing and every test and any
// embedding gets an engine that measures nothing rather than one that panics.
func (e *Engine) SetMetrics(m *metrics.Set) { e.metrics = m }

func (e *Engine) SetMCP(store *mcpstore.Store, resolver *secrets.Resolver) {
	e.mcp = store
	// The same resolver SetSecrets holds. Assigned again rather than skipped,
	// so an embedding that wires MCP and nothing else still resolves names.
	e.env = resolver
}

// The two tools a server's documents and prompt templates reach the model
// through — see tools.go for why it is two rather than one per item.
const (
	mcpReadTool   = "mcp_documents"
	mcpPromptTool = "mcp_prompts"
)

// runMCPRead lists or reads the documents this agent's granted servers hold.
//
// It walks EVERY granted server rather than asking the agent to name one. A
// document uri is globally unique in practice and the agent has no way to know
// which server holds which — making it guess would turn one call into several,
// each of which spawns or dials.
func (e *Engine) runMCPRead(ctx context.Context, wsID int64, agent workspace.Agent, argsJSON string) (string, error) {
	var in struct {
		URI string `json:"uri"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &in)

	servers, err := e.grantedServers(ctx, wsID, agent)
	if err != nil {
		return "", err
	}
	var listing []string
	for _, srv := range servers {
		conn, release, err := e.openMCP(ctx, wsID, srv)
		if err != nil {
			// One unreachable server must not deny the agent the others.
			slog.Warn("an MCP server could not be reached for its documents",
				"server", srv.Name, "workspace_id", wsID, "err", err)
			continue
		}
		if in.URI == "" {
			found, capped, err := conn.Resources(ctx, mcpstore.MaxToolsPerServer)
			release()
			if err != nil {
				continue
			}
			for _, r := range found {
				line := r.URI
				if r.Name != "" {
					line += " — " + r.Name
				}
				if r.Description != "" {
					line += ": " + r.Description
				}
				listing = append(listing, line)
			}
			if capped {
				listing = append(listing, "(the list from "+srv.Name+" was cut short)")
			}
			continue
		}
		res, err := conn.ReadResource(ctx, in.URI)
		release()
		if err != nil {
			continue
		}
		out := res.Text
		if len(res.Dropped) > 0 {
			out += "\n\n[" + strings.Join(res.Dropped, ", ") + " was returned and this install cannot carry it.]"
		}
		return out, nil
	}
	if in.URI != "" {
		return "", fmt.Errorf("no MCP server this agent was granted holds %q", in.URI)
	}
	if len(listing) == 0 {
		return "None of the MCP servers you were granted offers any documents.", nil
	}
	return strings.Join(listing, "\n"), nil
}

// runMCPPrompt lists or renders the templates this agent's granted servers
// offer.
func (e *Engine) runMCPPrompt(ctx context.Context, wsID int64, agent workspace.Agent, argsJSON string) (string, error) {
	var in struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &in)

	servers, err := e.grantedServers(ctx, wsID, agent)
	if err != nil {
		return "", err
	}
	var listing []string
	for _, srv := range servers {
		conn, release, err := e.openMCP(ctx, wsID, srv)
		if err != nil {
			slog.Warn("an MCP server could not be reached for its prompts",
				"server", srv.Name, "workspace_id", wsID, "err", err)
			continue
		}
		if in.Name == "" {
			found, _, err := conn.Prompts(ctx, mcpstore.MaxToolsPerServer)
			release()
			if err != nil {
				continue
			}
			for _, p := range found {
				line := p.Name
				if p.Description != "" {
					line += ": " + p.Description
				}
				listing = append(listing, line)
			}
			continue
		}
		res, err := conn.GetPrompt(ctx, in.Name, in.Arguments)
		release()
		if err != nil {
			continue
		}
		return res.Text, nil
	}
	if in.Name != "" {
		return "", fmt.Errorf("no MCP server this agent was granted offers the prompt %q", in.Name)
	}
	if len(listing) == 0 {
		return "None of the MCP servers you were granted offers any prompt templates.", nil
	}
	return strings.Join(listing, "\n"), nil
}

// grantedServers is the distinct set of servers behind this agent's granted
// tools.
//
// Derived from the TOOLS rather than from the bindings, deliberately: the tool
// query is the one that already applies every gate — the server is approved,
// the binding reaches this agent, the tool itself is approved — and a second
// path to "which servers may this agent reach" would be a second place for
// those gates to drift apart.
func (e *Engine) grantedServers(ctx context.Context, wsID int64, agent workspace.Agent) ([]mcpstore.Server, error) {
	if e.mcp == nil {
		return nil, errors.New("external MCP servers are not switched on for this install")
	}
	tools, err := e.mcp.ToolsForAgent(ctx, wsID, agent.ID)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var out []mcpstore.Server
	for _, t := range tools {
		if seen[t.ServerID] {
			continue
		}
		seen[t.ServerID] = true
		srv, err := e.mcp.Spawnable(ctx, t.ServerID)
		if err != nil {
			continue
		}
		out = append(out, srv)
	}
	return out, nil
}

// openMCP resolves a server's named values and takes a pooled connection.
func (e *Engine) openMCP(ctx context.Context, wsID int64, srv mcpstore.Server) (*mcpclient.Conn, func(), error) {
	spec, err := e.mcpSpec(ctx, wsID, srv)
	if err != nil {
		return nil, nil, err
	}
	return e.mcpPool.take(ctx, srv, spec)
}
