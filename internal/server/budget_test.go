package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/config"
)

// A budget REFUSES. That is the whole point of it.
//
// The difference between a bound and a dashboard is whether anything happens
// when the line is crossed, and every other spend figure in this product is a
// number shown after the fact. The pattern copied here is the web-search
// quota's, which already stops work and writes down that it did.
//
// The check is before the call, not after: checking after would mean the call
// that crossed the line still happened and still cost money.
func TestARunStopsAtItsTokenBudget(t *testing.T) {
	d := doorAround(t, newInstall(t, doorListen, func(c *config.Config) {
		// The fixture's provider reports 18 tokens a call, so ten allows the
		// first and refuses the second. A ceiling, not a zero: a budget that
		// refuses everything would pass this test while proving nothing.
		c.BudgetRunTokens = 10
	}))
	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")

	// A turn that keeps calling tools would run forever without a ceiling;
	// with one it must stop.
	d.provider.answers(func(n int, c modelCall) modelReply {
		return asksFor(modelToolCall{ID: "t", Name: "agent_list", Args: `{}`})
	})

	rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`))
	if rec.Code == http.StatusOK {
		t.Fatalf("a run past its budget answered 200: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "budget") {
		t.Fatalf("the answer does not say a budget stopped it: %s", rec.Body.String())
	}

	// And it stopped EARLY: without the ceiling this provider drives the loop
	// to its sixteen-iteration limit.
	if n := d.provider.called(); n > 3 {
		t.Fatalf("the model was called %d times past a ten-token budget", n)
	}
}

// An install that sets no budget is unaffected, which is the default and the
// only sane one: a ceiling nobody asked for, stopping a job at three in the
// morning, is worse than no ceiling.
func TestNoBudgetMeansNoCeiling(t *testing.T) {
	d := newDoor(t)
	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")
	d.provider.answers(func(n int, c modelCall) modelReply {
		if n == 1 {
			return asksFor(modelToolCall{ID: "t", Name: "agent_list", Args: `{}`})
		}
		return says("triaged")
	})

	rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("a run on an install with no budget was stopped: %d\n%s", rec.Code, rec.Body.String())
	}
}

// Every durable row a run writes names the unit it belonged to.
//
// Before this, gear runs, granted-gear connections and token charges could only
// be tied to the delivery that caused them by their timestamp — a guess that
// happens to work today only because the engine serialises runs per workspace,
// and stops working the moment anything else is true.
func TestARunsTokenChargesNameTheirUnit(t *testing.T) {
	d := newDoor(t)
	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })

	if rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`)); rec.Code != http.StatusOK {
		t.Fatalf("delivery: %d\n%s", rec.Code, rec.Body.String())
	}

	var unitID, charged int64
	if err := d.db.QueryRow(`SELECT id FROM work WHERE workspace_id = ? ORDER BY id DESC LIMIT 1`,
		d.wsID).Scan(&unitID); err != nil {
		t.Fatalf("find the unit: %v", err)
	}
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM agent_usage WHERE work_id = ?`, unitID).Scan(&charged); err != nil {
		t.Fatalf("count charges: %v", err)
	}
	if charged == 0 {
		t.Fatal("no token charge names the unit that caused it, so a delivery still cannot be joined to what it spent")
	}

	// An operator's own turn has no unit, and NULL says so rather than a zero
	// pretending to be an id.
	var orphans int64
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM agent_usage WHERE work_id = 0`).Scan(&orphans); err != nil {
		t.Fatalf("count zero-id charges: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("%d token charges carry work_id 0, which looks like a real id to anything joining on it", orphans)
	}
}
