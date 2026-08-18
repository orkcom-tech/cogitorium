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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/egress"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/library"
	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/mcpoauth"
	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
	"github.com/orkcom-tech/cogitorium/internal/metrics"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/websearch"
	"github.com/orkcom-tech/cogitorium/internal/work"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

const (
	maxToolIterations = 16
	// maxDelegationDepth bounds how deep the blueprint graph is walked in
	// one turn. Cycles are additionally blocked by the visited chain.
	maxDelegationDepth = 4
)

// The two refusals a caller outside the engine has to be able to tell apart,
// because each is a different answer to whoever is waiting: try again shortly,
// or go and fix the workspace. Everything else the engine returns is either a
// store error the HTTP layer already maps, or a failure of the run itself.
var (
	ErrBusy = errors.New("a turn is already in progress in this workspace")
	// ErrNoModel is the state runAgent used to dereference straight through.
	// An agent with no model is ordinary — an imported bundle can name a model
	// this install does not have — so it must be an answer, not a panic.
	ErrNoModel = errors.New("no model is bound to it; bind one on the blueprint before anything can be run through it")

	// ErrBudget is a run stopped because it reached a ceiling an operator set.
	//
	// A distinct error rather than a generic failure: "it went wrong" and "you
	// told me to stop it here" want different reactions, and a caller that
	// cannot tell them apart will retry the one that must not be retried.
	ErrBudget = errors.New("this run reached the token budget set for it and was stopped")
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
	// Approval carries a paused turn's request for permission to search the
	// web; Resolved reports how that request ended, so the modal never sits
	// on screen dead and clickable.
	Approval *egress.Request `json:"approval,omitempty"`
	Resolved string          `json:"resolved,omitempty"`
	// Did rides the final event of an operator's turn — the same record a
	// delivery through an inlet has always carried. A person watching a turn
	// could previously see the tool rows go past and had no summary of them
	// afterwards, while a machine on the other side of a door got one. The
	// asymmetry was an oversight, not a design: everything needed was already
	// collected on the turn state, and only the delivery path ever read it.
	Did *Record `json:"did,omitempty"`
}

type Engine struct {
	// mcpOAuth holds the grants for remote MCP servers signed in to. Nil is
	// most installs.
	mcpOAuth *mcpoauth.Store

	// mcpPool keeps an MCP connection between calls, so a turn calling four
	// tools pays one handshake rather than four. Always present; empty until
	// something is dialled.
	mcpPool *mcpPool

	// metrics is what an operator alerts on. Nil is a working engine that
	// publishes nothing, which is what every test and any embedding gets.
	metrics *metrics.Set

	ws       *workspace.Store
	cat      *catalog.Store
	ctx      *contextstore.Store
	gears    *gear.Store
	gearExec *gear.Executor
	// mcp is the external MCP servers an operator installed and granted. Nil
	// when the capability is off, which is the default: it is the one thing
	// this product runs that it never saw the source of.
	// mcpSecrets resolves the names such a server was granted — the same
	// resolver a gear's names go through, so the two cannot disagree about
	// what a name means in a workspace.
	mcp        *mcpstore.Store
	mcpSecrets *secrets.Resolver
	library    *library.Store

	// dataDir is where the workspace directories live. The engine holds it
	// because agents now read and write their workspace's own files, and
	// workdir.Dir is the one place that knows which directory that is.
	dataDir string

	// The internet gate. searcher is nil when the master switch is off, and
	// every path checks for nil rather than carrying a disabled object that
	// might dial by accident. egressKilled is the runtime close-only switch,
	// injected so the engine does not import the server.
	searcher     *websearch.Searcher
	broker       *egress.Broker
	egressKilled func() bool

	// lanes is the queue's table, used here for exactly one thing: an
	// operator's turn takes the same lane a delivery would queue behind. Two
	// latches that could not see each other would let a chat turn and a
	// delivery run at once in one workspace, and the turn state they would
	// share holds the egress budget, both anti-worm taint latches and the run
	// record — all keyed by workspace.
	lanes *work.Store

	// runTokenBudget is the most one run may spend before it is stopped. Zero
	// is off, and off is the default.
	//
	// One budget and not two. A workspace-wide or daily ceiling was designed and
	// then removed: nothing drives a workspace's total except the operator's own
	// schedules and their own typing, so it would have been a knob whose only
	// use is stopping your own work. This one bounds what somebody ELSE can cost
	// through an inlet, which is a different thing entirely.
	runTokenBudget int64

	mu     sync.Mutex
	status map[int64]AgentStatus
	// pendingWork is the unit a workspace's next run belongs to, set by the
	// worker before the turn exists and consumed by beginTurn. A map rather
	// than a parameter because RunUnattended's signature is the engine's
	// contract with three callers, and threading a queue id through it would
	// put the queue in the engine's vocabulary for one field.
	pendingWork map[int64]int64
	running     map[int64]bool // workspace_id -> a turn is in flight
	turns       map[int64]*turnState
}

