// Package engine runs workspace turns: the operator talks to the
// orchestrator, the orchestrator thinks, calls its built-in tools (inspect
// the catalog, create/configure agents, delegate tasks), and every step is
// persisted to the workspace timeline and streamed out as events.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

const (
	maxToolIterations = 16
	// maxDelegationDepth bounds how deep the blueprint graph is walked in
	// one turn. Cycles are additionally blocked by the visited chain.
	maxDelegationDepth = 4
)

type AgentStatus struct {
	AgentID int64  `json:"agent_id"`
	State   string `json:"state"` // idle | thinking | working | responding
	Detail  string `json:"detail"`
	Since   string `json:"since"`
}

// Event is one streamed occurrence during a workspace turn.
type Event struct {
	Type    string             `json:"type"` // message | delta | status | done | error
	Message *workspace.Message `json:"message,omitempty"`
	AgentID int64              `json:"agent_id,omitempty"`
	Text    string             `json:"text,omitempty"`
	Status  *AgentStatus       `json:"status,omitempty"`
	Error   string             `json:"error,omitempty"`
}

type Engine struct {
	ws       *workspace.Store
	cat      *catalog.Store
	ctx      *contextstore.Store
	gears    *gear.Store
	gearExec *gear.Executor

	mu      sync.Mutex
	status  map[int64]AgentStatus
	running map[int64]bool // workspace_id -> a turn is in flight
}

func New(ws *workspace.Store, cat *catalog.Store, cs *contextstore.Store, gears *gear.Store, gearExec *gear.Executor) *Engine {
	return &Engine{
		ws:       ws,
		cat:      cat,
		ctx:      cs,
		gears:    gears,
		gearExec: gearExec,
		status:   map[int64]AgentStatus{},
		running:  map[int64]bool{},
	}
}

// Statuses returns the live status of every agent in a workspace (agents
// with no recorded activity are idle).
func (e *Engine) Statuses(ctx context.Context, wsID int64) ([]AgentStatus, error) {
	agents, err := e.ws.ListAgents(ctx, wsID)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]AgentStatus, 0, len(agents))
	for _, a := range agents {
		if st, ok := e.status[a.ID]; ok {
			out = append(out, st)
			continue
		}
		out = append(out, AgentStatus{AgentID: a.ID, State: "idle"})
	}
	return out, nil
}

func (e *Engine) setStatus(agentID int64, state, detail string, emit func(Event)) {
	st := AgentStatus{AgentID: agentID, State: state, Detail: detail, Since: time.Now().UTC().Format(time.RFC3339)}
	e.mu.Lock()
	e.status[agentID] = st
	e.mu.Unlock()
	slog.Info("agent status", "agent_id", agentID, "state", state, "detail", detail)
	emit(Event{Type: "status", Status: &st})
}

// HandleUserMessage runs one full orchestrator turn. Every emitted event has
// already been persisted where it needs to be — emit is display, the
// timeline is truth.
func (e *Engine) HandleUserMessage(ctx context.Context, wsID int64, text string, emit func(Event)) error {
	e.mu.Lock()
	if e.running[wsID] {
		e.mu.Unlock()
		return errors.New("a turn is already in progress in this workspace")
	}
	e.running[wsID] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, wsID)
		e.mu.Unlock()
	}()

	orch, err := e.orchestrator(ctx, wsID)
	if err != nil {
		return err
	}

	userMsg, err := e.ws.AppendMessage(ctx, wsID, nil, "user", text, "")
	if err != nil {
		return err
	}
	emit(Event{Type: "message", Message: &userMsg})

	history, err := e.buildHistory(ctx, wsID, orch.ID)
	if err != nil {
		return err
	}

	defer e.setStatus(orch.ID, "idle", "", emit)

	for iter := 0; iter < maxToolIterations; iter++ {
		res, err := e.modelTurn(ctx, wsID, orch, history, emit)
		if err != nil {
			e.persistError(ctx, wsID, orch.ID, err, emit)
			return nil // the error is on the timeline; the HTTP turn itself succeeded
		}

		asstMsg, err := e.persistAssistant(ctx, wsID, orch.ID, res, emit)
		if err != nil {
			return err
		}
		_ = asstMsg

		if res.StopReason != llm.StopToolUse || len(res.ToolCalls) == 0 {
			if res.StopReason == "max_tokens" || res.StopReason == "length" {
				e.persistError(ctx, wsID, orch.ID, fmt.Errorf("orchestrator reply truncated by token limit (%s)", res.StopReason), emit)
			}
			emit(Event{Type: "done"})
			return nil
		}

		// Execute the tools and feed results back. Results are persisted
		// with a cancellation-immune context: once the assistant row with
		// tool_calls is on the timeline, its results MUST land too, or the
		// replayed history has orphaned tool_use blocks that providers
		// reject on every subsequent turn.
		persistCtx := context.WithoutCancel(ctx)
		var results []llm.ToolResult
		for _, call := range res.ToolCalls {
			out, isErr := e.execToolAs(ctx, wsID, orch, nil, call, emit)
			results = append(results, llm.ToolResult{CallID: call.ID, Name: call.Name, Content: out, IsError: isErr})

			meta, _ := json.Marshal(map[string]any{"call_id": call.ID, "name": call.Name, "is_error": isErr})
			trMsg, err := e.ws.AppendMessage(persistCtx, wsID, &orch.ID, "tool_result", out, string(meta))
			if err != nil {
				return err
			}
			emit(Event{Type: "message", Message: &trMsg})
		}

		history = append(history,
			llm.Turn{Role: "assistant", Text: res.Text, ToolCalls: res.ToolCalls},
			llm.Turn{Role: "user", ToolResults: results},
		)
	}

	e.persistError(ctx, wsID, orch.ID, fmt.Errorf("stopped after %d tool iterations without a final answer", maxToolIterations), emit)
	emit(Event{Type: "done"})
	return nil
}

