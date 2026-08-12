package store

import (
	"strings"
	"testing"
)

// The lane rule is an INDEX, and this proves it against a real database rather
// than against the DDL that was intended.
//
// It matters more than it looks. The obvious way to claim a unit is a guarded
// UPDATE with `WHERE NOT EXISTS (SELECT … WHERE lane = ? AND state='claimed')`,
// and under SQLite's single writer that is correct — so a queue built on the
// subquery alone would pass every test here and be silently wrong the moment a
// second writer exists, because under READ COMMITTED two workers claiming two
// different rows of one lane both see an empty subquery and both succeed. The
// index makes the rule true regardless; the subquery is only a way to avoid
// picking a row that would certainly fail.
func TestOnlyOneUnitPerLaneCanBeClaimed(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	insert := func(lane, state string) error {
		_, err := db.Exec(
			`INSERT INTO work (kind, workspace_id, lane, state, run_after, created_at, updated_at)
			 VALUES ('delivery', 1, ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			lane, state)
		return err
	}

	if err := insert("ws:1", "claimed"); err != nil {
		t.Fatalf("the first claimed unit in a lane was refused: %v", err)
	}
	if err := insert("ws:1", "claimed"); err == nil {
		t.Fatal("a second unit was claimed in a lane that was already busy — the rule this queue rests on is not enforced")
	} else if !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("the refusal did not come from the unique index: %v", err)
	}

	// Waiting is not claiming: a lane may have any number of units queued
	// behind the one running, which is the entire point of having a queue.
	if err := insert("ws:1", "queued"); err != nil {
		t.Fatalf("a queued unit was refused while its lane was busy: %v", err)
	}
	if err := insert("ws:1", "queued"); err != nil {
		t.Fatalf("a second queued unit was refused: %v", err)
	}

	// And another lane is unaffected.
	if err := insert("ws:2", "claimed"); err != nil {
		t.Fatalf("a claim in a different lane was refused: %v", err)
	}
}

// An idempotency key is unique per workspace and kind — and only when there is
// one. A partial index treats NULLs as distinct, which is exactly right:
// deliveries that carry no key are all different from each other.
func TestAnIdempotencyKeyIsUniquePerWorkspace(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	insert := func(ws int, key any) error {
		_, err := db.Exec(
			`INSERT INTO work (kind, workspace_id, lane, idem_key, state, run_after, created_at, updated_at)
			 VALUES ('delivery', ?, 'ws:x', ?, 'queued', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			ws, key)
		return err
	}

	if err := insert(1, "7:triage:abc"); err != nil {
		t.Fatalf("first keyed unit: %v", err)
	}
	if err := insert(1, "7:triage:abc"); err == nil {
		t.Fatal("the same key was accepted twice in one workspace, so a caller's retry would run the job again")
	}
	if err := insert(2, "7:triage:abc"); err != nil {
		t.Fatalf("the same key in a different workspace was refused: %v", err)
	}
	for range 3 {
		if err := insert(1, nil); err != nil {
			t.Fatalf("an unkeyed unit was refused: %v", err)
		}
	}
}

// The ledger can say "waiting" now. Until this state existed, a delivery that
// met a busy workspace was written as `failed` — the same terminal state a
// genuinely broken job gets — and the only way to tell the two apart was to
// string-match an error message.
func TestTheLedgerCanSayQueued(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for _, state := range []string{"queued", "refused_budget", "completed", "failed"} {
		_, err := db.Exec(
			`INSERT INTO inlet_runs (workspace_id, inlet_address, task_name, agent_name, state, created_at, updated_at)
			 VALUES (1, 'tickets', 'triage', 'orchestrator', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			state)
		if err != nil {
			t.Fatalf("the ledger refused the state %q: %v", state, err)
		}
	}
	// And it still refuses one it does not know, so the CHECK is doing work.
	if _, err := db.Exec(
		`INSERT INTO inlet_runs (workspace_id, inlet_address, task_name, agent_name, state, created_at, updated_at)
		 VALUES (1, 'tickets', 'triage', 'orchestrator', 'invented', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err == nil {
		t.Fatal("the ledger accepted a state nothing writes, so the CHECK guards nothing")
	}
}
