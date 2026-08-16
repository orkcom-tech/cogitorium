package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/engine"
)

// An operator can see the queue, and stop what is in it.
//
// The two arrive together on purpose. A queue nobody can see is discovered by
// being refused by it; a queue that can be seen but not stopped is worse than
// one that cannot be seen at all — fifty jobs visibly waiting behind a run that
// is wedged, and no button.
func TestTheQueueCanBeSeenAndStopped(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()

	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")

	// Hold the lane so the deliveries below certainly wait.
	held := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	d.provider.answers(func(n int, c modelCall) modelReply {
		once.Do(func() { close(held) })
		if n == 1 {
			<-release
			return says("held")
		}
		return says("triaged")
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = d.srv.engine.HandleUserMessage(ctx, d.wsID, "hold the lane", func(engine.Event) {})
	}()
	<-held
	// Release on every exit, including a failed assertion. Without this a test
	// that fails while the lane is held deadlocks in cleanup instead of
	// reporting what went wrong — which is how this test first "hung".
	var releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { letGo(); wg.Wait() }()

	const waiting = 3
	for range waiting {
		go func() { d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`)) }()
	}
	waitFor(t, func() bool {
		var n int
		_ = d.db.QueryRow(`SELECT COUNT(*) FROM work WHERE workspace_id = ? AND state = 'queued'`,
			d.wsID).Scan(&n)
		return n == waiting
	}, "three deliveries to be waiting")

	view := d.queue(t)
	if view.Running != 1 {
		t.Fatalf("the operator's own turn is not shown as running: %+v", view)
	}
	if len(view.Entries) != waiting+1 {
		t.Fatalf("the queue lists %d entries, want %d", len(view.Entries), waiting+1)
	}
	// The running unit comes first and positions count from it, so "how far
	// down am I" is readable rather than inferred.
	if view.Entries[0].State != "claimed" || view.Entries[0].Kind != "chat" {
		t.Fatalf("the first entry is not the running chat turn: %+v", view.Entries[0])
	}
	for i, e := range view.Entries {
		if e.Position != i {
			t.Fatalf("entry %d reports position %d", i, e.Position)
		}
	}

	// Stop the last one while it waits.
	last := view.Entries[len(view.Entries)-1]
	if rec := d.cancelUnit(t, last.Unit); rec.Code != http.StatusNoContent {
		t.Fatalf("cancelling a waiting unit: %d\n%s", rec.Code, rec.Body.String())
	}
	// Its ledger row says so, rather than waiting for an answer nobody will
	// ever write.
	if last.Run != nil {
		var state string
		if err := d.db.QueryRow(`SELECT state FROM inlet_runs WHERE id = ?`, *last.Run).Scan(&state); err != nil {
			t.Fatalf("read the cancelled run: %v", err)
		}
		if state != "interrupted" {
			t.Fatalf("a cancelled delivery's ledger row says %q", state)
		}
	}

	letGo()
	wg.Wait()

	// The two that were not cancelled still run: cancelling one unit is not a
	// way to empty a queue.
	waitFor(t, func() bool {
		var n int
		_ = d.db.QueryRow(`SELECT COUNT(*) FROM inlet_runs WHERE workspace_id = ? AND state = 'completed'`,
			d.wsID).Scan(&n)
		return n == waiting-1
	}, "the units that were not cancelled to run")
}

// Cancelling something already finished is a conflict, not a silent success. A
// stale button must not be able to rewrite a decision somebody already made.
func TestCancellingAFinishedUnitIsRefused(t *testing.T) {
	d := newDoor(t)

	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })

	if rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`)); rec.Code != http.StatusOK {
		t.Fatalf("delivery: %d\n%s", rec.Code, rec.Body.String())
	}
	var unitID int64
	if err := d.db.QueryRow(`SELECT id FROM work WHERE workspace_id = ? ORDER BY id DESC LIMIT 1`,
		d.wsID).Scan(&unitID); err != nil {
		t.Fatalf("find the unit: %v", err)
	}
	if rec := d.cancelUnit(t, unitID); rec.Code != http.StatusConflict {
		t.Fatalf("cancelling a finished unit answered %d, want 409\n%s", rec.Code, rec.Body.String())
	}
}

func (d *door) queue(t *testing.T) queueView {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+itoa(d.wsID)+"/queue", nil)
	req.Header.Set("Authorization", "Bearer "+d.adminTok)
	rec := httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("read the queue: %d\n%s", rec.Code, rec.Body.String())
	}
	var view queueView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode the queue: %v", err)
	}
	return view
}

