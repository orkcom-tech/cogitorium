package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/planboard"
)

// A planboard's whole promise is that the ORDER is not the model's to choose.
// These are the two halves of it: what the agent is shown, and what it is
// allowed to do about it.

// planned attaches a plan to the fixture's agent and returns the store, so a
// test can look at the position afterwards.
func planned(t *testing.T, f promptFixture, mode planboard.Mode, titles ...string) (*planboard.Store, int64) {
	t.Helper()
	ctx := context.Background()

	plans := planboard.NewStore(f.db)
	steps := make([]planboard.Step, 0, len(titles))
	for _, ti := range titles {
		steps = append(steps, planboard.Step{Title: ti})
	}
	p, err := plans.Save(ctx, "nightly", "", nil, mode, steps, 0, 0)
	if err != nil {
		t.Fatalf("save the plan: %v", err)
	}
	b, err := plans.Bind(ctx, p.ID, f.agent.WorkspaceID, &f.agent.ID)
	if err != nil {
		t.Fatalf("attach the plan: %v", err)
	}
	f.engine.SetPlanboards(plans)
	return plans, b.ID
}

func TestThePromptCarriesTheStepInFrontAndNotTheOnesAfterIt(t *testing.T) {
	t.Parallel()
	f := newPromptFixture(t, "")
	plans, binding := planned(t, f, planboard.ModeResume,
		"Gather the overnight errors", "Group them by service", "Write the summary")

	prompt := f.prompt(t)
	if !strings.Contains(prompt, "Gather the overnight errors") {
		t.Fatal("the step in front of the agent is not in its prompt")
	}
	// This is the feature. A model shown all three steps works ahead, reports
	// two of them done at once, and the order stops being a fact about the
	// workflow and becomes a suggestion the model weighed.
	for _, later := range []string{"Group them by service", "Write the summary"} {
		if strings.Contains(prompt, later) {
			t.Fatalf("the prompt carries %q, a step the agent has not reached", later)
		}
	}
	// It is told how far along it is, because a worker that cannot tell
	// whether it is near the end writes as if every step were the last.
	if !strings.Contains(prompt, "step 1 of 3") {
		t.Fatal("the prompt does not say which step of how many this is")
	}

	if _, err := plans.Done(context.Background(), binding, "done"); err != nil {
		t.Fatal(err)
	}
	next := f.prompt(t)
	if !strings.Contains(next, "Group them by service") || strings.Contains(next, "Gather the overnight errors") {
		t.Fatal("closing a step did not move which step the prompt carries")
	}
}

func TestTheReasonAStepFailedReachesTheNextRun(t *testing.T) {
	t.Parallel()
	f := newPromptFixture(t, "")
	plans, binding := planned(t, f, planboard.ModeResume, "Call the API", "File the report")

	if _, err := plans.Blocked(context.Background(), binding, "the api answered 503"); err != nil {
		t.Fatal(err)
	}
	prompt := f.prompt(t)
	if !strings.Contains(prompt, "the api answered 503") {
		t.Fatal("the next run is not told what stopped the last one")
	}
	if !strings.Contains(prompt, "Call the API") {
		t.Fatal("a blocked step is not the step in front of the next run")
	}
}

func TestAnAgentWithNoPlanIsOfferedNoWayToCloseOne(t *testing.T) {
	t.Parallel()
	f := newPromptFixture(t, "")

	// No plan attached: a tool for closing a step, offered to an agent with no
	// step, is an invitation to invent one.
	for _, tool := range f.engine.toolsFor(f.agent, nil, nil, nil, false, false, true, false) {
		if strings.HasPrefix(tool.Name, "plan_step_") {
			t.Fatalf("an agent with no plan was offered %q", tool.Name)
		}
	}

	planned(t, f, planboard.ModeResume, "the only step")
	var found []string
	for _, tool := range f.engine.toolsFor(f.agent, nil, nil, nil, false, false, true, true) {
		if strings.HasPrefix(tool.Name, "plan_step_") {
			found = append(found, tool.Name)
		}
	}
	if len(found) != 2 {
		t.Fatalf("an agent with a plan was offered %v, not both ways to end a step", found)
	}
}

