package planboard

import (
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/store"
)

// The point of this package is that the ORDER is not the model's to choose.
// So the tests are about the position: where it is, what moves it, what does
// not, and what happens to it when the plan underneath changes shape.

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// A real database, because half the rules here are schema: one binding per
// worker, steps unique per ordinal, state cascading away with its binding.
func newBoard(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a real database failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db), db
}

// A workspace and an agent to hang bindings on. Foreign keys are enforced, so
// these have to be real rows rather than invented ids.
func scaffold(t *testing.T, db *sql.DB) (wsID, agentID int64) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO workspaces (name, created_at, updated_at) VALUES ('nightly', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("creating a workspace failed: %v", err)
	}
	wsID, _ = res.LastInsertId()
	res, err = db.Exec(`INSERT INTO agents (workspace_id, name, role, created_at, updated_at) VALUES (?, 'runner', 'worker', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, wsID)
	if err != nil {
		t.Fatalf("creating an agent failed: %v", err)
	}
	agentID, _ = res.LastInsertId()
	return wsID, agentID
}

func steps(titles ...string) []Step {
	out := make([]Step, 0, len(titles))
	for _, ti := range titles {
		out = append(out, Step{Title: ti})
	}
	return out
}

func TestAPlanWithNoStepsIsRefused(t *testing.T) {
	t.Parallel()
	pb, _ := newBoard(t)

	_, err := pb.Save(t.Context(), "empty", "", nil, ModeResume, nil, 0, 0)
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("a plan with no steps was accepted: %v", err)
	}

	// Blank titles are not steps either. A pasted list with a trailing newline
	// must not become a step the agent is asked to report done.
	_, err = pb.Save(t.Context(), "blanks", "", nil, ModeResume, []Step{{Title: "  "}, {Title: ""}}, 0, 0)
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("a plan of blank titles was accepted: %v", err)
	}
}

func TestTheEngineHandsOverOneStepAtATime(t *testing.T) {
	t.Parallel()
	pb, db := newBoard(t)
	ws, agent := scaffold(t, db)
	ctx := t.Context()

	p, err := pb.Save(ctx, "nightly", "", nil, ModeResume, steps("gather", "summarise", "post"), 0, 0)
	if err != nil {
		t.Fatalf("saving the plan failed: %v", err)
	}
	if _, err := pb.Bind(ctx, p.ID, ws, &agent); err != nil {
		t.Fatalf("binding failed: %v", err)
	}

	active, err := pb.Active(ctx, ws, agent)
	if err != nil {
		t.Fatalf("reading the active plans failed: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("expected one plan in front of the agent, got %d", len(active))
	}
	// The whole promise: what reaches the agent is step one, not the plan.
	if active[0].Current.Title != "gather" || active[0].Current.Ordinal != 1 {
		t.Fatalf("the agent was handed %q (step %d), not step one", active[0].Current.Title, active[0].Current.Ordinal)
	}

	if _, err := pb.Done(ctx, active[0].Binding.ID, "got the feed"); err != nil {
		t.Fatalf("closing the step failed: %v", err)
	}
	active, _ = pb.Active(ctx, ws, agent)
	if active[0].Current.Title != "summarise" {
		t.Fatalf("after closing step one the agent was handed %q, not the second step", active[0].Current.Title)
	}
}

func TestTheLastStepWrapsAndCountsACycle(t *testing.T) {
	t.Parallel()
	pb, db := newBoard(t)
	ws, agent := scaffold(t, db)
	ctx := t.Context()

	p, _ := pb.Save(ctx, "twostep", "", nil, ModeResume, steps("one", "two"), 0, 0)
	b, _ := pb.Bind(ctx, p.ID, ws, &agent)

	if _, err := pb.Done(ctx, b.ID, ""); err != nil {
		t.Fatalf("closing step one failed: %v", err)
	}
	st, err := pb.Done(ctx, b.ID, "")
	if err != nil {
		t.Fatalf("closing the last step failed: %v", err)
	}
	// A plan that stops at the end is a workflow that does nothing on its
	// second night. It wraps, and the cycle count is what a person means by
	// "how many times has this gone round".
	if st.Step != 1 {
		t.Fatalf("after the last step the position is %d, not back at one", st.Step)
	}
	if st.Cycle != 1 {
		t.Fatalf("after one full pass the cycle count is %d, not 1", st.Cycle)
	}
}

func TestResumeCarriesThePositionAndRestartDoesNot(t *testing.T) {
	t.Parallel()
	pb, db := newBoard(t)
	ws, agent := scaffold(t, db)
	ctx := t.Context()

	// Both modes exist because both are ordinary. This is the test that says
	// which is which, so a change to the default is a change to this file.
	resume, _ := pb.Save(ctx, "backlog", "", nil, ModeResume, steps("a", "b", "c"), 0, 0)
	restart, _ := pb.Save(ctx, "checklist", "", nil, ModeRestart, steps("a", "b", "c"), 0, 0)

	rb, _ := pb.Bind(ctx, resume.ID, ws, &agent)
	cb, _ := pb.Bind(ctx, restart.ID, ws, &agent)

	if _, err := pb.Done(ctx, rb.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pb.Done(ctx, cb.ID, ""); err != nil {
		t.Fatal(err)
	}

	// The next run begins — through the door the SERVER uses, not by calling
	// BeginRun per binding. That distinction is the whole of this: BeginRun
	// existed and had no caller outside this file, so `restart` was `resume`
	// with a different word on the card. BeginRunFor is what the engine calls,
	// and if it ever stops calling it this test is what says so.
	if err := pb.BeginRunFor(ctx, ws, agent); err != nil {
		t.Fatal(err)
	}

	rs, _ := pb.State(ctx, rb.ID)
	if rs.Step != 2 {
		t.Fatalf("a resume plan restarted at step %d; the point of resume is that tonight continues last night", rs.Step)
	}
	cs, _ := pb.State(ctx, cb.ID)
	if cs.Step != 1 {
		t.Fatalf("a restart plan came back at step %d, not at the top", cs.Step)
	}
}

func TestABlockedStepIsMetAgainWithTheReasonAttached(t *testing.T) {
	t.Parallel()
	pb, db := newBoard(t)
	ws, agent := scaffold(t, db)
	ctx := t.Context()

	p, _ := pb.Save(ctx, "fragile", "", nil, ModeResume, steps("call the api", "file the report"), 0, 0)
	b, _ := pb.Bind(ctx, p.ID, ws, &agent)

	if _, err := pb.Blocked(ctx, b.ID, "the api answered 503"); err != nil {
		t.Fatalf("recording a block failed: %v", err)
	}

	active, _ := pb.Active(ctx, ws, agent)
	// Not advanced: a step nobody finished is not a step behind us.
	if active[0].Current.Ordinal != 1 {
		t.Fatalf("a blocked step advanced to %d", active[0].Current.Ordinal)
	}
	// And the next run is told what stopped the last one, rather than walking
	// into it blind.
	if active[0].State.BlockedNote != "the api answered 503" {
		t.Fatalf("the reason did not survive to the next run: %q", active[0].State.BlockedNote)
	}

	if _, err := pb.Done(ctx, b.ID, "it answered this time"); err != nil {
		t.Fatal(err)
	}
	st, _ := pb.State(ctx, b.ID)
	if st.BlockedNote != "" {
		t.Fatalf("the block outlived the step it was about: %q", st.BlockedNote)
	}
}

func TestAWorkspaceBindingIsOnePositionSharedByEveryAgent(t *testing.T) {
	t.Parallel()
	pb, db := newBoard(t)
	ws, first := scaffold(t, db)
	ctx := t.Context()

	res, err := db.Exec(`INSERT INTO agents (workspace_id, name, role, created_at, updated_at) VALUES (?, 'second', 'worker', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, ws)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := res.LastInsertId()

	p, _ := pb.Save(ctx, "shared", "", nil, ModeResume, steps("one", "two", "three"), 0, 0)
	if _, err := pb.Bind(ctx, p.ID, ws, nil); err != nil {
		t.Fatalf("binding to the workspace failed: %v", err)
	}

	mine, _ := pb.Active(ctx, ws, first)
	if _, err := pb.Done(ctx, mine[0].Binding.ID, ""); err != nil {
		t.Fatal(err)
	}

	// This is what makes it a WORKFLOW's plan rather than an agent's: whoever
	// runs next picks up the step the last one left.
	theirs, _ := pb.Active(ctx, ws, second)
	if len(theirs) != 1 {
		t.Fatalf("the second agent sees %d plans; a workspace binding reaches every agent", len(theirs))
	}
	if theirs[0].Current.Ordinal != 2 {
		t.Fatalf("the second agent was handed step %d, not the one the first agent left", theirs[0].Current.Ordinal)
	}
}

