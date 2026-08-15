package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// /invoke runs an approved gear. /run is the dry run and deliberately does not.
//
// Two routes because they are two different promises. The dry run exists so an
// operator can see what code does BEFORE trusting it, and is safe because it is
// an operator act against a throwaway container. Invoke exists so a gear can be
// called from outside a conversation — a CLI, an MCP client — and the whole
// value of that is that the gate still holds. Collapsing them into one route
// with a flag is how the safe one becomes the optional one.

func TestInvokeRefusesAGearNobodyApproved(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	// Forged directly rather than through d.forge, which approves what it
	// creates — the first version of this test used it and was therefore
	// asserting about an approved gear while claiming to be about a pending
	// one. It passed for the wrong reason until the status was checked.
	orch := d.agent(t, workspace.OrchestratorName)
	g, err := d.srv.gears.Forge(t.Context(), "unapproved", "never approved", nil,
		"python", "main.py", `{"type":"object","properties":{}}`, nil,
		[]gear.File{{Path: "main.py", Content: "print(1)\n"}}, d.wsID, orch.ID)
	if err != nil {
		t.Fatalf("forge: %v", err)
	}
	if g.Status == gear.StatusApproved {
		t.Fatalf("a freshly forged gear is %q, so this test proves nothing", g.Status)
	}

	rec := d.request(t, http.MethodPost, "/api/v1/gears/"+id(g.ID)+"/invoke", d.adminTok, `{"args":{}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a pending gear was invoked and answered %d: %s", rec.Code, rec.Body.String())
	}
	mustSay(t, "the refusal", rec.Body.String(), "not approved")
}

// And once it is approved, it runs.
func TestInvokeRunsAnApprovedGear(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	g := d.forge(t, carryGear)
	if rec := d.request(t, http.MethodPatch, "/api/v1/gears/"+id(g.ID), d.adminTok,
		`{"status":"approved"}`); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}

	rec := d.request(t, http.MethodPost, "/api/v1/gears/"+id(g.ID)+"/invoke", d.adminTok, `{"args":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("an approved gear was refused: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "not approved") {
		t.Fatalf("an approved gear reported an approval failure: %s", rec.Body.String())
	}
}

// A gear disabled after approval stops being invocable. This is the case a
// stale tool list produces: a client that listed the gear a minute ago and
// calls it now.
func TestInvokeRefusesAGearThatWasDisabledAgain(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	g := d.forge(t, carryGear)
	for _, status := range []string{"approved", "disabled"} {
		if rec := d.request(t, http.MethodPatch, "/api/v1/gears/"+id(g.ID), d.adminTok,
			`{"status":"`+status+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("set %s: %d %s", status, rec.Code, rec.Body.String())
		}
	}
	rec := d.request(t, http.MethodPost, "/api/v1/gears/"+id(g.ID)+"/invoke", d.adminTok, `{"args":{}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a disabled gear was invoked and answered %d: %s", rec.Code, rec.Body.String())
	}
}

// The dry run keeps its own behaviour: it is the one route that may run
// unapproved code, and this pins that so a future tidy-up cannot merge the two.
func TestTheDryRunStillRunsAnUnapprovedGear(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	g := d.forge(t, carryGear)

	rec := d.request(t, http.MethodPost, "/api/v1/gears/"+id(g.ID)+"/run", d.adminTok, `{"args":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("the dry run refused a pending gear, which is the one thing it exists to allow: %d %s",
			rec.Code, rec.Body.String())
	}
}