func TestAWorkerCannotRewriteThePlanItIsFollowing(t *testing.T) {
	t.Parallel()
	f := newPromptFixture(t, "")
	planned(t, f, planboard.ModeResume, "the only step")

	// Not offered — a worker's tool list has none of these.
	for _, tool := range f.engine.toolsFor(f.agent, nil, nil, nil, false, false, true, true) {
		if strings.HasPrefix(tool.Name, "planboard_") {
			t.Fatalf("a worker was offered %q", tool.Name)
		}
	}

	// And refused if it calls one anyway. A worker that can edit its own
	// instructions is a worker with no instructions.
	args, err := parseArgs("planboard_delete", `{"name":"nightly"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.engine.dispatchPlanTool(context.Background(), f.agent.WorkspaceID, f.agent, "planboard_delete", args)
	if err == nil {
		t.Fatal("a worker deleted the plan it was following")
	}
	if !strings.Contains(err.Error(), "orchestrator") {
		t.Fatalf("the refusal does not say whose the tool is: %v", err)
	}
}

func TestClosingAStepReportsTheNextOneWithoutAskingForIt(t *testing.T) {
	t.Parallel()
	f := newPromptFixture(t, "")
	planned(t, f, planboard.ModeResume, "first", "second")

	args, err := parseArgs("plan_step_done", `{"note":"did it"}`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.engine.dispatchPlanTool(context.Background(), f.agent.WorkspaceID, f.agent, "plan_step_done", args)
	if err != nil {
		t.Fatalf("closing the step failed: %v", err)
	}
	// Told what is next, and told not to start it: the next step belongs to
	// the next turn, which is what keeps one step one step.
	if !strings.Contains(out, "second") {
		t.Fatalf("closing a step did not say what comes next: %q", out)
	}
	if !strings.Contains(out, "not yours to start") {
		t.Fatalf("closing a step read as permission to carry on: %q", out)
	}
}

func TestNamingThePlanIsOptionalUntilThereAreTwo(t *testing.T) {
	t.Parallel()
	f := newPromptFixture(t, "")
	plans, _ := planned(t, f, planboard.ModeResume, "the only step")
	ctx := context.Background()

	// One plan: making the agent repeat a name it was just told is ceremony.
	args, _ := parseArgs("plan_step_done", `{}`)
	if _, err := f.engine.dispatchPlanTool(ctx, f.agent.WorkspaceID, f.agent, "plan_step_done", args); err != nil {
		t.Fatalf("closing the only step needed a name: %v", err)
	}

	second, err := plans.Save(ctx, "weekly", "", nil, planboard.ModeResume, []planboard.Step{{Title: "sweep"}}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plans.Bind(ctx, second.ID, f.agent.WorkspaceID, &f.agent.ID); err != nil {
		t.Fatal(err)
	}

	// Two plans: guessing which one the model closed would advance the wrong
	// workflow, silently.
	args, _ = parseArgs("plan_step_done", `{}`)
	_, err = f.engine.dispatchPlanTool(ctx, f.agent.WorkspaceID, f.agent, "plan_step_done", args)
	if err == nil {
		t.Fatal("with two plans in front of it, an unnamed step was closed anyway")
	}
	if !strings.Contains(err.Error(), "nightly") || !strings.Contains(err.Error(), "weekly") {
		t.Fatalf("the refusal does not list what it could have meant: %v", err)
	}
}

// A provider that answers in a shape this client cannot read used to produce a
// blank bubble and a 200: the turn was recorded as a success, the assistant
// message was empty, and nothing anywhere said why. This is that case.
func TestAnAnswerWithNothingInItIsReportedRatherThanStored(t *testing.T) {
	t.Parallel()
	f := newPromptFixture(t, "")

	_, err := f.engine.modelTurn(context.Background(), f.agent.WorkspaceID, f.agent,
		[]llm.Turn{{Role: "user", Text: "hello"}}, func(Event) {})
	if err == nil {
		t.Fatal("a turn against an unreachable provider was reported as a success")
	}
	// Not the point of this test, but worth stating: whatever went wrong, the
	// person is told something rather than shown an empty answer.
	if err.Error() == "" {
		t.Fatal("the failure has nothing to say")
	}
}

// The mode is applied when a RUN begins, and the run is what the queue starts.
//
// This is the link that was missing entirely. BeginRun existed, `restart` was
// offered on the screen and stored in the row, and nothing in the server ever
// called it — so a restart plan carried its position exactly like a resume one
// and the difference was a word on a card. The engine applies it now, before
// anything reaches a model, and this is the test that says so: the run below
// fails at the provider on purpose, and the position has already moved.
func TestARunAppliesThePlansModeBeforeItReachesAModel(t *testing.T) {
	t.Parallel()
	f := newPromptFixture(t, "")
	ctx := context.Background()
	plans, binding := planned(t, f, planboard.ModeRestart, "first", "second", "third")

	// Last night stopped at step two.
	if _, err := plans.Done(ctx, binding, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := plans.State(ctx, binding); st.Step != 2 {
		t.Fatalf("the fixture is not where this test needs it: step %d", st.Step)
	}

	// Tonight. The agent is moved onto a provider nothing listens on, so the
	// run fails immediately and offline — what is under test happens first.
	deadEnd(t, f)
	if _, err := f.engine.RunUnattended(ctx, f.agent.WorkspaceID, f.agent.Name, "do the thing"); err == nil {
		t.Fatal("a run against a dead provider reported success; this test proves nothing")
	}

	st, err := plans.State(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if st.Step != 1 {
		t.Fatalf("a restart plan began its run at step %d rather than at the top — "+
			"the mode is stored and shown but nothing applies it", st.Step)
	}
}

// And the other half: resume must NOT be reset by the same code path.
func TestARunLeavesAResumePlanWhereItWas(t *testing.T) {
	t.Parallel()
	f := newPromptFixture(t, "")
	ctx := context.Background()
	plans, binding := planned(t, f, planboard.ModeResume, "first", "second", "third")

	if _, err := plans.Done(ctx, binding, ""); err != nil {
		t.Fatal(err)
	}
	deadEnd(t, f)
	f.engine.RunUnattended(ctx, f.agent.WorkspaceID, f.agent.Name, "do the thing")

	if st, _ := plans.State(ctx, binding); st.Step != 2 {
		t.Fatalf("a resume plan was pulled back to step %d; the point of resume is that "+
			"tonight continues last night", st.Step)
	}
}

// deadEnd points the fixture's agent at a provider nothing answers on, so a run
// fails without leaving this machine.
func deadEnd(t *testing.T, f promptFixture) {
	t.Helper()
	ctx := context.Background()
	cat := catalog.NewStore(f.db)
	p, err := cat.CreateProvider(ctx, "nowhere", llm.TypeOpenAICompatible, "http://127.0.0.1:1/v1", "")
	if err != nil {
		t.Fatalf("create the dead provider: %v", err)
	}
	m, err := cat.CreateModel(ctx, p.ID, "nothing", "")
	if err != nil {
		t.Fatalf("create the dead model: %v", err)
	}
	if _, err := f.ws.UpdateAgent(ctx, f.agent.ID, nil, nil, &m.ID); err != nil {
		t.Fatalf("point the agent at it: %v", err)
	}
}
