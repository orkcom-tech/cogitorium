package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/schedule"
)

// A schedule that comes due does the job, by the same path a delivery takes.
//
// The same path deliberately: a scheduled job and a delivered one are the same
// work with a different trigger, and two ways into the engine would be two
// places for the record, the expectation and the callback to diverge. So the
// assertion is that a LEDGER ROW appears — the same row an HTTP caller would
// have produced — and not merely that a goroutine ran.
func TestADueScheduleRunsTheJob(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()

	task := d.addJSONTask(t, "nightly", `{"type":"object"}`, "orchestrator", "do the nightly thing")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("done") })

	sc, err := d.srv.schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: d.wsID, TaskID: task.ID, Name: "nightly", Spec: "every 1m",
		Payload: `{"id":7}`,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	// Make it due. A schedule stores when it next fires, so bringing that
	// forward is how a test reaches tomorrow without waiting for it.
	if _, err := d.db.Exec(`UPDATE schedules SET next_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), sc.ID); err != nil {
		t.Fatalf("bring the schedule forward: %v", err)
	}
	d.srv.tick(ctx)

	waitFor(t, func() bool {
		var n int
		_ = d.db.QueryRow(`SELECT COUNT(*) FROM inlet_runs WHERE workspace_id = ? AND state = 'completed'`,
			d.wsID).Scan(&n)
		return n == 1
	}, "the scheduled job to run")

	after, err := d.srv.schedules.Get(ctx, sc.ID)
	if err != nil {
		t.Fatalf("read the schedule back: %v", err)
	}
	if after.Fires != 1 || after.LastOutcome != schedule.OutcomeFired {
		t.Fatalf("the schedule does not record its firing: %+v", after)
	}
	if !after.NextAt.After(time.Now().UTC()) {
		t.Fatalf("the schedule did not move its clock forward: next_at=%s", after.NextAt)
	}
	if after.LastWorkID == nil {
		t.Fatal("the schedule does not name the unit it produced, so 'did last night run' needs a search")
	}
}

// A firing whose previous run has not finished is SKIPPED, and says so.
//
// A job slower than its own interval never catches up, and queueing every
// missed tick turns that into a backlog that outlives the reason for it. The
// skip is recorded as a skip rather than as a failure, because they want
// different reactions.
func TestASlowJobSkipsItsNextFiringRatherThanPilingUp(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()

	task := d.addJSONTask(t, "nightly", `{"type":"object"}`, "orchestrator", "do the nightly thing")

	// The first run never finishes while this test looks at it.
	started := make(chan struct{})
	forever := make(chan struct{})
	defer close(forever)
	var once sync.Once
	d.provider.answers(func(n int, c modelCall) modelReply {
		once.Do(func() { close(started) })
		<-forever
		return says("never")
	})

	sc, err := d.srv.schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: d.wsID, TaskID: task.ID, Name: "nightly", Spec: "every 1m", Payload: `{}`,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	due := func() {
		if _, err := d.db.Exec(`UPDATE schedules SET next_at = ? WHERE id = ?`,
			time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), sc.ID); err != nil {
			t.Fatalf("bring the schedule forward: %v", err)
		}
	}

	due()
	d.srv.tick(ctx)
	<-started

	due()
	d.srv.tick(ctx)

	after, err := d.srv.schedules.Get(ctx, sc.ID)
	if err != nil {
		t.Fatalf("read the schedule back: %v", err)
	}
	if after.Skips != 1 || after.LastOutcome != schedule.OutcomeSkipped {
		t.Fatalf("a firing over an unfinished run was not recorded as a skip: %+v", after)
	}
	// And exactly one job exists, not two.
	var runs int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM inlet_runs WHERE workspace_id = ?`, d.wsID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 1 {
		t.Fatalf("%d runs exist; the skipped firing created work anyway", runs)
	}
}

// A schedule is checked when it is written, not at 02:00.
//
// This is the only moment the person who typed it is still looking at it. A
// spec that first fails in the middle of the night is a job nobody notices has
// stopped.
func TestASchedulesMistakesAreCaughtWhenItIsSaved(t *testing.T) {
	d := newDoor(t)

	fileTask := d.addFileTask(t, "ingest", "text/plain", "orchestrator", "read it")
	jsonTask := d.addJSONTask(t, "strict", `{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}`,
		"orchestrator", "do it")

	for _, c := range []struct {
		name, body, says string
	}{
		{
			name: "a spec that is not a spec",
			body: `{"task_id":` + itoa(jsonTask.ID) + `,"name":"a","spec":"nightly-ish","payload":{"id":1}}`,
			says: "five fields",
		},
		{
			name: "faster than the floor",
			body: `{"task_id":` + itoa(jsonTask.ID) + `,"name":"a","spec":"every 10s","payload":{"id":1}}`,
			says: "is the floor",
		},
		{
			name: "a timezone this install does not know",
			body: `{"task_id":` + itoa(jsonTask.ID) + `,"name":"a","spec":"every 1h","tz":"Mars/Olympus","payload":{"id":1}}`,
			says: "not a timezone",
		},
		{
			name: "a payload the task would refuse every night",
			body: `{"task_id":` + itoa(jsonTask.ID) + `,"name":"a","spec":"every 1h","payload":{"nope":1}}`,
			says: "every firing would be refused",
		},
		{
			name: "a file task, which a clock has no bytes for",
			body: `{"task_id":` + itoa(fileTask.ID) + `,"name":"a","spec":"every 1h"}`,
			says: "only a JSON task can be scheduled",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := d.api(t, http.MethodPost, "/api/v1/workspaces/"+itoa(d.wsID)+"/schedules", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("answered %d, want 400\n%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.says) {
				t.Fatalf("the refusal does not say what is wrong: %s", rec.Body.String())
			}
		})
	}
}

// A disabled schedule does not fire, and switching it back on re-bases its
// clock rather than firing once for every tick it was off for.
func TestADisabledScheduleDoesNotFire(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()

	task := d.addJSONTask(t, "nightly", `{"type":"object"}`, "orchestrator", "do it")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("done") })
	sc, err := d.srv.schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: d.wsID, TaskID: task.ID, Name: "nightly", Spec: "every 1m", Payload: `{}`,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.srv.schedules.SetEnabled(ctx, sc.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := d.db.Exec(`UPDATE schedules SET next_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), sc.ID); err != nil {
		t.Fatalf("bring forward: %v", err)
	}

	d.srv.tick(ctx)
	var runs int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM inlet_runs WHERE workspace_id = ?`, d.wsID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 0 {
		t.Fatalf("a disabled schedule fired %d time(s)", runs)
	}

	back, err := d.srv.schedules.SetEnabled(ctx, sc.ID, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !back.NextAt.After(time.Now().UTC()) {
		t.Fatalf("re-enabling left the clock in the past, so it fires for every tick it was off: %s", back.NextAt)
	}
}

// api is an authenticated API call, which every schedule route needs: this
// fixture listens on a non-loopback address on purpose, so there is no implicit
// admin to fall back on.
func (d *door) api(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+d.adminTok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}
