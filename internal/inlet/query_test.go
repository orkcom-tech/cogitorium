package inlet

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/store"
)

// A run's record, written the way the engine writes it.
func recorded(t *testing.T, s *Store, wsID int64, state string, did map[string]any) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := s.Accept(ctx, wsID, ptr(0), "tickets", "triage", "orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(did)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(ctx, id, state, "done", "", raw); err != nil {
		t.Fatal(err)
	}
	return id
}

func queryFixture(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func ids(runs []Run) []int64 {
	out := []int64{}
	for _, r := range runs {
		out = append(out, r.ID)
	}
	return out
}

func TestTheRecordCanBeAskedWhichRunsCalledAGear(t *testing.T) {
	s := queryFixture(t)
	ctx := context.Background()

	deployed := recorded(t, s, 1, StateCompleted, map[string]any{
		"tools": []map[string]any{{"name": "gear_deploy", "agent": "releaser", "ok": true}},
	})
	recorded(t, s, 1, StateCompleted, map[string]any{
		"tools": []map[string]any{{"name": "write_file", "agent": "writer", "ok": true}},
	})
	// A run whose FILE is called deploy.md, to prove this is not a text search
	// over the whole record.
	recorded(t, s, 1, StateCompleted, map[string]any{
		"tools": []map[string]any{{"name": "write_file", "agent": "writer", "ok": true}},
		"files": []map[string]any{{"path": "out/gear_deploy.md", "bytes": 12}},
	})

	got, err := s.FindRuns(ctx, 1, Query{Tool: "gear_deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != deployed {
		t.Fatalf("asking which runs called gear_deploy returned %v; want just %d", ids(got), deployed)
	}
}

func TestTheRecordCanBeAskedByAgentContextAndFile(t *testing.T) {
	s := queryFixture(t)
	ctx := context.Background()

	// A delegated worker four levels down did the work; the run started at the
	// orchestrator. Asking by agent has to find it anyway.
	deep := recorded(t, s, 1, StateCompleted, map[string]any{
		"tools":   []map[string]any{{"name": "gear_lint", "agent": "critic", "depth": 3, "ok": true}},
		"context": []map[string]any{{"path": "team/style.md", "version": "v4"}},
		"files":   []map[string]any{{"path": "out/report.md", "bytes": 90}},
	})
	recorded(t, s, 1, StateCompleted, map[string]any{
		"tools":   []map[string]any{{"name": "write_file", "agent": "writer", "ok": true}},
		"context": []map[string]any{{"path": "team/rules.md", "version": "v1"}},
		"files":   []map[string]any{{"path": "out/other.md", "bytes": 3}},
	})

	for _, c := range []struct {
		what string
		q    Query
	}{
		{"agent", Query{Agent: "critic"}},
		{"context document", Query{Context: "team/style.md"}},
		{"file produced", Query{File: "out/report.md"}},
	} {
		got, err := s.FindRuns(ctx, 1, c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		if len(got) != 1 || got[0].ID != deep {
			t.Fatalf("asking by %s returned %v; want just %d", c.what, ids(got), deep)
		}
	}
}

func TestFiltersCombineAndFailedMeansEveryWayItDidNotLand(t *testing.T) {
	s := queryFixture(t)
	ctx := context.Background()

	wanted := recorded(t, s, 1, StateFailed, map[string]any{
		"tools": []map[string]any{{"name": "gear_deploy", "agent": "releaser", "ok": false}},
	})
	// Same gear, succeeded — must not come back under Failed.
	recorded(t, s, 1, StateCompleted, map[string]any{
		"tools": []map[string]any{{"name": "gear_deploy", "agent": "releaser", "ok": true}},
	})
	// Failed, different gear — must not come back under the combined query.
	other := recorded(t, s, 1, StateRefusedExpectation, map[string]any{
		"tools": []map[string]any{{"name": "write_file", "agent": "writer", "ok": true}},
	})

	got, err := s.FindRuns(ctx, 1, Query{Tool: "gear_deploy", Failed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != wanted {
		t.Fatalf("failed runs that called gear_deploy: %v; want just %d", ids(got), wanted)
	}

	// And Failed on its own catches the refusals, which are not "failed" but
	// are also not work that landed.
	all, err := s.FindRuns(ctx, 1, Query{Failed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("what went wrong returned %v; want the failure and the refusal (%d, %d)", ids(all), wanted, other)
	}
}

func TestARunWithNoRecordIsNeverClaimedToHaveDoneNothing(t *testing.T) {
	// An empty `did` means NO RECORD WAS KEPT — it ran before records existed,
	// or it is still in flight — which is not the same as a record showing
	// nothing happened. It must not answer "which runs did not call this gear"
	// by appearing, and must not answer "which did" either.
	s := queryFixture(t)
	ctx := context.Background()
	id, err := s.Accept(ctx, 1, ptr(0), "tickets", "triage", "orchestrator")
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.FindRuns(ctx, 1, Query{Tool: "gear_deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a run with no record was claimed to have called a gear: %v", ids(got))
	}
	// It is still listed when nothing is being asked about its record.
	all, err := s.FindRuns(ctx, 1, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != id {
		t.Fatalf("the plain listing lost the run: %v", ids(all))
	}
}

func TestQueriesAreScopedToTheirWorkspace(t *testing.T) {
	s := queryFixture(t)
	ctx := context.Background()
	recorded(t, s, 1, StateCompleted, map[string]any{
		"tools": []map[string]any{{"name": "gear_deploy", "agent": "releaser", "ok": true}},
	})
	elsewhere := recorded(t, s, 2, StateCompleted, map[string]any{
		"tools": []map[string]any{{"name": "gear_deploy", "agent": "releaser", "ok": true}},
	})

	got, err := s.FindRuns(ctx, 1, Query{Tool: "gear_deploy"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.ID == elsewhere {
			t.Fatal("a query returned a run from another workspace")
		}
	}
}

// ptr is what these tests need now that a run's inlet is nullable — see Accept.
// A clock firing straight at an agent or a gear has no door at all, so the
// column had to admit NULL and the argument had to admit nil.
func ptr(n int64) *int64 { return &n }