func (e *Engine) orchestrator(ctx context.Context, wsID int64) (workspace.Agent, error) {
	agents, err := e.ws.ListAgents(ctx, wsID)
	if err != nil {
		return workspace.Agent{}, err
	}
	for _, a := range agents {
		if a.IsOrchestrator {
			return a, nil
		}
	}
	return workspace.Agent{}, fmt.Errorf("workspace %d has no orchestrator: %w", wsID, workspace.ErrNotFound)
}

// modelTurn runs one streamed model call for an agent.
func (e *Engine) modelTurn(ctx context.Context, wsID int64, agent workspace.Agent, history []llm.Turn, emit func(Event)) (llm.Result, error) {
	if agent.ModelID == nil {
		return llm.Result{}, fmt.Errorf("agent %q has no model bound", agent.Name)
	}
	model, err := e.cat.GetModel(ctx, *agent.ModelID)
	if err != nil {
		return llm.Result{}, fmt.Errorf("agent %q: %w", agent.Name, err)
	}
	client, _, err := e.cat.Client(ctx, model.ProviderID)
	if err != nil {
		return llm.Result{}, err
	}

	system, err := e.systemPrompt(ctx, wsID, agent)
	if err != nil {
		return llm.Result{}, err
	}
	targets, err := e.ws.DelegationTargets(ctx, wsID, agent.ID)
	if err != nil {
		return llm.Result{}, err
	}
	gears, err := e.gears.ForAgent(ctx, wsID, agent.ID)
	if err != nil {
		return llm.Result{}, err
	}

	e.setStatus(agent.ID, "thinking", "", emit)
	res, err := client.Chat(ctx, llm.Request{
		Model:    model.ModelName,
		System:   system,
		Messages: history,
		Tools:    e.toolsFor(agent, targets, gears),
	}, func(text string) error {
		emit(Event{Type: "delta", AgentID: agent.ID, Text: text})
		return ctx.Err()
	})
	if err != nil {
		return llm.Result{}, fmt.Errorf("model call for %q failed: %w", agent.Name, err)
	}
	slog.Info("model turn finished", "workspace_id", wsID, "agent", agent.Name, "stop_reason", res.StopReason, "tool_calls", len(res.ToolCalls))
	return res, nil
}