// Budgets is what one run may spend, in tokens. Zero is off.
type Budgets struct{ Run int64 }

func New(ws *workspace.Store, cat *catalog.Store, cs *contextstore.Store, gears *gear.Store, gearExec *gear.Executor, lib *library.Store, searcher *websearch.Searcher, broker *egress.Broker, lanes *work.Store, budgets Budgets, dataDir string) *Engine {
	return &Engine{
		ws:             ws,
		cat:            cat,
		ctx:            cs,
		gears:          gears,
		gearExec:       gearExec,
		library:        lib,
		dataDir:        dataDir,
		searcher:       searcher,
		broker:         broker,
		status:         map[int64]AgentStatus{},
		lanes:          lanes,
		runTokenBudget: budgets.Run,
		running:        map[int64]bool{},
		turns:          map[int64]*turnState{},
		mcpPool:        newMCPPool(),
	}
}

// StartMCPPool runs the sweeper that closes idle MCP connections, on the
// caller's lifetime.
//
// SEPARATE FROM New because a pooled connection is a PROCESS THAT OUTLIVES A
// TURN, and starting a goroutine that owns processes from a constructor would
// mean every test and every embedding acquires one whether or not it ever dials
// anything. The server starts it; without it the pool still works and simply
// never expires, which is the harmless half.
func (e *Engine) StartMCPPool(ctx context.Context) { e.mcpPool.sweep(ctx) }

// CloseMCP closes every pooled connection. Called when the server shuts down,
// so a child process does not outlive the thing that started it.
func (e *Engine) CloseMCP() { e.mcpPool.closeAll() }

