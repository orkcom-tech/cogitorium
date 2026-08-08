package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// toolsFor returns the tools an agent may use. The orchestrator manages the
// workspace; any agent wired to others may delegate to exactly those. Gears
// (agent-forged tools) join this list in Phase 5.
func (e *Engine) toolsFor(agent workspace.Agent, targets []workspace.Agent) []llm.Tool {
	var tools []llm.Tool

	if agent.IsOrchestrator {
		tools = append(tools,
			llm.Tool{
				Name:        "models_list",
				Description: "List the models available in the catalog. Use the returned model_name when creating or re-binding agents.",
				InputSchema: obj(map[string]any{}),
			},
			llm.Tool{
				Name:        "agent_list",
				Description: "List this workspace's agents with their roles and models.",
				InputSchema: obj(map[string]any{}),
			},
			llm.Tool{
				Name:        "agent_create",
				Description: "Create a new worker agent with a role (its system prompt) and a model from the catalog. You are automatically wired to it, so you can delegate to it right away.",
				InputSchema: obj(map[string]any{
					"name":  str("short unique agent name, e.g. 'researcher'"),
					"role":  str("the agent's system prompt: who it is, how it works, what it optimizes for"),
					"model": str("catalog model to bind: model_name (preferred), label, or 'provider / model_name'"),
				}, "name", "role", "model"),
			},
			llm.Tool{
				Name:        "agent_update",
				Description: "Update an existing agent's role and/or model.",
				InputSchema: obj(map[string]any{
					"name":  str("name of the agent to update"),
					"role":  str("new system prompt (omit to keep)"),
					"model": str("new catalog model (omit to keep)"),
				}, "name"),
			},
			llm.Tool{
				Name:        "wire_create",
				Description: "Wire one agent to another so the first may delegate to the second. Wires are the delegation capability: without one, delegation is refused.",
				InputSchema: obj(map[string]any{
					"from":  str("agent that gains the ability to delegate"),
					"to":    str("agent that may be delegated to"),
					"label": str("optional label describing the relationship"),
				}, "from", "to"),
			},
			llm.Tool{
				Name:        "context_list",
				Description: "List the files in the Contextverse space and this workspace's current context bindings (which file feeds which agent).",
				InputSchema: obj(map[string]any{}),
			},
			llm.Tool{
				Name:        "context_bind",
				Description: "Bind a Contextverse file into this workspace's context: to every agent (omit agent) or to one agent. Bound files are injected into the agent's system prompt.",
				InputSchema: obj(map[string]any{
					"path":  str("file path inside the context space, e.g. 'projects/foo/project.md'"),
					"agent": str("agent name to bind to; omit for workspace-wide"),
				}, "path"),
			},
			llm.Tool{
				Name:        "context_unbind",
				Description: "Remove a context binding (same path and scope as it was bound with).",
				InputSchema: obj(map[string]any{
					"path":  str("bound file path"),
					"agent": str("agent name the binding is scoped to; omit for the workspace-wide binding"),
				}, "path"),
			},
		)
	}

	if len(targets) > 0 {
		names := make([]string, 0, len(targets))
		for _, t := range targets {
			names = append(names, t.Name)
		}
		tools = append(tools, llm.Tool{
			Name: "delegate",
			Description: fmt.Sprintf(
				"Delegate a task to an agent you are wired to and get its answer back. Available: %s. Give the full task context — the agent sees only its role, its bound context, and your task text.",
				strings.Join(names, ", ")),
			InputSchema: obj(map[string]any{
				"agent": map[string]any{"type": "string", "description": "name of the agent to delegate to", "enum": names},
				"task":  str("the complete task, self-contained"),
			}, "agent", "task"),
		})
	}
	return tools
}

// execToolAs runs one tool call on behalf of an agent and returns
// (output, isError). Tool failures are results for the model to react to,
// not turn aborts.
func (e *Engine) execToolAs(ctx context.Context, wsID int64, agent workspace.Agent, chain []int64, call llm.ToolCall, emit func(Event)) (string, bool) {
	slog.Info("tool call", "workspace_id", wsID, "agent", agent.Name, "tool", call.Name, "input", call.InputJSON)
	e.setStatus(agent.ID, "working", call.Name, emit)

	out, err := e.dispatchTool(ctx, wsID, agent, chain, call, emit)
	if err != nil {
		slog.Warn("tool call failed", "workspace_id", wsID, "agent", agent.Name, "tool", call.Name, "err", err)
		return err.Error(), true
	}
	return out, false
}