func TestShorteningAPlanPullsThePositionBackInsideIt(t *testing.T) {
	t.Parallel()
	pb, db := newBoard(t)
	ws, agent := scaffold(t, db)
	ctx := t.Context()

	p, _ := pb.Save(ctx, "shrinking", "", nil, ModeResume, steps("a", "b", "c", "d"), 0, 0)
	b, _ := pb.Bind(ctx, p.ID, ws, &agent)
	if _, err := pb.Seek(ctx, b.ID, 4); err != nil {
		t.Fatal(err)
	}

	// Somebody edits the plan down to two steps while a position points at the
	// fourth. Without the clamp the agent is handed nothing, for ever.
	if _, err := pb.Save(ctx, "shrinking", "", nil, ModeResume, steps("a", "b"), 0, 0); err != nil {
		t.Fatalf("shortening the plan failed: %v", err)
	}

	active, err := pb.Active(ctx, ws, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("after shortening the plan the agent has %d steps in front of it", len(active))
	}
	if active[0].Current.Ordinal != 2 {
		t.Fatalf("the position landed on step %d, not on the new last step", active[0].Current.Ordinal)
	}
}

func TestSeekRefusesAStepThePlanDoesNotHave(t *testing.T) {
	t.Parallel()
	pb, db := newBoard(t)
	ws, agent := scaffold(t, db)
	ctx := t.Context()

	p, _ := pb.Save(ctx, "seven", "", nil, ModeResume, steps("1", "2", "3"), 0, 0)
	b, _ := pb.Bind(ctx, p.ID, ws, &agent)

	// Clamping would hide the mistake: somebody asking for step nine of three
	// has a wrong idea about the plan, and quietly giving them step three
	// leaves them with it.
	if _, err := pb.Seek(ctx, b.ID, 9); err == nil {
		t.Fatal("seeking past the end was accepted")
	}
	if _, err := pb.Seek(ctx, b.ID, 0); err == nil {
		t.Fatal("seeking to step zero was accepted")
	}
	st, _ := pb.State(ctx, b.ID)
	if st.Step != 1 {
		t.Fatalf("a refused seek moved the position to %d", st.Step)
	}
}