// SetEgressKill injects the server's runtime kill switch.
func (e *Engine) SetEgressKill(f func() bool) { e.egressKilled = f }

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
//
// attachments are workspace-relative paths of files the operator put in front
// of the orchestrator; they are already in the workspace directory by the time
// this is called. It is variadic so that every existing caller of a plain text
// turn — the chat handler before this, and the tests that drive a turn — reads
// and compiles exactly as it did: a message with no files on it must be the
// same message it always was, all the way to the wire.
func (e *Engine) HandleUserMessage(ctx context.Context, wsID int64, text string, emit func(Event), attachments ...string) error {
	e.mu.Lock()
	if e.running[wsID] {
		e.mu.Unlock()
		return ErrBusy
	}
	e.running[wsID] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, wsID)
		e.mu.Unlock()
	}()

	// And the same lane the queue serialises deliveries on. An operator's turn
	// is claimed rather than queued: a person is holding a stream and possibly
	// an approval on screen, so being told the workspace is busy is a better
	// answer than a place in a line they cannot see.
	//
	// Taken AFTER the in-process latch so the two can never disagree about who
	// holds the workspace, and released on every path, including a panic —
	// a lane left claimed by a turn that is over is a workspace that never
	// accepts another delivery.
	lane, err := e.lanes.ClaimNow(ctx, work.Unit{
		Kind: work.KindChat, WorkspaceID: wsID, Lane: work.Lane(wsID),
	})
	if err != nil {
		if errors.Is(err, work.ErrLaneBusy) {
			return ErrBusy
		}
		return fmt.Errorf("take the workspace lane: %w", err)
	}
	defer func() {
		if err := e.lanes.Settle(context.WithoutCancel(ctx), lane.ID, ""); err != nil {
			slog.Error("could not release the workspace lane after an operator turn",
				"workspace_id", wsID, "unit", lane.ID, "err", err)
		}
	}()

	// One budget and one set of latches for the whole turn, delegation tree
	// included. Created here rather than threaded through every signature,
	// which is sound because only one turn runs per workspace at a time.
	e.beginTurn(wsID)
	defer e.endTurn(wsID)

	orch, err := e.orchestrator(ctx, wsID)
	if err != nil {
		return err
	}

	// The files are read and classified before the message is persisted, so a
	// path that names nothing leaves no half-message on the timeline for the
	// operator to wonder about — they fix the attachment and send again.
	atts, parts, err := e.collectAttachments(wsID, attachments)
	if err != nil {
		return err
	}
	meta, err := attachmentMeta(atts)
	if err != nil {
		return err
	}

	userMsg, err := e.ws.AppendMessage(ctx, wsID, nil, "user", text, meta)
	if err != nil {
		return err
	}
	emit(Event{Type: "message", Message: &userMsg})

	history, err := e.buildHistory(ctx, wsID, orch.ID)
	if err != nil {
		return err
	}
	// The bytes go on the turn they were attached to, and only there.
	// buildHistory has just replayed the row appended above — with the note
	// naming the files and without their content — so the last turn IS this
	// message, and this is where the files it carries are put in front of the
	// model for the one and only time (see buildHistory for why once).
	//
	// "Once" means once per turn, not once per request: the tool loop below
	// resends this history on every iteration, as every tool loop does, so an
	// orchestrator that calls four tools while looking at a photograph carries
	// it four times. That is the cost of the model still being able to see it
	// while it works, and it ends when the turn does.
	if len(parts) > 0 {
		last := len(history) - 1
		if last < 0 || history[last].Role != "user" {
			return fmt.Errorf("the message carrying %d attached files did not come back as the last turn of the replayed conversation, so the files would be shown against the wrong message; nothing was sent", len(parts))
		}
		history[last].Parts = parts
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
			e.emitDone(wsID, emit)
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
	e.emitDone(wsID, emit)
	return nil
}