// systemPrompt assembles an agent's effective system prompt: its role, the
// workspace snapshot (orchestrator only), and its bound context documents
// fetched live from Contextverse.
func (e *Engine) systemPrompt(ctx context.Context, wsID int64, agent workspace.Agent) (string, error) {
	var b []byte
	b = append(b, agent.Role...)

	if agent.IsOrchestrator {
		ws, err := e.ws.GetWorkspace(ctx, wsID)
		if err != nil {
			return "", err
		}
		agents, err := e.ws.ListAgents(ctx, wsID)
		if err != nil {
			return "", err
		}
		b = fmt.Appendf(b, "\n\n## Workspace snapshot\nWorkspace: %s", ws.Name)
		if ws.Description != "" {
			b = fmt.Appendf(b, " — %s", ws.Description)
		}
		b = fmt.Appendf(b, "\nAgents:\n")
		for _, a := range agents {
			kind := ""
			if a.IsOrchestrator {
				kind = " (you)"
			}
			model := a.ModelLabel
			if model == "" {
				model = "no model bound"
			}
			role := a.Role
			if len(role) > 200 {
				role = role[:200] + "…"
			}
			b = fmt.Appendf(b, "- %s%s — model: %s — role: %s\n", a.Name, kind, model, role)
		}
	}

	paths, err := e.ws.BindingsForAgent(ctx, wsID, agent.ID)
	if err != nil {
		return "", err
	}
	if len(paths) > 0 {
		b = fmt.Appendf(b, "\n\n## Context (from Contextverse)\n")
		for _, p := range paths {
			content, err := e.ctx.Get(ctx, p)
			if err != nil {
				return "", fmt.Errorf("context doc %q bound to agent %q cannot be read: %w — restore the file in Contextverse or unbind it from the agent", p, agent.Name, err)
			}
			b = fmt.Appendf(b, "\n### %s\n%s\n", p, content)
		}
	}
	return string(b), nil
}

// AssembledPrompt exposes the effective system prompt for the UI's
// "what does this agent see" preview.
func (e *Engine) AssembledPrompt(ctx context.Context, agentID int64) (string, error) {
	agent, err := e.ws.GetAgent(ctx, agentID)
	if err != nil {
		return "", err
	}
	return e.systemPrompt(ctx, agent.WorkspaceID, agent)
}

func (e *Engine) persistAssistant(ctx context.Context, wsID, agentID int64, res llm.Result, emit func(Event)) (workspace.Message, error) {
	meta := "{}"
	// Tool calls are persisted only when they will actually be executed
	// (stop_reason tool_use). A turn cut by the token limit can carry a
	// truncated, invalid tool call — storing it would poison every future
	// replay of this timeline.
	if len(res.ToolCalls) > 0 && res.StopReason == llm.StopToolUse {
		raw, err := json.Marshal(map[string]any{"tool_calls": res.ToolCalls})
		if err != nil {
			return workspace.Message{}, err
		}
		meta = string(raw)
	} else if len(res.ToolCalls) > 0 {
		slog.Warn("dropping unexecuted tool calls from persisted turn", "workspace_id", wsID, "stop_reason", res.StopReason, "calls", len(res.ToolCalls))
	}
	msg, err := e.ws.AppendMessage(context.WithoutCancel(ctx), wsID, &agentID, "assistant", res.Text, meta)
	if err != nil {
		return workspace.Message{}, err
	}
	emit(Event{Type: "message", Message: &msg})
	return msg, nil
}

func (e *Engine) persistError(ctx context.Context, wsID, agentID int64, cause error, emit func(Event)) {
	slog.Error("workspace turn error", "workspace_id", wsID, "agent_id", agentID, "err", cause)
	msg, err := e.ws.AppendMessage(context.WithoutCancel(ctx), wsID, &agentID, "error", cause.Error(), "")
	if err != nil {
		slog.Error("could not persist error message", "err", err)
		emit(Event{Type: "error", Error: cause.Error()})
		return
	}
	emit(Event{Type: "message", Message: &msg})
}

// buildHistory reconstructs the orchestrator's conversation from the
// timeline and repairs it into something every provider accepts. The
// timeline is an append-only record of what happened — including turns that
// were interrupted, truncated, or failed — so replay must enforce the
// protocol invariants the providers check: every tool_use paired with a
// tool_result, valid call input JSON, alternating roles, and a history that
// starts at an operator message.
func (e *Engine) buildHistory(ctx context.Context, wsID, orchID int64) ([]llm.Turn, error) {
	msgs, err := e.ws.ListMessages(ctx, wsID, nil, 500)
	if err != nil {
		return nil, err
	}

	var turns []llm.Turn
	var pendingResults []llm.ToolResult
	flushResults := func() {
		if len(pendingResults) > 0 {
			turns = append(turns, llm.Turn{Role: "user", ToolResults: pendingResults})
			pendingResults = nil
		}
	}

	for _, m := range msgs {
		switch {
		case m.Kind == "user" && m.AgentID == nil:
			flushResults()
			if m.Content != "" {
				turns = append(turns, llm.Turn{Role: "user", Text: m.Content})
			}
		case m.Kind == "assistant" && m.AgentID != nil && *m.AgentID == orchID:
			flushResults()
			var meta struct {
				ToolCalls []llm.ToolCall `json:"tool_calls"`
			}
			if err := json.Unmarshal([]byte(m.Meta), &meta); err != nil {
				slog.Warn("bad assistant meta in timeline", "message_id", m.ID, "err", err)
			}
			if m.Content == "" && len(meta.ToolCalls) == 0 {
				continue // providers reject empty turns
			}
			turns = append(turns, llm.Turn{Role: "assistant", Text: m.Content, ToolCalls: meta.ToolCalls})
		case m.Kind == "tool_result" && m.AgentID != nil && *m.AgentID == orchID:
			var meta struct {
				CallID  string `json:"call_id"`
				Name    string `json:"name"`
				IsError bool   `json:"is_error"`
			}
			if err := json.Unmarshal([]byte(m.Meta), &meta); err != nil {
				slog.Warn("bad tool_result meta in timeline", "message_id", m.ID, "err", err)
				continue
			}
			pendingResults = append(pendingResults, llm.ToolResult{CallID: meta.CallID, Name: meta.Name, Content: m.Content, IsError: meta.IsError})
		}
	}
	flushResults()
	return repairHistory(turns), nil
}

