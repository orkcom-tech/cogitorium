package work

// These run against a real SQLite database built by the real migration runner.
// Nothing is stubbed, and nothing needs to be: this package holds no engine, no
// HTTP and no model, which is most of the reason it is a package at all.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/store"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func enqueue(t *testing.T, s *Store, wsID int64, key string) Unit {
	t.Helper()
	u, err := s.Enqueue(context.Background(), Unit{
		Kind: KindDelivery, WorkspaceID: wsID, Lane: Lane(wsID), IdemKey: key,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return u
}

// One unit runs per lane, and the rest WAIT — which is the whole reason this
// table exists. Before it, a second delivery into a busy workspace was settled
// failed and answered 429: a burst of two hundred was one job done and a
// hundred and ninety-nine losses.
func TestALaneRunsOneUnitAtATimeAndTheRestWait(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for range 3 {
		enqueue(t, s, 1, "")
	}
	other := enqueue(t, s, 2, "")

	first, err := s.Claim(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if first.Lane != Lane(1) {
		t.Fatalf("claimed %q first, want the oldest unit's lane", first.Lane)
	}

	// The next claim must skip that lane entirely and take the other
	// workspace's unit — not block, and not take a second unit from a lane that
	// is already running one.
	second, err := s.Claim(ctx)
	if err != nil {
		t.Fatalf("claim while a lane is busy: %v", err)
	}
	if second.ID != other.ID {
		t.Fatalf("claimed unit %d, want the unit in the free lane (%d)", second.ID, other.ID)
	}

	// Nothing left that is runnable: both lanes are busy, and the two units
	// still queued behind the first are waiting rather than lost.
	if _, err := s.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a third claim took something from a busy lane: %v", err)
	}
	queued, claimed, err := s.Depth(ctx, Lane(1))
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if queued != 2 || claimed != 1 {
		t.Fatalf("lane 1 holds %d queued and %d claimed, want 2 and 1", queued, claimed)
	}

	// And once the running unit finishes, the lane opens for the next one.
	if err := s.Settle(ctx, first.ID, ""); err != nil {
		t.Fatalf("settle: %v", err)
	}
	third, err := s.Claim(ctx)
	if err != nil {
		t.Fatalf("claim after the lane freed: %v", err)
	}
	if third.Lane != Lane(1) {
		t.Fatalf("the freed lane was not picked up: claimed %q", third.Lane)
	}
}

// Many workers, one lane, one winner. This is the property the whole design
// rests on, so it is measured under real concurrency rather than argued from
// the DDL.
func TestConcurrentClaimsNeverDoubleRunALane(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	const units = 12
	for range units {
		enqueue(t, s, 1, "")
	}

	var wg sync.WaitGroup
	claimed := make([]int, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, err := s.Claim(ctx)
				if errors.Is(err, ErrNotFound) {
					return
				}
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				claimed[i]++
				// Deliberately NOT settled: every successful claim holds the
				// lane, so more than one in total means the rule broke.
			}
		}()
	}
	wg.Wait()

	total := 0
	for _, n := range claimed {
		total += n
	}
	if total != 1 {
		t.Fatalf("%d workers claimed %d units from one lane; exactly one may hold it", len(claimed), total)
	}
}