// emitDone ends an operator's turn with the record of what it did.
//
// Every way a turn can end goes through here, including the ones that ended
// badly: a turn that wrote four files and then ran out of iterations still
// wrote them, and the record is most worth reading exactly when the answer is
// not. It is read BEFORE the deferred endTurn, for the same reason the delivery
// path reads it before its own — after that the turn state is gone and the
// record would report a run that did nothing.
func (e *Engine) emitDone(wsID int64, emit func(Event)) {
	out := e.outcome(wsID, "")
	slog.Info("turn finished", "workspace_id", wsID,
		"tools", len(out.Did.Tools), "files", out.Did.DistinctFiles(),
		"model_calls", out.Did.ModelCalls, "tokens_in", out.Did.Tokens.In, "tokens_out", out.Did.Tokens.Out)
	emit(Event{Type: "done", Did: &out.Did})
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
	if err := e.guardBudget(wsID); err != nil {
		return llm.Result{}, err
	}
	model, err := e.cat.GetModel(ctx, *agent.ModelID)
	if err != nil {
		return llm.Result{}, fmt.Errorf("agent %q: %w", agent.Name, err)
	}
	// Asked before the request is built, because the catalog is the only thing
	// that knows whether this model can be shown a picture and a provider that
	// is sent one it cannot take answers with a 400 naming neither the file nor
	// the model. A turn with no files asks nothing and costs nothing.
	if parts := attachedParts(history); len(parts) > 0 {
		if err := e.cat.CheckAccepts(ctx, model, parts); err != nil {
			return llm.Result{}, err
		}
	}
	client, _, err := e.cat.Client(ctx, model.ProviderID)
	if err != nil {
		return llm.Result{}, err
	}

	system, err := e.systemPrompt(ctx, wsID, agent, "")
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
	mcpTools, err := e.mcpToolsFor(ctx, wsID, agent.ID)
	if err != nil {
		return llm.Result{}, err
	}

	e.setStatus(agent.ID, "thinking", "", emit)
	res, err := client.Chat(ctx, llm.Request{
		Model:    model.ModelName,
		System:   system,
		Messages: history,
		Tools:    e.toolsFor(agent, targets, gears, mcpTools, e.egressAvailable(ctx, wsID, agent), e.turn(wsID).unattended),
	}, func(text string) error {
		emit(Event{Type: "delta", AgentID: agent.ID, Text: text})
		return ctx.Err()
	})
	if err != nil {
		return llm.Result{}, fmt.Errorf("model call for %q failed: %w", agent.Name, err)
	}
	slog.Info("model turn finished", "workspace_id", wsID, "agent", agent.Name, "stop_reason", res.StopReason, "tool_calls", len(res.ToolCalls))
	e.recordUsage(ctx, wsID, agent, res.Usage)
	return res, nil
}

// recordUsage books what a turn cost against the agent that spent it. The
// answer is already in hand by this point, so a bookkeeping failure is logged
// and swallowed: losing a reply because the ledger hiccuped would be a worse
// bug than an incomplete ledger.
func (e *Engine) recordUsage(ctx context.Context, wsID int64, agent workspace.Agent, u llm.Usage) {
	// The run's own record is written first, and unconditionally. It is the
	// same fact the workspace ledger below books per agent, counted for the run
	// as a whole — and a caller asking "how many times did this go to a model"
	// must get an answer even when the token ledger is having a bad day.
	//
	// Only calls that came back are counted. A provider error returns before
	// this line, and what happened to that attempt is in the run's error rather
	// than dressed up as work.
	e.noteModelCall(wsID, u)
	// The money, published where an operator can alert on it. NO AGENT, NO
	// MODEL, NO WORKSPACE in a label: per-agent spend is already on the agent's
	// own card and in the database, which are authenticated screens with an
	// audience, and a label here would put the whole roster on a dashboard.
	//
	// `reported` is kept as a label because it is the one distinction that
	// makes the number honest: not every OpenAI-compatible server returns
	// usage, and a confident zero for one that does not is a lie an operator
	// discovers from a bill.
	if e.metrics != nil {
		e.metrics.ModelCalls.Inc(map[string]string{
			"outcome":  "ok",
			"reported": strconv.FormatBool(u.Reported),
		})
		if u.Reported {
			e.metrics.ModelTokens.Add(map[string]string{"direction": "in"}, float64(u.InputTokens))
			e.metrics.ModelTokens.Add(map[string]string{"direction": "out"}, float64(u.OutputTokens))
		}
	}
	if err := e.ws.RecordTurn(context.WithoutCancel(ctx), wsID, agent.ID, agent.ModelLabel,
		u.InputTokens, u.OutputTokens, u.Reported, e.workOf(wsID)); err != nil {
		slog.Warn("could not record token usage", "workspace_id", wsID, "agent", agent.Name, "err", err)
	}
	if !u.Reported {
		slog.Info("provider reported no token usage", "agent", agent.Name, "model", agent.ModelLabel)
	}
}