// repairHistory enforces the provider invariants on a raw replay: pairs
// every tool call with a result (synthesizing an "interrupted" error result
// when the recorded one is missing), drops calls whose input is not valid
// JSON and results that answer no call, merges consecutive same-role text
// turns, and trims the front so history starts at an operator message.
func repairHistory(raw []llm.Turn) []llm.Turn {
	repaired := make([]llm.Turn, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		t := raw[i]
		if t.Role == "user" && len(t.ToolResults) > 0 {
			// Results with no preceding assistant call turn answer nothing.
			slog.Warn("dropping orphaned tool results from replay", "count", len(t.ToolResults))
			continue
		}
		if t.Role != "assistant" || len(t.ToolCalls) == 0 {
			repaired = append(repaired, t)
			continue
		}

		valid := make([]llm.ToolCall, 0, len(t.ToolCalls))
		for _, c := range t.ToolCalls {
			if c.InputJSON != "" && !json.Valid([]byte(c.InputJSON)) {
				slog.Warn("dropping tool call with invalid input from replay", "call", c.Name)
				continue
			}
			valid = append(valid, c)
		}
		t.ToolCalls = valid

		var recorded []llm.ToolResult
		if i+1 < len(raw) && raw[i+1].Role == "user" && len(raw[i+1].ToolResults) > 0 {
			recorded = raw[i+1].ToolResults
			i++
		}
		if len(t.ToolCalls) == 0 {
			if t.Text != "" {
				repaired = append(repaired, llm.Turn{Role: "assistant", Text: t.Text})
			}
			continue
		}

		byID := make(map[string]llm.ToolResult, len(recorded))
		for _, r := range recorded {
			byID[r.CallID] = r
		}
		paired := make([]llm.ToolResult, 0, len(t.ToolCalls))
		for _, c := range t.ToolCalls {
			if r, ok := byID[c.ID]; ok {
				paired = append(paired, r)
				continue
			}
			slog.Warn("synthesizing interrupted tool result for replay", "call", c.Name, "call_id", c.ID)
			paired = append(paired, llm.ToolResult{
				CallID: c.ID, Name: c.Name,
				Content: "(tool execution was interrupted; no result was recorded)", IsError: true,
			})
		}
		repaired = append(repaired, t, llm.Turn{Role: "user", ToolResults: paired})
	}

	// Merge consecutive plain-text turns of the same role — failed turns
	// leave e.g. user text followed by user text, which alternation-
	// enforcing providers reject.
	merged := make([]llm.Turn, 0, len(repaired))
	for _, t := range repaired {
		n := len(merged)
		if n > 0 && merged[n-1].Role == t.Role &&
			len(t.ToolCalls) == 0 && len(t.ToolResults) == 0 &&
			len(merged[n-1].ToolCalls) == 0 && len(merged[n-1].ToolResults) == 0 {
			merged[n-1].Text += "\n\n" + t.Text
			continue
		}
		merged = append(merged, t)
	}

	// History must open with an operator message: the row window may have
	// cut mid tool exchange.
	for i, t := range merged {
		if t.Role == "user" && t.Text != "" && len(t.ToolResults) == 0 {
			return merged[i:]
		}
	}
	return nil
}

