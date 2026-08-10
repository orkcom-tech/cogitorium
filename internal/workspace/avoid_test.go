package workspace

import (
	"context"
	"reflect"
	"testing"
)

// Rules is what turns the operator's free text into the prohibitions the
// model is given. The prompt, the API and the log all go through it, so what
// an operator is told they wrote has to be what the agent is told to obey —
// a line silently dropped here is a rule nobody knows is missing.
func TestRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		avoid string
		want  []string
	}{
		{"nothing to avoid", "", []string{}},
		{"only whitespace", "  \n\t\n   ", []string{}},
		{"one rule", "Never spend money.", []string{"Never spend money."}},
		{
			name:  "one per line",
			avoid: "Never spend money.\nNever email a customer.",
			want:  []string{"Never spend money.", "Never email a customer."},
		},
		{
			// An operator separates thoughts with a blank line. A blank bullet
			// in the prompt reads as a rule with no content.
			name:  "blank lines between rules are not rules",
			avoid: "Never spend money.\n\n\nNever email a customer.\n",
			want:  []string{"Never spend money.", "Never email a customer."},
		},
		{
			name:  "indentation and trailing spaces are trimmed",
			avoid: "   Never spend money.   \n\t- Never email a customer.\t",
			want:  []string{"Never spend money.", "- Never email a customer."},
		},
		{
			// Text pasted from a Windows editor must not leave a stray \r on
			// the end of every rule.
			name:  "carriage returns do not survive",
			avoid: "Never spend money.\r\nNever email a customer.\r\n",
			want:  []string{"Never spend money.", "Never email a customer."},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Rules(tc.avoid); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Rules(%q) = %#v, want %#v", tc.avoid, got, tc.want)
			}
		})
	}
}

// A clone is the same setup under a new owner, and prohibitions are part of
// what an agent is. A copy that quietly drops them is a copy that will do the
// thing the original was forbidden to do.
func TestCloneCopiesProhibitions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	src := f.createWorkspace(t, "atlas", "the atlas project", f.alice.ID)
	orch, err := f.ws.GetAgentByName(ctx, src.ID, OrchestratorName)
	if err != nil {
		t.Fatalf("get orchestrator: %v", err)
	}
	if _, err := f.ws.SetAgentAvoid(ctx, orch.ID, "Never spend money.\nNever email a customer."); err != nil {
		t.Fatalf("set orchestrator prohibitions: %v", err)
	}
	if _, err := f.ws.CreateAgentSpec(ctx, src.ID, AgentSpec{
		Name: "researcher", Role: "You find sources.",
		Avoid: "Never cite a paper you did not read.", ModelID: &f.smallModel,
	}); err != nil {
		t.Fatalf("create researcher: %v", err)
	}

	clone, err := f.ws.Clone(ctx, src.ID, "atlas copy", f.bob.ID)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	for name, want := range map[string]string{
		OrchestratorName: "Never spend money.\nNever email a customer.",
		"researcher":     "Never cite a paper you did not read.",
	} {
		a, err := f.ws.GetAgentByName(ctx, clone.ID, name)
		if err != nil {
			t.Fatalf("clone has no agent %q: %v", name, err)
		}
		if a.Avoid != want {
			t.Errorf("the cloned %q has avoid %q, want %q", name, a.Avoid, want)
		}
	}
}

// The prohibitions are their own write, so that changing them cannot depend
// on — or overwrite — a name, role or model somebody else changed meanwhile.
// Clearing them has to be possible for the same reason a rule can be added.
func TestSetAgentAvoidSetsAndClearsWithoutTouchingTheRest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	ws := f.createWorkspace(t, "atlas", "", f.alice.ID)
	agent, err := f.ws.CreateAgent(ctx, ws.ID, "researcher", "You find sources.", f.smallModel)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if agent.Avoid != "" {
		t.Errorf("a new agent starts with avoid %q, want empty", agent.Avoid)
	}

	updated, err := f.ws.SetAgentAvoid(ctx, agent.ID, "Never cite a paper you did not read.")
	if err != nil {
		t.Fatalf("set prohibitions: %v", err)
	}
	if updated.Avoid != "Never cite a paper you did not read." {
		t.Errorf("avoid is %q after being set", updated.Avoid)
	}
	if updated.Role != agent.Role || updated.Name != agent.Name || updated.ModelID == nil || *updated.ModelID != f.smallModel {
		t.Errorf("setting the prohibitions also changed the agent: %+v, was %+v", updated, agent)
	}

	// Reading it back through the list path too: a column filled in on one
	// read path and defaulted on another is how an operator sees a rule the
	// prompt never gets.
	agents, err := f.ws.ListAgents(ctx, ws.ID)
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	for _, a := range agents {
		if a.ID == agent.ID && a.Avoid != updated.Avoid {
			t.Errorf("ListAgents reports avoid %q, GetAgent reports %q", a.Avoid, updated.Avoid)
		}
	}

	cleared, err := f.ws.SetAgentAvoid(ctx, agent.ID, "")
	if err != nil {
		t.Fatalf("clear prohibitions: %v", err)
	}
	if cleared.Avoid != "" {
		t.Errorf("avoid is %q after being cleared, want empty — an operator who deletes a rule must be able to", cleared.Avoid)
	}
}