func (e *Engine) dispatchTool(ctx context.Context, wsID int64, agent workspace.Agent, chain []int64, call llm.ToolCall, emit func(Event)) (string, error) {
	var args struct {
		Name  string `json:"name"`
		Role  string `json:"role"`
		Model string `json:"model"`
		Agent string `json:"agent"`
		Task  string `json:"task"`
		Path  string `json:"path"`
		From  string `json:"from"`
		To    string `json:"to"`
		Label string `json:"label"`
	}
	if call.InputJSON != "" {
		if err := json.Unmarshal([]byte(call.InputJSON), &args); err != nil {
			return "", fmt.Errorf("tool %s: arguments are not valid JSON: %w", call.Name, err)
		}
	}

	// Only the orchestrator manages the workspace; a worker that somehow
	// asks for a management tool gets a clear refusal.
	if !agent.IsOrchestrator && call.Name != "delegate" {
		return "", fmt.Errorf("tool %q is only available to the orchestrator", call.Name)
	}

	switch call.Name {
	case "models_list":
		models, err := e.cat.ListModels(ctx)
		if err != nil {
			return "", err
		}
		return marshal(models)

	case "agent_list":
		agents, err := e.ws.ListAgents(ctx, wsID)
		if err != nil {
			return "", err
		}
		return marshal(agents)

	case "agent_create":
		modelID, err := e.resolveModel(ctx, args.Model)
		if err != nil {
			return "", err
		}
		created, err := e.ws.CreateAgent(ctx, wsID, args.Name, args.Role, modelID)
		if err != nil {
			return "", err
		}
		// The creator gets the delegation capability automatically.
		if _, err := e.ws.CreateWire(ctx, wsID, agent.ID, created.ID, "created"); err != nil {
			return "", fmt.Errorf("agent %q created but wiring failed: %w", created.Name, err)
		}
		return marshal(created)

	case "agent_update":
		target, err := e.ws.GetAgentByName(ctx, wsID, args.Name)
		if err != nil {
			return "", err
		}
		var rolePtr *string
		if args.Role != "" {
			rolePtr = &args.Role
		}
		var modelPtr *int64
		if args.Model != "" {
			id, err := e.resolveModel(ctx, args.Model)
			if err != nil {
				return "", err
			}
			modelPtr = &id
		}
		updated, err := e.ws.UpdateAgent(ctx, target.ID, nil, rolePtr, modelPtr)
		if err != nil {
			return "", err
		}
		return marshal(updated)

	case "wire_create":
		from, err := e.ws.GetAgentByName(ctx, wsID, args.From)
		if err != nil {
			return "", err
		}
		to, err := e.ws.GetAgentByName(ctx, wsID, args.To)
		if err != nil {
			return "", err
		}
		wire, err := e.ws.CreateWire(ctx, wsID, from.ID, to.ID, args.Label)
		if err != nil {
			return "", err
		}
		return marshal(wire)

	case "delegate":
		if strings.TrimSpace(args.Task) == "" {
			return "", fmt.Errorf("delegate: task must not be empty")
		}
		return e.delegate(ctx, wsID, agent, chain, args.Agent, args.Task, emit)

	case "context_list":
		files, err := e.ctx.List(ctx)
		if err != nil {
			return "", err
		}
		bindings, err := e.ws.ListContextBindings(ctx, wsID)
		if err != nil {
			return "", err
		}
		return marshal(map[string]any{"space_files": files, "bindings": bindings})

	case "context_bind":
		agentID, err := e.bindScope(ctx, wsID, args.Agent)
		if err != nil {
			return "", err
		}
		// Verify the path actually exists in the space before binding.
		if _, err := e.ctx.Get(ctx, args.Path); err != nil {
			return "", err
		}
		b, err := e.ws.CreateContextBinding(ctx, wsID, args.Path, agentID)
		if err != nil {
			return "", err
		}
		return marshal(b)

	case "context_unbind":
		agentID, err := e.bindScope(ctx, wsID, args.Agent)
		if err != nil {
			return "", err
		}
		if err := e.ws.DeleteContextBindingByPath(ctx, wsID, args.Path, agentID); err != nil {
			return "", err
		}
		return `{"unbound": true}`, nil

	default:
		return "", fmt.Errorf("unknown tool %q", call.Name)
	}
}

// bindScope resolves an optional agent name to a binding scope (nil =
// workspace-wide).
func (e *Engine) bindScope(ctx context.Context, wsID int64, agentName string) (*int64, error) {
	if strings.TrimSpace(agentName) == "" {
		return nil, nil
	}
	a, err := e.ws.GetAgentByName(ctx, wsID, agentName)
	if err != nil {
		return nil, err
	}
	return &a.ID, nil
}

// resolveModel maps a model reference the orchestrator gives (model_name,
// label, or "provider / model_name") to a catalog id. Ambiguity and misses
// return an error listing what exists, so the model can self-correct.
func (e *Engine) resolveModel(ctx context.Context, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, fmt.Errorf("model reference is required")
	}
	models, err := e.cat.ListModels(ctx)
	if err != nil {
		return 0, err
	}

	var matches []int64
	for _, m := range models {
		display := m.ProviderName + " / " + m.ModelName
		if strings.EqualFold(m.ModelName, ref) || strings.EqualFold(m.Label, ref) || strings.EqualFold(display, ref) {
			matches = append(matches, m.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		available := make([]string, 0, len(models))
		for _, m := range models {
			available = append(available, m.ProviderName+" / "+m.ModelName)
		}
		return 0, fmt.Errorf("no catalog model matches %q; available: %s", ref, strings.Join(available, ", "))
	default:
		return 0, fmt.Errorf("model reference %q is ambiguous (%d matches) — use 'provider / model_name'", ref, len(matches))
	}
}

func marshal(v any) (string, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal tool result: %w", err)
	}
	return string(raw), nil
}