// systemPrompt assembles an agent's effective system prompt: its role, the
// workspace snapshot (orchestrator only), and its bound context documents
// fetched live from Contextverse.
//
// extra is instruction that belongs to how this particular run was started —
// today, the delegation contract. It is a parameter rather than something a
// caller appends afterwards because the prohibitions must come last, and a
// caller that appended its own paragraph put 350 bytes of instruction after
// them. That is the one position the feature depends on.
func (e *Engine) systemPrompt(ctx context.Context, wsID int64, agent workspace.Agent, extra string) (string, error) {
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

	explicit, err := e.ws.BindingsForAgent(ctx, wsID, agent.ID)
	if err != nil {
		return "", err
	}
	implicit, err := e.branchDocs(ctx, wsID, agent)
	if err != nil {
		return "", err
	}
	paths := append(implicit, explicit...)

	if len(paths) > 0 {
		b = fmt.Appendf(b, "\n\n## Context (from Contextverse)\n")
		seen := map[string]bool{}
		for _, p := range paths {
			if seen[p] {
				continue
			}
			seen[p] = true
			// Through the run's snapshot, not straight to contextd: one read
			// per document per run, shared by every agent in the delegation
			// tree, and the version is recorded as it goes. See
			// contextcache.go for what this was costing and why reading the
			// same document twice in one run was also a correctness bug.
			content, _, err := e.contextDoc(ctx, wsID, p)
			if err != nil {
				return "", fmt.Errorf("context doc %q bound to agent %q cannot be read: %w — restore the file in Contextverse or unbind it from the agent", p, agent.Name, err)
			}
			// An emptied document contributes nothing. Forgetting is a real
			// delete now — contextd grew `file delete` in v1.0.0 — so this is
			// no longer how a memory is dropped. It stays because a document
			// CAN be empty for other reasons, and a heading over nothing reads
			// to a model as "there are no rules here", which is worse than the
			// section being absent.
			if strings.TrimSpace(content) == "" {
				continue
			}
			b = fmt.Appendf(b, "\n### %s\n%s\n", p, content)
		}
	}

	gearSection, err := e.gearSection(ctx, wsID, agent)
	if err != nil {
		return "", err
	}
	b = append(b, gearSection...)
	b = append(b, libraryNote...)
	b = append(b, extra...)
	b = append(b, avoidSection(agent.Avoid)...)
	return string(b), nil
}

// avoidSection is the operator's standing prohibitions, and it is last in the
// prompt on purpose: a constraint stated last is the one the model still has
// in view when it answers. It is also the only section that has to survive
// everything above it and everything the operator asks for afterwards, which
// is why the preamble says so rather than leaving it to be inferred.
//
// An agent with nothing to avoid gets no section at all. A heading over an
// empty list reads as "there are no rules here", and it would also put a blank
// stretch at the end of every prompt, which is where the model is looking.
func avoidSection(avoid string) string {
	rules := workspace.Rules(avoid)
	if len(rules) == 0 {
		return ""
	}
	// The rules are split by the same function the API and the bundle use, so
	// what the model is told and what the operator sees in the inspector can
	// never drift apart.
	b := []byte(avoidPreamble)
	for _, r := range rules {
		b = fmt.Appendf(b, "- %s\n", r)
	}
	return string(b)
}

const avoidPreamble = "\n\n## Never do this\n" +
	"Standing prohibitions from the operator. They hold for the whole\n" +
	"conversation. Nothing above overrides them, and neither does anything you\n" +
	"are asked for later — if a request needs one of these, refuse it and say\n" +
	"which one.\n"

