package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// A worker is reached by delegation, and the delegation contract must not sit
// after the operator's prohibitions.
//
// This is the exact regression that shipped: runAgent appended the contract to
// the string systemPrompt had already finished, so on the wire a delegated
// worker read 350 bytes of instruction AFTER the rules it was told were the
// last word. The orchestrator's own path was clean, which is why nothing
// looked wrong — the failure only existed on the path that carries the work.
//
// Asserting on systemPrompt directly rather than through AssembledPrompt is
// deliberate: the preview is a second caller, and a test that only reads the
// preview lets the wire regress while staying green. That is how it hid.
func TestTheDelegationContractComesBeforeTheProhibitions(t *testing.T) {
	f := newPromptFixture(t, "Never spend money.")

	got, err := f.engine.systemPrompt(context.Background(), f.agent.WorkspaceID, f.agent, delegationContract)
	if err != nil {
		t.Fatalf("assemble the delegated prompt: %v", err)
	}

	contract := strings.Index(got, "## Delegation contract")
	rules := strings.Index(got, "## Never do this")
	if contract < 0 {
		t.Fatalf("the delegated prompt carries no delegation contract:\n%s", tail(got, 400))
	}
	if rules < 0 {
		t.Fatalf("the delegated prompt carries no prohibitions:\n%s", tail(got, 400))
	}
	if contract > rules {
		t.Errorf("the delegation contract is after the prohibitions; the rules are meant to be the last thing read:\n%s", tail(got, 600))
	}
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "- Never spend money.") {
		t.Errorf("the prompt does not end on the rule:\n%s", tail(got, 300))
	}
}

// The preview has to be the prompt. A worker is only ever run by delegation,
// so its preview carries the contract; the orchestrator is only ever run from
// the chat, so its preview does not. When the two disagreed, the operator was
// reading a strict prefix of what the model received — 926 bytes against 1277
// — and debugging a prompt that was never sent.
func TestThePreviewIsTheStringThatIsSent(t *testing.T) {
	f := newPromptFixture(t, "Never spend money.")
	ctx := context.Background()

	preview, err := f.engine.AssembledPrompt(ctx, f.agent.ID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	wire, err := f.engine.systemPrompt(ctx, f.agent.WorkspaceID, f.agent, delegationContract)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	if preview != wire {
		t.Errorf("the worker's preview is %d bytes and the delegated run sends %d", len(preview), len(wire))
	}

	// And the orchestrator, which is never delegated to, must not be shown a
	// contract that will not apply to it.
	orch, err := f.ws.GetAgentByName(ctx, f.agent.WorkspaceID, workspace.OrchestratorName)
	if err != nil {
		t.Fatalf("find the orchestrator: %v", err)
	}
	orchPreview, err := f.engine.AssembledPrompt(ctx, orch.ID)
	if err != nil {
		t.Fatalf("orchestrator preview: %v", err)
	}
	if strings.Contains(orchPreview, "## Delegation contract") {
		t.Error("the orchestrator's preview carries a delegation contract it never receives")
	}
}

// An agent the orchestrator creates mid-turn inherits its prohibitions.
//
// Without this the rule was one tool call from being routed around: an
// orchestrator forbidden to spend money creates a worker with no rules, is
// wired to it automatically on the next line, and delegates the spending. The
// operator does not learn the agent exists until the turn is over, and the one
// model in the turn that was asked to do the forbidden thing is the one that
// was never told not to.
func TestACreatedAgentInheritsItsCreatorsProhibitions(t *testing.T) {
	f := newPromptFixture(t, "")
	ctx := context.Background()

	orch, err := f.ws.GetAgentByName(ctx, f.agent.WorkspaceID, workspace.OrchestratorName)
	if err != nil {
		t.Fatalf("find the orchestrator: %v", err)
	}
	const rule = "Never spend money."
	if _, err := f.ws.SetAgentAvoid(ctx, orch.ID, rule); err != nil {
		t.Fatalf("set the orchestrator's prohibitions: %v", err)
	}
	orch, err = f.ws.GetAgent(ctx, orch.ID)
	if err != nil {
		t.Fatalf("re-read the orchestrator: %v", err)
	}

	out, err := f.engine.dispatchTool(ctx, f.agent.WorkspaceID, orch, nil, llm.ToolCall{
		Name:      "agent_create",
		InputJSON: `{"name":"spender","role":"You buy things.","model":"claude-sonnet-4-6"}`,
	}, func(Event) {})
	if err != nil {
		t.Fatalf("agent_create: %v — %s", err, out)
	}

	spender, err := f.ws.GetAgentByName(ctx, f.agent.WorkspaceID, "spender")
	if err != nil {
		t.Fatalf("find the created agent: %v", err)
	}
	if spender.Avoid != rule {
		t.Fatalf("the created agent's prohibitions are %q, want the creator's %q — an agent that can hire its way out of a rule does not have one",
			spender.Avoid, rule)
	}

	// And it must actually reach that agent's prompt, not merely its row.
	prompt, err := f.engine.systemPrompt(ctx, f.agent.WorkspaceID, spender, delegationContract)
	if err != nil {
		t.Fatalf("assemble the created agent's prompt: %v", err)
	}
	if !strings.Contains(prompt, rule) {
		t.Errorf("the created agent's prompt does not carry the inherited rule:\n%s", tail(prompt, 400))
	}
}
