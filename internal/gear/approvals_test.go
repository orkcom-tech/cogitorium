package gear

import (
	"context"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/store"
)

// plainStore is a gear store over an empty database — no workspace, no agent,
// no executor. Everything in this file is about rows.
func plainStore(t *testing.T) (*Store, identity.User) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ana, _, err := identity.NewStore(db).CreateUser(context.Background(), "ana", "admin", "")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return NewStore(db), ana
}

// Two facts this file exists to keep true:
//
//	deleting a gear must not delete the evidence that it ran, and
//	approving a gear must leave a row saying who, when, which version, and what
//	they granted.

func TestDeletingAGearKeepsItsRuns(t *testing.T) {
	ctx := context.Background()
	s, _ := plainStore(t)

	g, err := s.Forge(ctx, "deploy", "ship it", nil, "python", "main.py", "{}", nil,
		[]File{{Path: "main.py", Content: "print(1)"}}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.RecordRun(ctx, Run{GearID: g.ID, Version: g.Version, ExitCode: 0, DurationMs: 5,
			Stdout: "shipped"}); err != nil {
			t.Fatal(err)
		}
	}

	before, err := s.ListRuns(ctx, g.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 {
		t.Fatalf("recorded 3 runs, listed %d", len(before))
	}
	if before[0].GearName != "deploy" {
		t.Fatalf("a run must carry the gear's name, got %q", before[0].GearName)
	}

	if err := s.Delete(ctx, g.ID); err != nil {
		t.Fatal(err)
	}

	// The runs are still there. This is the whole point: the moment you most
	// need to know what a gear did is after deciding it should not exist.
	var kept, name int
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(gear_name = 'deploy') FROM gear_runs WHERE gear_id IS NULL`)
	if err := row.Scan(&kept, &name); err != nil {
		t.Fatal(err)
	}
	if kept != 3 {
		t.Fatalf("deleting the gear destroyed its history: %d of 3 runs survive", kept)
	}
	if name != 3 {
		t.Fatalf("%d of 3 surviving runs still say which gear they were", name)
	}
}

func TestApprovingLeavesATrail(t *testing.T) {
	ctx := context.Background()
	s, ana := plainStore(t)

	g, err := s.Forge(ctx, "deploy", "ship it", nil, "python", "main.py", "{}",
		[]string{"DEPLOY_KEY"}, []File{{Path: "main.py", Content: "print(1)"}}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNetwork(ctx, g.ID, true, []string{"api.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, g.ID, StatusApproved, Actor{ID: &ana.ID, Name: ana.Name}); err != nil {
		t.Fatal(err)
	}

	trail, err := s.Approvals(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trail) != 1 {
		t.Fatalf("one approval left %d trail rows", len(trail))
	}
	a := trail[0]
	if a.UserName != "ana" || a.UserID == nil || *a.UserID != ana.ID {
		t.Fatalf("the trail must name who decided, got %q/%v", a.UserName, a.UserID)
	}
	if a.Version != g.Version {
		t.Fatalf("the trail must name the version approved: %d, gear is at %d", a.Version, g.Version)
	}
	if a.Status != StatusApproved {
		t.Fatalf("status %q", a.Status)
	}
	// The grants in force at the moment of the decision — most of what makes
	// the decision dangerous, and what a status column cannot say.
	if !strings.Contains(a.EnvNames, "DEPLOY_KEY") {
		t.Fatalf("the credentials granted are missing from the trail: %q", a.EnvNames)
	}
	if !strings.Contains(a.Network, "api.example.com") {
		t.Fatalf("the network reach granted is missing from the trail: %q", a.Network)
	}
}

func TestTheTrailIsAppendOnlyAndOrdered(t *testing.T) {
	ctx := context.Background()
	s, _ := plainStore(t)
	g, _ := s.Forge(ctx, "deploy", "ship it", nil, "python", "main.py", "{}", nil,
		[]File{{Path: "main.py", Content: "print(1)"}}, 0, 0)

	for _, st := range []string{StatusApproved, StatusDisabled, StatusApproved} {
		if _, err := s.SetStatus(ctx, g.ID, st, Actor{Name: "ana"}); err != nil {
			t.Fatal(err)
		}
	}
	trail, err := s.Approvals(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trail) != 3 {
		t.Fatalf("three decisions left %d rows — nothing here may overwrite anything", len(trail))
	}
	if trail[0].Status != StatusApproved || trail[1].Status != StatusDisabled {
		t.Fatalf("newest first: %s, %s, %s", trail[0].Status, trail[1].Status, trail[2].Status)
	}
}

func TestApprovedVersionCatchesAGearEditedAfterApproval(t *testing.T) {
	ctx := context.Background()
	s, _ := plainStore(t)
	g, _ := s.Forge(ctx, "deploy", "ship it", nil, "python", "main.py", "{}", nil,
		[]File{{Path: "main.py", Content: "print(1)"}}, 0, 0)

	if _, ok, _ := s.ApprovedVersion(ctx, g.ID); ok {
		t.Fatal("a gear nobody approved must not report an approved version")
	}
	if _, err := s.SetStatus(ctx, g.ID, StatusApproved, Actor{Name: "ana"}); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.ApprovedVersion(ctx, g.ID)
	if err != nil || !ok || v != g.Version {
		t.Fatalf("approved version %d/%v (err %v), gear is v%d", v, ok, err, g.Version)
	}

	// Edited afterwards: still "approved", now running code nobody read.
	edited, err := s.Forge(ctx, "deploy", "ship it", nil, "python", "main.py", "{}", nil,
		[]File{{Path: "main.py", Content: "print(2)"}}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	v, ok, _ = s.ApprovedVersion(ctx, g.ID)
	if !ok {
		t.Fatal("the approval did not stop existing because the gear changed")
	}
	if v == edited.Version {
		t.Fatalf("the trail must still name v%d, the version somebody actually read, not v%d", v, edited.Version)
	}
}
