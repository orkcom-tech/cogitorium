package workflow

import (
	"context"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/planboard"
	"github.com/orkcom-tech/cogitorium/internal/schedule"
	"github.com/orkcom-tech/cogitorium/internal/store"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

type fixture struct {
	st      Stores
	vers    *Store
	wsID    int64
	modelID int64
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	cat := catalog.NewStore(db)
	provider, err := cat.CreateProvider(ctx, "house", "openai-compatible", "http://127.0.0.1:9", "k")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	model, err := cat.CreateModel(ctx, provider.ID, "m", "house / m")
	if err != nil {
		t.Fatalf("model: %v", err)
	}

	// A workspace has an owner, and the row insists.
	admin, _, err := identity.NewStore(db).Bootstrap(ctx, identity.Seeds{})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	spaces := workspace.NewStore(db)
	ws, err := spaces.CreateWorkspace(ctx, "research", "for the test", model.ID, admin.ID)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	return fixture{
		st: Stores{
			Spaces:     spaces,
			Gears:      gear.NewStore(db),
			Schedules:  schedule.NewStore(db),
			Planboards: planboard.NewStore(db),
		},
		vers:    NewStore(db),
		wsID:    ws.ID,
		modelID: model.ID,
	}
}

// The whole point, in one test: save what a workflow is, break it, and get it
// back.
//
// Not "the snapshot has the right fields" — that passes while restore silently
// drops half of them. What is asserted is the round trip, because the round
// trip is the only thing anybody actually wants from a version.
func TestAWorkflowComesBack(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	drafter, err := f.st.Spaces.CreateAgent(ctx, f.wsID, "drafter", "you draft", f.modelID)
	if err != nil {
		t.Fatalf("create drafter: %v", err)
	}
	checker, err := f.st.Spaces.CreateAgent(ctx, f.wsID, "checker", "you check", f.modelID)
	if err != nil {
		t.Fatalf("create checker: %v", err)
	}
	if _, err := f.st.Spaces.CreateWire(ctx, f.wsID, drafter.ID, checker.ID, "drafts"); err != nil {
		t.Fatalf("wire: %v", err)
	}
	if _, err := f.st.Spaces.CreateContextBinding(ctx, f.wsID, "library/house-style.md", &drafter.ID); err != nil {
		t.Fatalf("bind context: %v", err)
	}
	g, err := f.st.Gears.Forge(ctx, "count", "counts", nil, "python", "main.py", "", nil,
		[]gear.File{{Path: "main.py", Content: "print(1)"}}, 0, 0)
	if err != nil {
		t.Fatalf("forge: %v", err)
	}
	if _, err := f.st.Gears.Bind(ctx, g.ID, f.wsID, &checker.ID); err != nil {
		t.Fatalf("bind gear: %v", err)
	}
	if _, err := f.st.Schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: f.wsID, TargetKind: schedule.TargetAgent, TargetAgentID: &drafter.ID,
		Name: "nightly", Spec: "every 1h", Instruction: "draft", Enabled: true,
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	saved, err := Take(ctx, f.st, f.wsID)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if len(saved.Agents) != 3 || len(saved.Wires) != 1 || len(saved.Gears) != 1 ||
		len(saved.Context) != 1 || len(saved.Schedules) != 1 {
		t.Fatalf("the snapshot did not record the workflow: %s", saved.Summary())
	}

	// Now take it apart, the way a bad afternoon does.
	if err := f.st.Spaces.DeleteAgent(ctx, checker.ID); err != nil {
		t.Fatalf("delete checker: %v", err)
	}
	if _, err := f.st.Spaces.UpdateAgent(ctx, drafter.ID, nil, ptr("you do something else entirely"), nil); err != nil {
		t.Fatalf("change drafter: %v", err)
	}
	clocks, _ := f.st.Schedules.List(ctx, f.wsID)
	for _, sc := range clocks {
		if err := f.st.Schedules.Delete(ctx, sc.ID); err != nil {
			t.Fatalf("delete clock: %v", err)
		}
	}

	missing, err := Restore(ctx, f.st, f.wsID, saved)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("restoring an untouched library reported losses: %v", missing)
	}

	back, err := Take(ctx, f.st, f.wsID)
	if err != nil {
		t.Fatalf("take again: %v", err)
	}
	if !Same(saved, back) {
		t.Errorf("the workflow did not come back.\nsaved: %s\nafter: %s", saved.Summary(), back.Summary())
		t.Errorf("agents saved=%d after=%d, wires %d/%d, gears %d/%d, context %d/%d, clocks %d/%d",
			len(saved.Agents), len(back.Agents), len(saved.Wires), len(back.Wires),
			len(saved.Gears), len(back.Gears), len(saved.Context), len(back.Context),
			len(saved.Schedules), len(back.Schedules))
	}
}