// A caller's retry gets the original unit, not a second execution and not a
// refusal. Idempotency that answers with an error just moves the problem to
// whoever wrote the retry loop.
func TestARepeatedIdempotencyKeyReturnsTheOriginal(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	first := enqueue(t, s, 1, "7:triage:abc")

	again, err := s.Enqueue(ctx, Unit{
		Kind: KindDelivery, WorkspaceID: 1, Lane: Lane(1), IdemKey: "7:triage:abc",
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("a repeated key was not recognised: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("a repeated key produced unit %d, want the original %d", again.ID, first.ID)
	}

	queued, _, err := s.Depth(ctx, Lane(1))
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if queued != 1 {
		t.Fatalf("a repeated key left %d units queued, want 1", queued)
	}

	// Unkeyed units are all different from each other: a partial index treats
	// NULLs as distinct, and that is the behaviour wanted rather than a quirk
	// tolerated.
	for range 3 {
		enqueue(t, s, 1, "")
	}
	if queued, _, _ = s.Depth(ctx, Lane(1)); queued != 4 {
		t.Fatalf("three unkeyed units left the queue at %d, want 4", queued)
	}
}

// A unit that was running when the server stopped is DEAD, not retried.
//
// Requeueing reads as obviously kind and is the wrong call: that unit may have
// spent tokens, written four files and sent something outward, and nothing in
// the row says how far it got. Running it again is a second execution nobody
// asked for.
func TestUnitsRunningAtShutdownAreNotSilentlyRerun(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	enqueue(t, s, 1, "")
	running, err := s.Claim(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	n, err := s.ReleaseClaims(ctx)
	if err != nil {
		t.Fatalf("release claims: %v", err)
	}
	if n != 1 {
		t.Fatalf("released %d claims, want 1", n)
	}

	after, err := s.Get(ctx, running.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.State != StateDead {
		t.Fatalf("a unit interrupted by a restart is in state %q, want %q", after.State, StateDead)
	}
	if _, err := s.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a unit interrupted by a restart was picked up and run again: %v", err)
	}
}

// A unit not due yet is not claimed. Nothing sets run_after in the future today
// — the scheduler will — and the column is here so that adding a clock later is
// a writer rather than a migration.
func TestAUnitIsNotClaimedBeforeItIsDue(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.Enqueue(ctx, Unit{
		Kind: KindDelivery, WorkspaceID: 1, Lane: Lane(1),
		RunAfter: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := s.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a unit due in an hour was claimed now: %v", err)
	}

	// Control: the same unit dated now IS claimed, so the check above measures
	// the date and not an empty queue.
	enqueue(t, s, 1, "")
	if _, err := s.Claim(ctx); err != nil {
		t.Fatalf("a due unit was not claimed: %v", err)
	}
}

// Cancel works on a unit that is waiting and on one that is running, because an
// operator pressing stop does not know or care which it was.
func TestCancelReachesBothWaitingAndRunningUnits(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Claim first, THEN enqueue the one that waits: a claim takes the oldest
	// runnable unit, so enqueuing in the other order would leave both variables
	// naming the same row and the test would cancel it twice.
	enqueue(t, s, 2, "")
	running, err := s.Claim(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	waiting := enqueue(t, s, 1, "")

	for _, u := range []Unit{waiting, running} {
		if err := s.Kill(ctx, u.ID, "the operator stopped it"); err != nil {
			t.Fatalf("cancel unit %d in state %q: %v", u.ID, u.State, err)
		}
		got, err := s.Get(ctx, u.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.State != StateDead || got.LastError == "" {
			t.Fatalf("cancelled unit %d is %q with reason %q", u.ID, got.State, got.LastError)
		}
	}

	// A settled unit cannot be cancelled: there is nothing to stop, and saying
	// otherwise would let a stale button rewrite history.
	third := enqueue(t, s, 3, "")
	got, err := s.Claim(ctx)
	if err != nil || got.ID != third.ID {
		t.Fatalf("claim the third unit: %v", err)
	}
	if err := s.Settle(ctx, third.ID, ""); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if err := s.Kill(ctx, third.ID, "too late"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelling a finished unit was accepted: %v", err)
	}
}

// Settled units are pruned; live ones never are. A queue is the one table in
// this schema that grows forever by design rather than by accident — one row
// per delivery and per chat turn, for as long as the install runs.
func TestPruneTakesSettledUnitsAndLeavesLiveOnes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	done := enqueue(t, s, 1, "")
	if _, err := s.Claim(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.Settle(ctx, done.ID, ""); err != nil {
		t.Fatalf("settle: %v", err)
	}
	waiting := enqueue(t, s, 2, "")

	// Nothing is old enough yet.
	if n, err := s.Prune(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("prune with an hour's window removed %d units (err %v)", n, err)
	}
	// With a zero window every settled unit is past it.
	n, err := s.Prune(ctx, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d units, want 1", n)
	}
	if _, err := s.Get(ctx, done.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the settled unit survived the prune: %v", err)
	}
	if _, err := s.Get(ctx, waiting.ID); err != nil {
		t.Fatalf("prune took a unit that was still waiting: %v", err)
	}
}