func (d *door) cancelUnit(t *testing.T, unitID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/queue/"+itoa(unitID), nil)
	req.Header.Set("Authorization", "Bearer "+d.adminTok)
	rec := httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// Cancelling a RUNNING delivery stops the work, not just the row.
//
// A cancel that only marked the row would leave the model answering and the
// tokens being spent for a job somebody already stopped — and an operator
// watching a row that says cancelled while the work carries on is worse than no
// button at all.
//
// The provider here NEVER answers. That is the whole design of the test: the
// only way this run can end is by its context being cancelled, so the lane
// coming back is proof the work was interrupted rather than merely relabelled.
// An earlier version released the provider after cancelling, and passed with
// the interruption removed — it was measuring a run that finished normally.
func TestCancellingARunningDeliveryStopsTheWork(t *testing.T) {
	d := newDoor(t)

	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")

	started := make(chan struct{})
	var once sync.Once
	forever := make(chan struct{})
	// Closed only at the end, so the provider's own goroutine can unwind when
	// the test server shuts down. Nothing before the assertions touches it.
	defer close(forever)
	d.provider.answers(func(n int, c modelCall) modelReply {
		if n == 1 {
			once.Do(func() { close(started) })
			<-forever
			return says("this answer must never be reached")
		}
		return says("the workspace is free again")
	})

	go func() { d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`)) }()
	<-started

	var unitID int64
	waitFor(t, func() bool {
		return d.db.QueryRow(`SELECT id FROM work WHERE workspace_id = ? AND state = 'claimed'`,
			d.wsID).Scan(&unitID) == nil
	}, "the delivery to be running")

	if rec := d.cancelUnit(t, unitID); rec.Code != http.StatusNoContent {
		t.Fatalf("cancelling a running unit: %d\n%s", rec.Code, rec.Body.String())
	}

	// The proof that the WORK stopped, and not merely its row: the engine's
	// own latch comes back.
	//
	// Marking the row is not evidence — Kill takes the unit out of `claimed`
	// whether or not anything was interrupted, so counting claimed rows would
	// pass with the interruption removed. It did, which is why this assertion
	// is here instead. RunUnattended holds the workspace inside the engine for
	// as long as the model call lasts, and the provider is still holding that
	// call; the only way another turn can start is if the first one's context
	// was actually cancelled.
	waitFor(t, func() bool {
		return d.srv.engine.HandleUserMessage(context.Background(), d.wsID,
			"is the workspace free?", func(engine.Event) {}) == nil
	}, "the workspace to accept a turn again after the cancelled run ended")

	var state string
	if err := d.db.QueryRow(`SELECT state FROM inlet_runs WHERE workspace_id = ? ORDER BY id DESC LIMIT 1`,
		d.wsID).Scan(&state); err != nil {
		t.Fatalf("read the ledger row: %v", err)
	}
	if state != "interrupted" {
		t.Fatalf("a cancelled run's ledger row says %q", state)
	}
}

// Cancelling a unit that is not there is a 404, not a 500.
//
// It answered 500 until the work package's own ErrNotFound was mapped: every
// other store aliases catalog's sentinel and that one defines its own. The
// difference is not cosmetic — 5xx is what a client retries and what pages
// somebody, and "you asked for something that is not here" is neither. Found
// by running the new command line against a real server, which is the point of
// having one.
func TestCancellingAUnitThatIsNotThereIsNotAServerFault(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	rec := d.request(t, http.MethodDelete, "/api/v1/queue/999999", d.adminTok, "")
	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("a missing unit answered 500, which is a retry and a page: %s", rec.Body.String())
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for a unit that does not exist, got %d: %s", rec.Code, rec.Body.String())
	}
}
