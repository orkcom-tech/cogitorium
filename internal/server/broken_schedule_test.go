package server

import (
	"context"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/schedule"
)

// A schedule whose target was deleted must stop firing.
//
// Not stop existing: the row is what lets somebody point it at another agent,
// and a schedule that vanished with its agent is a nightly job that silently
// stopped and nobody learns why until the work is noticed missing. But left
// enabled it fired and FAILED on every tick — a clock set to a minute wrote a
// failure a minute, none of which said anything the first one had not.
func TestABrokenScheduleSwitchesItselfOff(t *testing.T) {
	in := newInstall(t, "", nil)
	ctx := context.Background()

	provider, err := in.cat.CreateProvider(ctx, "house", "openai-compatible", deadProvider, "sk-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	model, err := in.cat.CreateModel(ctx, provider.ID, "test-model", "house / test-model")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	ws, err := in.spaces.CreateWorkspace(ctx, "clocks", "", model.ID, 1)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	// A worker, not the orchestrator: the orchestrator is the entry point and
	// cannot be deleted, which is a different rule and rightly so.
	target, err := in.spaces.CreateAgent(ctx, ws.ID, "sweeper", "you sweep", model.ID)
	if err != nil {
		t.Fatalf("create the agent: %v", err)
	}

	made, err := in.srv.schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: ws.ID, TargetKind: schedule.TargetAgent, TargetAgentID: &target.ID,
		Name: "nightly", Spec: "every 1m", Instruction: "go", Enabled: true,
	})
	if err != nil {
		t.Fatalf("creating the schedule: %v", err)
	}

	if err := in.spaces.DeleteAgent(ctx, target.ID); err != nil {
		t.Fatalf("deleting the agent: %v", err)
	}

	broken, err := in.srv.schedules.Get(ctx, made.ID)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if !broken.Broken() {
		t.Fatal("deleting the agent left the schedule still pointing at it")
	}

	in.srv.fire(ctx, broken)

	after, err := in.srv.schedules.Get(ctx, made.ID)
	if err != nil {
		t.Fatalf("the schedule was removed rather than switched off; there is nothing left to repoint: %v", err)
	}
	if after.Enabled {
		t.Error("a broken schedule is still enabled after a tick; it will fail again on the next one, and every one after that")
	}
}
