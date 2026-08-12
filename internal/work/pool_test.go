package work

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Waiting instead of being destroyed, end to end: four units into one lane, all
// four run, one at a time, in order.
//
// This is the whole point of the table. Before it, three of these four were
// settled `failed` with the engine's busy error — the same terminal state a
// genuinely broken job gets.
func TestEveryQueuedUnitRunsAndOnlyOnePerLaneAtATime(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var mu sync.Mutex
	var order []int64
	var concurrent, peak int

	pool := NewPool(s, func(ctx context.Context, u Unit) error {
		mu.Lock()
		concurrent++
		peak = max(peak, concurrent)
		order = append(order, u.ID)
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		concurrent--
		mu.Unlock()
		return nil
	}, PoolOptions{Workers: 4, Idle: 5 * time.Millisecond})

	var ids []int64
	for range 4 {
		ids = append(ids, enqueue(t, s, 1, "").ID)
	}
	pool.Start(ctx)
	pool.Wake()
	defer pool.Stop()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 4
	}, "all four units to run")

	mu.Lock()
	defer mu.Unlock()
	if peak != 1 {
		t.Fatalf("%d units of one lane ran at once; a lane runs one at a time", peak)
	}
	for i := range ids {
		if order[i] != ids[i] {
			t.Fatalf("units ran in order %v, want %v — a queue that reorders is not a queue", order, ids)
		}
	}
}

// Different lanes do not wait for each other. Without this the queue is a
// global bottleneck wearing a lane's clothes.
func TestDifferentLanesRunAtTheSameTime(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	release := make(chan struct{})
	var mu sync.Mutex
	running := map[string]bool{}

	pool := NewPool(s, func(ctx context.Context, u Unit) error {
		mu.Lock()
		running[u.Lane] = true
		mu.Unlock()
		<-release
		return nil
	}, PoolOptions{Workers: 4, Idle: 5 * time.Millisecond})

	for ws := int64(1); ws <= 3; ws++ {
		enqueue(t, s, ws, "")
	}
	pool.Start(ctx)
	pool.Wake()
	defer func() { close(release); pool.Stop() }()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(running) == 3
	}, "three lanes to be running at once")
}

// A unit whose work fails is finished, not stuck. The failure belongs to
// whatever the unit was filling in; the queue's only job is to stop holding the
// lane.
func TestAFailingUnitReleasesItsLane(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var runs int
	var mu sync.Mutex
	pool := NewPool(s, func(ctx context.Context, u Unit) error {
		mu.Lock()
		runs++
		mu.Unlock()
		return errors.New("the work itself went wrong")
	}, PoolOptions{Workers: 2, Idle: 5 * time.Millisecond})

	first := enqueue(t, s, 1, "")
	enqueue(t, s, 1, "")
	pool.Start(ctx)
	pool.Wake()
	defer pool.Stop()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return runs == 2
	}, "the second unit to run after the first failed")

	u, err := s.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.State != StateDone {
		t.Fatalf("a unit whose work failed is in state %q, want %q", u.State, StateDone)
	}
	if u.LastError == "" {
		t.Fatal("the failure was not recorded on the unit")
	}
}

// A runner that panics must not take its lane down with it. An unsettled unit
// holds its lane until the process restarts, so the one thing execute may never
// do is return without writing a terminal state.
func TestAPanickingUnitStillReleasesItsLane(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var mu sync.Mutex
	var seen int
	pool := NewPool(s, func(ctx context.Context, u Unit) error {
		mu.Lock()
		seen++
		first := seen == 1
		mu.Unlock()
		if first {
			panic("the runner fell over")
		}
		return nil
	}, PoolOptions{Workers: 2, Idle: 5 * time.Millisecond})

	enqueue(t, s, 1, "")
	enqueue(t, s, 1, "")
	pool.Start(ctx)
	pool.Wake()
	defer pool.Stop()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen == 2
	}, "the lane to be released after a panic")
}

// A chat turn takes the same lane a delivery would queue behind, and is refused
// rather than parked when the lane is busy.
//
// Two latches that could not see each other would let a chat turn and a delivery
// run at once in one workspace — and the turn state they would share holds the
// egress budget, both anti-worm taint latches and the run record, keyed by
// workspace.
func TestAChatTurnAndADeliveryShareOneLane(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	held, err := s.ClaimNow(ctx, Unit{Kind: KindChat, WorkspaceID: 1, Lane: Lane(1)})
	if err != nil {
		t.Fatalf("a chat turn could not take a free lane: %v", err)
	}

	// A second chat turn is refused immediately: a person is waiting on a
	// stream and cannot be parked in a queue.
	if _, err := s.ClaimNow(ctx, Unit{Kind: KindChat, WorkspaceID: 1, Lane: Lane(1)}); !errors.Is(err, ErrLaneBusy) {
		t.Fatalf("a second chat turn in a busy workspace was not refused: %v", err)
	}

	// A delivery may still be QUEUED — that is the difference between the two
	// — but nothing may claim it while the chat turn holds the lane.
	enqueue(t, s, 1, "")
	if _, err := s.Claim(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a delivery was claimed while a chat turn held the lane: %v", err)
	}

	if err := s.Settle(ctx, held.ID, ""); err != nil {
		t.Fatalf("settle the chat turn: %v", err)
	}
	if _, err := s.Claim(ctx); err != nil {
		t.Fatalf("the delivery was not picked up once the chat turn ended: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A unit that finishes while the pool is stopping is still recorded as
// finished.
//
// Stop cancels the workers' context and then waits for units in flight. If the
// settle rode that same context it would be cancelled with everything else, and
// the unit would still be `claimed` in the database — so its lane would be held
// by a process that no longer exists, and every delivery into that workspace
// would queue behind something that will never finish. Shutdown would be how a
// lane gets stuck.
func TestAUnitFinishingDuringShutdownIsStillSettled(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	started := make(chan struct{})
	release := make(chan struct{})
	pool := NewPool(s, func(ctx context.Context, u Unit) error {
		close(started)
		<-release
		return nil
	}, PoolOptions{Workers: 1, Idle: 5 * time.Millisecond})

	u := enqueue(t, s, 1, "")
	pool.Start(ctx)
	pool.Wake()
	<-started

	stopped := make(chan struct{})
	go func() { pool.Stop(); close(stopped) }()

	// Give Stop time to cancel the workers, then let the unit finish inside
	// the shutdown.
	time.Sleep(20 * time.Millisecond)
	close(release)
	<-stopped

	after, err := s.Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.State != StateDone {
		t.Fatalf("a unit that finished during shutdown is %q; its lane is held by a process that is gone", after.State)
	}
}
