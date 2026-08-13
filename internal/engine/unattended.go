package engine

import (
	"context"
	"fmt"
	"log/slog"
)

// RunUnattended runs one named agent on one payload and returns what the agent
// said together with the record of what the run actually did. Both, always: the
// prose alone cannot distinguish a job that was done from one that was claimed,
// which is the failure the record exists for (see record.go).
//
// This is the door an inlet delivery comes through, and it differs from the
// operator's turn in three ways that all follow from the same fact — nobody is
// at a screen.
//
//  1. It goes through runAgent, not HandleUserMessage. buildHistory replays the
//     workspace timeline into every orchestrator turn, so a pipeline calling the
//     chat path would append each request and each answer to one transcript and
//     replay the lot on the next call: request 200 carrying the previous 199,
//     at the caller's expense, until the context window ends it. runAgent
//     starts from a single user turn and appends nothing, which is how every
//     delegated worker already runs.
//
//  2. The turn is marked tainted before the first model call. The payload is
//     third-party text arriving as the opening user turn, which is exactly what
//     the latch exists for: save_instruction, forge_gear, context_bind/unbind,
//     agent_create, agent_update, wire_create and grant_gear are closed for the
//     whole run. Otherwise anything an inlet's caller could write would be one
//     obliging model away from the global instruction library or the blueprint,
//     where it would reach every agent on every later turn.
//
//  3. web_search is not offered. Every search stops the turn and waits up to a
//     minute for a person to approve that exact query, so on this path it is a
//     stall followed by a failure. The turn state carries the fact, so an agent
//     the payload reaches by delegation is covered too.
func (e *Engine) RunUnattended(ctx context.Context, wsID int64, agentName, task string) (Outcome, error) {
	agent, err := e.ws.GetAgentByName(ctx, wsID, agentName)
	if err != nil {
		return Outcome{}, err
	}
	if agent.ModelID == nil {
		return Outcome{}, fmt.Errorf("agent %q: %w", agent.Name, ErrNoModel)
	}

	// One run per workspace, the same rule the operator's turn follows. The
	// egress budget and both latches are keyed per workspace and assume it, so
	// a second concurrent run would spend the first one's allowance.
	e.mu.Lock()
	if e.running[wsID] {
		e.mu.Unlock()
		return Outcome{}, ErrBusy
	}
	e.running[wsID] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, wsID)
		e.mu.Unlock()
	}()

	ts := e.beginTurn(wsID)
	defer e.endTurn(wsID)
	// Set before anything can reach a model, and before any goroutine other
	// than this one exists to read it.
	ts.tainted = true
	ts.unattended = true

	// Events go nowhere: there is no stream on this path, and the timeline is
	// deliberately untouched. Status still reaches the blueprint through
	// setStatus, so an operator watching a workspace sees the agent working.
	discard := func(Event) {}

	e.setStatus(agent.ID, "working", "an inlet delivery", discard)
	defer e.setStatus(agent.ID, "idle", "", discard)

	slog.Info("unattended run started", "workspace_id", wsID, "agent", agent.Name, "task_len", len(task))
	answer, runErr := e.runAgent(ctx, wsID, agent, nil, task, discard)

	// Read here, on both paths, and before the deferred endTurn takes the turn
	// state away. On the failing path especially: a run that unpacked an
	// archive, wrote four files and then fell over did that work, and the
	// moment somebody is woken up is the moment they need to know it.
	out := e.outcome(wsID, answer)
	if runErr != nil {
		slog.Info("unattended run failed", "workspace_id", wsID, "agent", agent.Name,
			"tools", len(out.Did.Tools), "files", len(out.Did.Files),
			"model_calls", out.Did.ModelCalls, "err", runErr)
		return out, runErr
	}
	slog.Info("unattended run finished", "workspace_id", wsID, "agent", agent.Name,
		"answer_len", len(answer), "tools", len(out.Did.Tools), "files", len(out.Did.Files),
		"model_calls", out.Did.ModelCalls)
	return out, nil
}

// UnattendedTargetError checks a task's target agent without running it, so the
// management API can refuse a task that names an agent nobody has created —
// while the operator writing it is still there to read the complaint, rather
// than at delivery time when the reader is a caller who cannot fix it.
func (e *Engine) UnattendedTargetError(ctx context.Context, wsID int64, agentName string) error {
	agent, err := e.ws.GetAgentByName(ctx, wsID, agentName)
	if err != nil {
		return err
	}
	if agent.ModelID == nil {
		return fmt.Errorf("agent %q: %w", agent.Name, ErrNoModel)
	}
	return nil
}

// WorkspaceBusy reports whether a turn is already running in this workspace.
//
// It exists so a caller can refuse BEFORE it spends anything on a request it
// is about to reject. The delivery handler used to buffer the whole body and
// write the file to disk first and meet the latch afterwards: eight concurrent
// 32 MiB deliveries into a busy workspace took the server's heap from 2 MiB to
// 592 MiB and left 256 MiB on disk, and seven of the eight were then refused.
// Everybody paid full price for an answer of "no".
//
// It is deliberately advisory. The authoritative check is still inside
// RunUnattended, under the same lock that sets the flag — this only avoids the
// expensive work in the common case, and a race here costs one wasted read
// rather than a second concurrent run.
func (e *Engine) WorkspaceBusy(wsID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running[wsID]
}

// SetWorkFor names the queued unit a workspace's next unattended run belongs
// to. Called by the worker before RunUnattended, because the turn state does
// not exist yet at that point — the value is held until beginTurn takes it.
func (e *Engine) SetWorkFor(wsID, workID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pendingWork == nil {
		e.pendingWork = map[int64]int64{}
	}
	e.pendingWork[wsID] = workID
}