func TestBindingTwiceIsTheSameAttachment(t *testing.T) {
	t.Parallel()
	pb, db := newBoard(t)
	ws, agent := scaffold(t, db)
	ctx := t.Context()

	p, _ := pb.Save(ctx, "once", "", nil, ModeResume, steps("a", "b"), 0, 0)
	first, err := pb.Bind(ctx, p.ID, ws, &agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pb.Done(ctx, first.ID, ""); err != nil {
		t.Fatal(err)
	}

	// Somebody clicks attach again, unsure whether they already did. Two rows
	// would be two positions in one plan and the engine could not choose.
	again, err := pb.Bind(ctx, p.ID, ws, &agent)
	if err != nil {
		t.Fatalf("binding an already-bound plan failed: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("binding twice made a second binding (%d then %d)", first.ID, again.ID)
	}
	st, _ := pb.State(ctx, again.ID)
	if st.Step != 2 {
		t.Fatalf("binding again reset the position to %d", st.Step)
	}
}

func TestDeletingAPlanTakesItsPositionsWithIt(t *testing.T) {
	t.Parallel()
	pb, db := newBoard(t)
	ws, agent := scaffold(t, db)
	ctx := t.Context()

	p, _ := pb.Save(ctx, "doomed", "", nil, ModeResume, steps("a", "b"), 0, 0)
	b, _ := pb.Bind(ctx, p.ID, ws, &agent)
	if _, err := pb.Done(ctx, b.ID, ""); err != nil {
		t.Fatal(err)
	}

	if err := pb.Delete(ctx, p.ID); err != nil {
		t.Fatalf("deleting failed: %v", err)
	}

	var orphans int
	if err := db.QueryRow(`SELECT COUNT(*) FROM planboard_state`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	// A position that outlived its plan would attach itself to the next plan
	// that took the name.
	if orphans != 0 {
		t.Fatalf("%d positions survived the plan they belonged to", orphans)
	}
}