// A gear the library no longer has cannot be conjured back, and the honest
// answer is to restore everything else and say which one is gone.
func TestARestoreSaysWhatItCouldNotBringBack(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	g, err := f.st.Gears.Forge(ctx, "doomed", "goes away", nil, "python", "main.py", "", nil,
		[]gear.File{{Path: "main.py", Content: "print(1)"}}, 0, 0)
	if err != nil {
		t.Fatalf("forge: %v", err)
	}
	if _, err := f.st.Gears.Bind(ctx, g.ID, f.wsID, nil); err != nil {
		t.Fatalf("bind: %v", err)
	}

	saved, err := Take(ctx, f.st, f.wsID)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if err := f.st.Gears.Delete(ctx, g.ID); err != nil {
		t.Fatalf("delete the gear: %v", err)
	}

	missing, err := Restore(ctx, f.st, f.wsID, saved)
	if err != nil {
		t.Fatalf("restore refused entirely rather than reporting one loss: %v", err)
	}
	if len(missing) != 1 {
		t.Fatalf("a deleted gear was not reported: %v", missing)
	}
}

// Numbers are never reused, including across a rollback: a history where a
// number means two things is a history nobody can cite.
func TestVersionNumbersOnlyGoUp(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	snap, err := Take(ctx, f.st, f.wsID)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	for want := 1; want <= 3; want++ {
		v, err := f.vers.Save(ctx, f.wsID, snap, "saved", "admin", 0)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if v.Number != want {
			t.Fatalf("version %d was numbered %d", want, v.Number)
		}
	}
	// A rollback records itself rather than deleting what came after.
	rolled, err := f.vers.Save(ctx, f.wsID, snap, "back to v1", "admin", 1)
	if err != nil {
		t.Fatalf("save a rollback: %v", err)
	}
	if rolled.Number != 4 || rolled.RestoredFrom != 1 {
		t.Fatalf("a rollback became v%d restored from %d; it must be v4 from 1", rolled.Number, rolled.RestoredFrom)
	}
	list, err := f.vers.List(ctx, f.wsID)
	if err != nil || len(list) != 4 {
		t.Fatalf("the history is %d entries after four saves: %v", len(list), err)
	}
}

func ptr(s string) *string { return &s }

// A version restores the marker, not only the plan.
//
// This is the half that is easy to leave out and impossible to notice: the
// steps come back, the workflow looks right, and it silently resumes from
// wherever the run that went wrong had pushed it. A rollback that returns the
// map and keeps the wrong pin on it is not a rollback.
func TestARollbackPutsThePlanBackWhereItStood(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	runner, err := f.st.Spaces.CreateAgent(ctx, f.wsID, "runner", "you run the plan", f.modelID)
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	plan, err := f.st.Planboards.Save(ctx, "nightly", "", nil, planboard.ModeResume,
		[]planboard.Step{{Title: "one"}, {Title: "two"}, {Title: "three"}}, 0, 0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	binding, err := f.st.Planboards.Bind(ctx, plan.ID, f.wsID, &runner.ID)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	// Two steps done, so the recorded position is somewhere in the middle
	// rather than at either end where an off-by-one would hide.
	for range 2 {
		if _, err := f.st.Planboards.Done(ctx, binding.ID, ""); err != nil {
			t.Fatal(err)
		}
	}

	saved, err := Take(ctx, f.st, f.wsID)
	if err != nil {
		t.Fatalf("take: %v", err)
	}

	// Now the workflow goes wrong in both ways a plan can: the marker moves,
	// and something attaches a plan that was not there when the version was
	// taken.
	if _, err := f.st.Planboards.Seek(ctx, binding.ID, 1); err != nil {
		t.Fatal(err)
	}
	stray, err := f.st.Planboards.Save(ctx, "stray", "", nil, planboard.ModeResume,
		[]planboard.Step{{Title: "not in the version"}}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Planboards.Bind(ctx, stray.ID, f.wsID, nil); err != nil {
		t.Fatal(err)
	}

	missing, err := Restore(ctx, f.st, f.wsID, saved)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("the restore could not finish: %v", missing)
	}

	back, err := f.st.Planboards.Bindings(ctx, f.wsID)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 {
		t.Fatalf("after the rollback %d plans are attached; the stray one should have gone with it", len(back))
	}
	if back[0].Planboard != "nightly" {
		t.Fatalf("the wrong plan survived the rollback: %q", back[0].Planboard)
	}
	state, err := f.st.Planboards.State(ctx, back[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Step != 3 {
		t.Fatalf("the plan came back on step %d, not where the version says it stood", state.Step)
	}
}