// branchDocs returns the workspace-branch documents an agent sees without
// anyone binding them by hand: everything under the workspace's shared
// branch, plus everything under its own. This is the mind-map shape —
// context organized by who it belongs to rather than by a list of manual
// bindings.
//
// An unreachable Contextverse yields no documents rather than an error:
// the branch is a convenience, and an agent must still be able to run on
// its role alone.
func (e *Engine) branchDocs(ctx context.Context, wsID int64, agent workspace.Agent) ([]string, error) {
	ws, err := e.ws.GetWorkspace(ctx, wsID)
	if err != nil {
		return nil, err
	}
	// Through the run's snapshot: this is called before every model call of
	// every agent, and it was listing the whole space each time.
	files, err := e.contextList(ctx, wsID)
	if err != nil {
		slog.Warn("context branch unavailable; agent runs without it", "workspace_id", wsID, "agent", agent.Name, "err", err)
		return nil, nil
	}

	var out []string
	for _, f := range files {
		if ws.Branch != "" && strings.HasPrefix(f.Path, ws.SharedBranch()+"/") {
			out = append(out, f.Path)
			continue
		}
		if agent.Branch != "" && strings.HasPrefix(f.Path, agent.Branch+"/") {
			out = append(out, f.Path)
		}
	}
	return out, nil
}

// gearSection spells out the agent's forged tools in the prompt itself.
// Listing them among the tool definitions is not enough: models skim tool
// lists and rebuild what already exists, and duplicate gears are how the
// catalog turns into noise. Naming each one, with what it does, is what
// keeps the environment consistent across turns and sessions.
func (e *Engine) gearSection(ctx context.Context, wsID int64, agent workspace.Agent) (string, error) {
	gears, err := e.gears.ForAgent(ctx, wsID, agent.ID)
	if err != nil {
		return "", err
	}

	var b []byte
	b = fmt.Appendf(b, "\n\n## Your gears\n")
	if len(gears) == 0 {
		b = fmt.Appendf(b, "None are bound to you yet.\n\nBefore building anything with forge_gear, call list_gears first: a gear that already exists — forged here or in another workspace — can be granted to you instead of reinvented. Only forge when the catalog genuinely has nothing that fits.\n")
		return string(b), nil
	}

	b = fmt.Appendf(b, "These tools were forged for reuse. Use them instead of writing the same work again — that is what they are for.\n\n")
	for _, g := range gears {
		b = fmt.Appendf(b, "- **%s%s** — %s\n", gearToolPrefix, g.Name, g.Description)
		if g.ArgsSchema != "" && g.ArgsSchema != "{}" {
			b = fmt.Appendf(b, "  arguments: %s\n", g.ArgsSchema)
		}
	}
	b = fmt.Appendf(b, "\nIf none of them fits the task, call list_gears to check the wider catalog before forging a new one.\n")
	return string(b), nil
}

// delegationContract is what a worker is told about the shape of its answer.
//
// Small models offered tools tend to answer through them instead of in text,
// so it has to be explicit. The gear reminder belongs here too — a delegated
// task is exactly the moment an agent is tempted to rebuild work that a forged
// tool already does.
const delegationContract = "\n\n## Delegation contract\nYou were delegated a task. Deliver your answer as plain text — your final text IS the result returned to the delegating agent. Use tools only when the task genuinely requires them. If one of your gears does part of this work, use it rather than redoing it by hand; if none fits, check list_gears before forging anything new."

// libraryNote points every agent at the shared instruction library. The
// same reasoning as gears: guidance nobody can find gets written again,
// slightly differently, until nothing is authoritative.
const libraryNote = "\n\n## Instruction library\nReusable guidance — house style, procedures, checklists — is kept in a shared library. Call list_instructions before writing out guidance from scratch, and read one with read_instruction. When you work something out that will still be true next week, save_instruction puts it where the next agent will find it.\n"

// AssembledPrompt exposes the effective system prompt for the UI's
// "what does this agent see" preview.
//
// A worker is only ever run by being delegated to, so its preview carries the
// delegation contract; the orchestrator is only ever run from the chat, so its
// preview does not. Showing the same string the wire carries is the whole
// point of the preview — a version that omitted a paragraph would send the
// operator debugging a prompt the model never received.
func (e *Engine) AssembledPrompt(ctx context.Context, agentID int64) (string, error) {
	agent, err := e.ws.GetAgent(ctx, agentID)
	if err != nil {
		return "", err
	}
	extra := delegationContract
	if agent.IsOrchestrator {
		extra = ""
	}
	return e.systemPrompt(ctx, agent.WorkspaceID, agent, extra)
}

