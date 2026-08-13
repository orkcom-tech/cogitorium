package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/engine"
)

// A delivery into a busy workspace WAITS and then runs.
//
// This is the change the whole queue exists for. Before it, a delivery that met
// a running turn was settled `failed` with the engine's busy error and answered
// 429 — the same terminal state a genuinely broken job gets, so a burst of two
// hundred tickets was one job done and a hundred and ninety-nine losses a caller
// could only tell from real failures by string-matching an error message.
func TestADeliveryIntoABusyWorkspaceWaitsAndThenRuns(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()

	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")

	// An operator's turn holds the lane until this test lets it go.
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

	delivered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		delivered <- d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`))
	}()

	// While the operator holds the lane the delivery is WAITING — named as
	// such, not settled as a failure.
	waitFor(t, func() bool {
		var state string
		_ = d.db.QueryRow(`SELECT state FROM inlet_runs WHERE workspace_id = ? ORDER BY id DESC LIMIT 1`,
			d.wsID).Scan(&state)
		return state == "queued"
	}, "the delivery to be recorded as waiting")

	close(release)
	wg.Wait()

	rec := <-delivered
	if rec.Code != http.StatusOK {
		t.Fatalf("a delivery that waited its turn was not run: %d\n%s", rec.Code, rec.Body.String())
	}
	var state, result string
	if err := d.db.QueryRow(`SELECT state, result FROM inlet_runs WHERE workspace_id = ? ORDER BY id DESC LIMIT 1`,
		d.wsID).Scan(&state, &result); err != nil {
		t.Fatalf("read the ledger row: %v", err)
	}
	if state != "completed" || result != "triaged" {
		t.Fatalf("the ledger says state=%q result=%q", state, result)
	}
}

// Every delivery in a burst runs, exactly once, in order.
//
// The count is the point: this used to be one run and the rest destroyed.
func TestABurstOfDeliveriesAllRun(t *testing.T) {
	d := newDoor(t)

	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })

	const burst = 6
	var wg sync.WaitGroup
	codes := make([]int, burst)
	for i := range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`)).Code
		}()
	}
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("delivery %d answered %d; every one of a burst must run", i, c)
		}
	}
	var completed int
	if err := d.db.QueryRow(
		`SELECT COUNT(*) FROM inlet_runs WHERE workspace_id = ? AND state = 'completed'`,
		d.wsID).Scan(&completed); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if completed != burst {
		t.Fatalf("%d of %d deliveries completed", completed, burst)
	}
}

// A repeated idempotency key answers with the original run, and runs the job
// once.
//
// Answered rather than refused: idempotency that returns an error just moves the
// problem to whoever wrote the retry loop, who then has to tell "already done"
// from "went wrong" — the exact confusion this whole change exists to end.
func TestARepeatedIdempotencyKeyRunsTheJobOnce(t *testing.T) {
	d := newDoor(t)

	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")
	var calls int
	var mu sync.Mutex
	d.provider.answers(func(n int, c modelCall) modelReply {
		mu.Lock()
		calls++
		mu.Unlock()
		return says("triaged")
	})

	first := d.deliverWithKey(t, "triage", "ticket-1834", []byte(`{"id":7}`))
	if first.Code != http.StatusOK {
		t.Fatalf("first delivery: %d\n%s", first.Code, first.Body.String())
	}
	second := d.deliverWithKey(t, "triage", "ticket-1834", []byte(`{"id":7}`))
	if second.Code != http.StatusOK {
		t.Fatalf("the repeat was not answered from the original run: %d\n%s", second.Code, second.Body.String())
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("a repeated key produced a different answer:\nfirst:  %s\nsecond: %s",
			first.Body.String(), second.Body.String())
	}

	var runs int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM inlet_runs WHERE workspace_id = ?`, d.wsID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 1 {
		t.Fatalf("a repeated key left %d ledger rows, want 1", runs)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("the model was called %d times for one idempotency key", calls)
	}
}

// deliverWithKey is deliver with an Idempotency-Key, which is the only header
// the queue reads and the one thing a caller's retry needs to carry.
func (d *door) deliverWithKey(t *testing.T, task, key string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/i/"+d.address+"/"+task, strings.NewReader(string(body)))
	req.RemoteAddr = offBox
	req.Header.Set("Authorization", "Bearer "+d.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}