// delegate runs a task on another agent and returns its answer. The wire
// graph is the capability: without an edge from caller to target there is
// no delegation. chain carries the agents already on this delegation path
// so cycles terminate.
func (e *Engine) delegate(ctx context.Context, wsID int64, caller workspace.Agent, chain []int64, agentName, task string, emit func(Event)) (string, error) {
	target, err := e.ws.GetAgentByName(ctx, wsID, agentName)
	if err != nil {
		return "", err
	}
	if target.ID == caller.ID {
		return "", errors.New("an agent cannot delegate to itself")
	}
	allowed, err := e.ws.CanDelegate(ctx, wsID, caller.ID, target.ID)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", fmt.Errorf("no wire from %q to %q — draw one in the blueprint to grant this delegation", caller.Name, target.Name)
	}
	for _, id := range chain {
		if id == target.ID {
			return "", fmt.Errorf("delegation cycle: %q is already working on this chain", target.Name)
		}
	}
	if len(chain) >= maxDelegationDepth {
		return "", fmt.Errorf("delegation depth limit (%d) reached", maxDelegationDepth)
	}
	if target.ModelID == nil {
		return "", fmt.Errorf("agent %q has no model bound", target.Name)
	}

	e.setStatus(target.ID, "working", task, emit)
	defer e.setStatus(target.ID, "idle", "", emit)

	slog.Info("delegation started", "workspace_id", wsID, "from", caller.Name, "to", target.Name, "depth", len(chain), "task_len", len(task))
	answer, err := e.runAgent(ctx, wsID, target, append(chain, caller.ID), task, emit)
	if err != nil {
		return "", fmt.Errorf("delegation to %q failed: %w", target.Name, err)
	}

	meta, _ := json.Marshal(map[string]any{"task": task, "delegated_by": caller.Name})
	msg, err := e.ws.AppendMessage(context.WithoutCancel(ctx), wsID, &target.ID, "delegation", answer, string(meta))
	if err != nil {
		return "", err
	}
	emit(Event{Type: "message", Message: &msg})
	slog.Info("delegation finished", "workspace_id", wsID, "to", target.Name)
	return answer, nil
}

// runAgent executes a delegated agent's turn, including its own tool loop
// when the blueprint wires it to further agents. Returns its final text.
func (e *Engine) runAgent(ctx context.Context, wsID int64, agent workspace.Agent, chain []int64, task string, emit func(Event)) (string, error) {
	model, err := e.cat.GetModel(ctx, *agent.ModelID)
	if err != nil {
		return "", err
	}
	client, _, err := e.cat.Client(ctx, model.ProviderID)
	if err != nil {
		return "", err
	}
	system, err := e.systemPrompt(ctx, wsID, agent)
	if err != nil {
		return "", err
	}
	// Small models offered tools tend to answer through them instead of in
	// text; the delegation contract has to be explicit.
	system += "\n\n## Delegation contract\nYou were delegated a task. Deliver your answer as plain text — your final text IS the result returned to the delegating agent. Use tools only when the task genuinely requires them."
	targets, err := e.ws.DelegationTargets(ctx, wsID, agent.ID)
	if err != nil {
		return "", err
	}
	gears, err := e.gears.ForAgent(ctx, wsID, agent.ID)
	if err != nil {
		return "", err
	}
	tools := e.toolsFor(agent, targets, gears)

	history := []llm.Turn{{Role: "user", Text: task}}
	for iter := 0; iter < maxToolIterations; iter++ {
		res, err := client.Chat(ctx, llm.Request{
			Model:    model.ModelName,
			System:   system,
			Messages: history,
			Tools:    tools,
		}, func(text string) error {
			emit(Event{Type: "delta", AgentID: agent.ID, Text: text})
			return ctx.Err()
		})
		if err != nil {
			return "", err
		}
		if res.StopReason != llm.StopToolUse || len(res.ToolCalls) == 0 {
			// An empty answer is a failure the caller must see, not a
			// result to pass along silently.
			if strings.TrimSpace(res.Text) == "" {
				return "", fmt.Errorf("agent %q returned an empty answer (stop_reason %s)", agent.Name, res.StopReason)
			}
			return res.Text, nil
		}

		var results []llm.ToolResult
		for _, call := range res.ToolCalls {
			out, isErr := e.execToolAs(ctx, wsID, agent, chain, call, emit)
			results = append(results, llm.ToolResult{CallID: call.ID, Name: call.Name, Content: out, IsError: isErr})
		}
		history = append(history,
			llm.Turn{Role: "assistant", Text: res.Text, ToolCalls: res.ToolCalls},
			llm.Turn{Role: "user", ToolResults: results},
		)
	}
	return "", fmt.Errorf("agent %q stopped after %d tool iterations without an answer", agent.Name, maxToolIterations)
}