// MemoryItem is one thing an agent remembers, with where it came from and
// whether the operator can change it. Everything that reaches the prompt
// appears here: memory an operator cannot see is memory they cannot correct,
// which is how an agent ends up quietly steering by something nobody
// intended.
type MemoryItem struct {
	Kind        string `json:"kind"`   // role | private | shared | bound | instruction
	Source      string `json:"source"` // the path, or "role"
	Content     string `json:"content"`
	Editable    bool   `json:"editable"`
	Removable   bool   `json:"removable"`
	BindingID   *int64 `json:"binding_id,omitempty"`
	Description string `json:"description"`
}

// Memory returns everything shaping this agent, in the order it reaches the
// prompt.
func (e *Engine) Memory(ctx context.Context, agentID int64) ([]MemoryItem, error) {
	agent, err := e.ws.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	ws, err := e.ws.GetWorkspace(ctx, agent.WorkspaceID)
	if err != nil {
		return nil, err
	}

	items := []MemoryItem{{
		Kind: "role", Source: "role", Content: agent.Role, Editable: true,
		Description: "Its role — the instruction it always carries.",
	}}

	bindings, err := e.ws.ListContextBindings(ctx, agent.WorkspaceID)
	if err != nil {
		return nil, err
	}
	bindingFor := map[string]int64{}
	for _, b := range bindings {
		if b.AgentID == nil || *b.AgentID == agent.ID {
			bindingFor[b.Path] = b.ID
		}
	}

	files, err := e.ctx.List(ctx)
	if err != nil {
		// Without Contextverse the agent runs on its role alone; say so
		// rather than pretending the list is complete.
		slog.Warn("memory listing incomplete: contextd unavailable", "agent", agent.Name, "err", err)
		return items, nil
	}

	seen := map[string]bool{}
	add := func(path, kind, description string, removable bool, bindingID *int64) {
		if seen[path] {
			return
		}
		seen[path] = true
		content, err := e.ctx.Get(ctx, path)
		if err != nil {
			content = "(cannot be read: " + err.Error() + ")"
		}
		items = append(items, MemoryItem{
			Kind: kind, Source: path, Content: content, Editable: true,
			Removable: removable, BindingID: bindingID, Description: description,
		})
	}

	for _, f := range files {
		if agent.Branch != "" && strings.HasPrefix(f.Path, agent.Branch+"/") {
			add(f.Path, "private", "Only this agent reads it.", false, nil)
		}
	}
	for _, f := range files {
		if ws.Branch != "" && strings.HasPrefix(f.Path, ws.SharedBranch()+"/") {
			add(f.Path, "shared", "Every agent in this workspace reads it.", false, nil)
		}
	}
	for path, id := range bindingFor {
		kind, description := "bound", "Bound from elsewhere in the space."
		if strings.HasPrefix(path, library.Root+"/") {
			kind, description = "instruction", "From the shared instruction library."
		}
		bindingID := id
		add(path, kind, description, true, &bindingID)
	}
	return items, nil
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
			// A replayed attachment is its path and its description, never its
			// bytes again.
			//
			// The alternative is a cost multiplier nobody asked for: a 4 MB
			// photograph re-encoded into every later request means the twentieth
			// turn of that conversation pays for it for the twentieth time, and
			// providers bill an image by its pixels whether or not the question
			// is still about it. What the model needs after the turn it arrived
			// on is that the file exists and where — and the note carries
			// exactly that, so an agent asked about it three turns later reads
			// the file with read_file or hands the path to a gear, which is the
			// route every file too big to show has taken all along.
			//
			// The cost of the decision, stated rather than hidden: the model
			// cannot LOOK at the image again. It saw it once and whatever it
			// said about it is in the transcript. An operator who needs another
			// look attaches it again — the file is still in the workspace, so
			// that is one click and one deliberate charge.
			text := withAttachments(m.Content, attachmentsOf(m.Meta))
			if text != "" {
				turns = append(turns, llm.Turn{Role: "user", Text: text})
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

	// The delegation row belongs to the operator's conversation, and an
	// unattended run is not part of it. It is not replayed — buildHistory
	// ignores this kind — so leaving it in would not cost tokens; it would
	// simply fill the timeline the operator reads with pipeline traffic. A
	// nightly job classifying four thousand tickets would write four thousand
	// rows into a conversation nobody had. Where an unattended run went is
	// recorded in its own ledger, which is the place to look for it.
	if !e.turn(wsID).unattended {
		meta, _ := json.Marshal(map[string]any{"task": task, "delegated_by": caller.Name})
		msg, err := e.ws.AppendMessage(context.WithoutCancel(ctx), wsID, &target.ID, "delegation", answer, string(meta))
		if err != nil {
			return "", err
		}
		emit(Event{Type: "message", Message: &msg})
	}
	slog.Info("delegation finished", "workspace_id", wsID, "to", target.Name, "unattended", e.turn(wsID).unattended)
	return answer, nil
}

// runAgent executes a delegated agent's turn, including its own tool loop
// when the blueprint wires it to further agents. Returns its final text.
func (e *Engine) runAgent(ctx context.Context, wsID int64, agent workspace.Agent, chain []int64, task string, emit func(Event)) (string, error) {
	// Checked here rather than only at the call sites: an agent with no model
	// is an ordinary state — a bundle can name a model this install does not
	// have — and dereferencing the pointer for it would take the server down
	// instead of telling somebody to bind a model.
	if agent.ModelID == nil {
		return "", fmt.Errorf("agent %q: %w", agent.Name, ErrNoModel)
	}
	model, err := e.cat.GetModel(ctx, *agent.ModelID)
	if err != nil {
		return "", err
	}
	client, _, err := e.cat.Client(ctx, model.ProviderID)
	if err != nil {
		return "", err
	}
	// The contract goes THROUGH systemPrompt rather than onto the end of it:
	// appending it here put it after the operator's prohibitions, which are
	// meant to be the last thing the model reads.
	system, err := e.systemPrompt(ctx, wsID, agent, delegationContract)
	if err != nil {
		return "", err
	}
	targets, err := e.ws.DelegationTargets(ctx, wsID, agent.ID)
	if err != nil {
		return "", err
	}
	gears, err := e.gears.ForAgent(ctx, wsID, agent.ID)
	if err != nil {
		return "", err
	}
	mcpTools, err := e.mcpToolsFor(ctx, wsID, agent.ID)
	if err != nil {
		return "", err
	}
	tools := e.toolsFor(agent, targets, gears, mcpTools, e.egressAvailable(ctx, wsID, agent), e.turn(wsID).unattended)

	history := []llm.Turn{{Role: "user", Text: task}}
	for iter := 0; iter < maxToolIterations; iter++ {
		if err := e.guardBudget(wsID); err != nil {
			return "", err
		}
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
		e.recordUsage(ctx, wsID, agent, res.Usage)
		if res.StopReason != llm.StopToolUse || len(res.ToolCalls) == 0 {
			// A reply the token ceiling cut short is not an answer. Returned as
			// one it becomes a sentence that stops mid-word, handed back up the
			// delegation chain — or out of an inlet to a caller who has no way
			// to tell it apart from a complete one.
			if res.StopReason == "max_tokens" || res.StopReason == "length" {
				return "", fmt.Errorf("agent %q was cut off by the model's token limit (%s), so its partial answer was discarded", agent.Name, res.StopReason)
			}
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
