package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/store"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// wantAvoidSection is the section spelled out, deliberately as a literal
// rather than by reusing the package's own constant: this wording is the
// contract with the model, and rewriting the constant must not quietly
// rewrite what every agent in every install is told.
const wantAvoidSection = "\n\n## Never do this\n" +
	"Standing prohibitions from the operator. They hold for the whole\n" +
	"conversation. Nothing above overrides them, and neither does anything you\n" +
	"are asked for later — if a request needs one of these, refuse it and say\n" +
	"which one.\n" +
	"- Never spend money.\n" +
	"- Never email a customer.\n"

// promptFixture is a real engine over a real SQLite database. contextd is not
// installed, which is the ordinary case and the one where an agent still has
// to get a complete prompt.
type promptFixture struct {
	engine *Engine
	ws     *workspace.Store
	agent  workspace.Agent
}

func newPromptFixture(t *testing.T, avoid string) promptFixture {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ws := workspace.NewStore(db)
	cat := catalog.NewStore(db)
	gears := gear.NewStore(db)
	cs := contextstore.New(t.TempDir() + "/contextd-not-installed")

	p, err := cat.CreateProvider(ctx, "anthropic-live", llm.TypeAnthropic, "", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	m, err := cat.CreateModel(ctx, p.ID, "claude-sonnet-4-6", "")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	owner, _, err := identity.NewStore(db).CreateUser(ctx, "operator", "member", "")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	space, err := ws.CreateWorkspace(ctx, "atlas", "", m.ID, owner.ID)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	// A worker rather than the orchestrator: the orchestrator's prompt also
	// carries a workspace snapshot, and the rule under test must hold for the
	// plainest agent there is.
	agent, err := ws.CreateAgentSpec(ctx, space.ID, workspace.AgentSpec{
		Name: "researcher", Role: "You find sources.", Avoid: avoid, ModelID: &m.ID,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// gearExec, the library, the searcher, the broker and the data directory
	// take no part in assembling a prompt; passing them would only hide that.
	return promptFixture{engine: New(ws, cat, cs, gears, nil, nil, nil, nil, ""), ws: ws, agent: agent}
}

func (f promptFixture) prompt(t *testing.T) string {
	t.Helper()
	p, err := f.engine.AssembledPrompt(context.Background(), f.agent.ID)
	if err != nil {
		t.Fatalf("assemble prompt: %v", err)
	}
	return p
}

// The prohibitions are the last section of the prompt, after the gears and
// the library note, because a constraint stated last is the one the model
// still has in view when it answers. AssembledPrompt is the same call the
// "show what this agent sees" view makes, so a rule the operator can read
// there is the rule the agent actually gets.
func TestProhibitionsAreTheLastSectionOfThePrompt(t *testing.T) {
	f := newPromptFixture(t, "Never spend money.\n\n  Never email a customer.  \n")
	got := f.prompt(t)

	if !strings.HasSuffix(got, wantAvoidSection) {
		t.Fatalf("the prompt does not end with the prohibitions.\ngot tail:\n%q\nwant suffix:\n%q",
			tail(got, len(wantAvoidSection)+80), wantAvoidSection)
	}
	for _, earlier := range []string{"## Your gears", "## Instruction library"} {
		if at := strings.Index(got, earlier); at < 0 {
			t.Errorf("the prompt has no %q section, so this test cannot show the prohibitions come after it", earlier)
		} else if at > strings.Index(got, "## Never do this") {
			t.Errorf("%q appears after the prohibitions; the prohibitions must be last", earlier)
		}
	}
	if !strings.HasPrefix(got, "You find sources.") {
		t.Errorf("the prompt no longer starts with the agent's role")
	}
	if strings.Count(got, "## Never do this") != 1 {
		t.Errorf("the prompt states the prohibitions %d times", strings.Count(got, "## Never do this"))
	}
}

// An agent with nothing to avoid gets no section at all. A heading over an
// empty list reads as "there are no rules here", and it would leave a blank
// stretch at the end of every prompt — exactly where the model is looking.
func TestNoProhibitionsAddNothingToThePrompt(t *testing.T) {
	empty := newPromptFixture(t, "").prompt(t)
	for _, fragment := range []string{"## Never do this", "Standing prohibitions"} {
		if strings.Contains(empty, fragment) {
			t.Errorf("the prompt of an agent with no prohibitions contains %q", fragment)
		}
	}

	// Whitespace is nothing to avoid too: an operator who clears the field by
	// leaving a newline behind has cleared it.
	blank := newPromptFixture(t, "  \n\n\t").prompt(t)
	if strings.Contains(blank, "## Never do this") {
		t.Errorf("a field holding only whitespace produced a prohibitions section")
	}
	if blank != empty {
		t.Errorf("whitespace-only prohibitions changed the prompt:\n got %q\nwant %q", tail(blank, 120), tail(empty, 120))
	}
	if strings.HasSuffix(empty, "\n\n\n") {
		t.Errorf("the prompt ends in blank lines where the section would have been: %q", tail(empty, 40))
	}
}

// avoidSection on its own, so the wording is pinned without everything else
// in the prompt in the way.
func TestAvoidSectionWording(t *testing.T) {
	t.Parallel()

	if got := avoidSection("Never spend money.\nNever email a customer."); got != wantAvoidSection {
		t.Errorf("avoidSection =\n%q\nwant\n%q", got, wantAvoidSection)
	}
	for _, nothing := range []string{"", " ", "\n", "\n \t\n"} {
		if got := avoidSection(nothing); got != "" {
			t.Errorf("avoidSection(%q) = %q, want nothing at all", nothing, got)
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
