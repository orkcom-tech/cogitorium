package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
)

// A finished run tells whoever the task named, with the same body reading the
// run back would give.
//
// One shape deliberately: a pipeline that handles a callback and one that polls
// should not have to parse two different things, and a caller can move between
// them without touching their code.
func TestAFinishedRunTellsItsListener(t *testing.T) {
	var got atomic.Pointer[callbackBody]
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var cb callbackBody
		if err := json.Unmarshal(body, &cb); err == nil {
			got.Store(&cb)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer listener.Close()

	d := doorAround(t, newInstall(t, doorListen, func(c *config.Config) {
		// 127.0.0.1 is what httptest listens on, so this is the allowlist
		// doing its job rather than being switched off.
		c.CallbackHosts = []string{"127.0.0.1"}
	}))
	if _, err := d.srv.inlets.AddTask(context.Background(), d.inletID, inlet.Task{
		Name: "triage", Accepts: inlet.AcceptsJSON, Schema: `{"type":"object"}`,
		AgentName: "orchestrator", Instruction: "triage it",
		CallbackURL: listener.URL + "/hook",
	}); err != nil {
		t.Fatalf("add task: %v", err)
	}
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })

	rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("delivery: %d\n%s", rec.Code, rec.Body.String())
	}

	waitFor(t, func() bool { return got.Load() != nil }, "the listener to be told")
	cb := got.Load()
	if cb.State != "completed" || cb.Result != "triaged" {
		t.Fatalf("the callback carried %+v", cb)
	}
	if cb.Task != "triage" || cb.Inlet != d.address {
		t.Fatalf("the callback does not say which door and job it is about: %+v", cb)
	}
	if cb.Did.ModelCalls == 0 {
		t.Fatalf("the callback carries no record: %+v", cb.Did)
	}
}

// An install with no callback_hosts does not make outbound requests, whatever a
// task says.
//
// This is the inversion that matters. A callback URL arrives in a task, and a
// task is editable by anyone who can reach the workspace — so an empty
// allowlist meaning "everything is allowed" would turn editing a task into
// making this server call an address of somebody else's choosing.
func TestCallbacksAreOffUntilAnOperatorNamesTheHosts(t *testing.T) {
	var called atomic.Int64
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer listener.Close()

	// No CallbackHosts at all: the default install.
	d := newDoor(t)
	if _, err := d.srv.inlets.AddTask(context.Background(), d.inletID, inlet.Task{
		Name: "triage", Accepts: inlet.AcceptsJSON, Schema: `{"type":"object"}`,
		AgentName: "orchestrator", Instruction: "triage it",
		CallbackURL: listener.URL + "/hook",
	}); err != nil {
		t.Fatalf("add task: %v", err)
	}
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })

	if rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`)); rec.Code != http.StatusOK {
		t.Fatalf("delivery: %d\n%s", rec.Code, rec.Body.String())
	}
	// The run itself is unaffected — a callback that cannot be sent must not
	// fail the work that was done.
	waitFor(t, func() bool {
		var state string
		_ = d.db.QueryRow(`SELECT state FROM inlet_runs WHERE workspace_id = ? ORDER BY id DESC LIMIT 1`,
			d.wsID).Scan(&state)
		return state == "completed"
	}, "the run to finish")

	if n := called.Load(); n != 0 {
		t.Fatalf("an install with no callback_hosts called out %d time(s)", n)
	}
}

// A host that is not on the list is refused even when others are.
func TestOnlyNamedHostsAreCalled(t *testing.T) {
	var called atomic.Int64
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer listener.Close()

	d := doorAround(t, newInstall(t, doorListen, func(c *config.Config) {
		c.CallbackHosts = []string{"hooks.example.com"}
	}))
	if err := d.srv.callbackAllowed(listener.URL + "/hook"); err == nil {
		t.Fatal("a host that is not on the list was allowed")
	}
	if err := d.srv.callbackAllowed("https://hooks.example.com/x"); err != nil {
		t.Fatalf("a host that IS on the list was refused: %v", err)
	}
	// And a scheme this server will not speak is refused before the host is
	// even considered.
	if err := d.srv.callbackAllowed("file:///etc/passwd"); err == nil {
		t.Fatal("a file:// callback was allowed")
	}
}
