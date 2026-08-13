package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/inlet"
)

// Editing a task that already exists.
//
// Before this the only repair for a wrong schema was delete-and-recreate: for
// the minutes in between the address answers 404 to every caller, and the task
// comes back with a new id, so everything pointed at the old one is pointed at
// nothing.

func (d *door) editTask(t *testing.T, taskID int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	return d.request(t, http.MethodPut, "/api/v1/inlet-tasks/"+id(taskID), d.adminTok, body)
}

func TestATaskCanBeEditedAndTheDoorObeysTheEdit(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	task := d.addJSONTask(t, "ingest", `{"type":"object","required":["url"]}`, "orchestrator", "fetch it")

	// The field was named wrong: it is "link", not "url".
	rec := d.editTask(t, task.ID, `{"name":"ingest","accepts":"json",
	  "schema":{"type":"object","required":["link"]},
	  "agent":"orchestrator","instruction":"fetch it"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("the edit was refused: %d %s", rec.Code, rec.Body.String())
	}

	// The door now behaves the way the edit says. An edit that is only stored
	// is a decoration.
	d.provider.answers(func(n int, c modelCall) modelReply { return says("fetched") })
	if rec := d.deliver(t, "ingest", d.key, "application/json", []byte(`{"link":"x"}`)); rec.Code != http.StatusOK {
		t.Fatalf("a body matching the NEW schema was refused: %d %s", rec.Code, rec.Body.String())
	}
	if rec := d.deliver(t, "ingest", d.key, "application/json", []byte(`{"url":"x"}`)); rec.Code == http.StatusOK {
		t.Fatal("a body matching only the OLD schema was still accepted, so the edit changed nothing that matters")
	}
}

// The id survives. That is the reason to edit rather than replace: the delivery
// ledger and every schedule hold it.
func TestEditingATaskKeepsItsIdentity(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	task := d.addJSONTask(t, "ingest", `{"type":"object"}`, "orchestrator", "fetch it")

	rec := d.editTask(t, task.ID, `{"name":"ingest","accepts":"json","schema":{"type":"object"},
	  "agent":"orchestrator","instruction":"fetch it, carefully this time"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("the edit was refused: %d %s", rec.Code, rec.Body.String())
	}
	var got inlet.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the edited task is not a task: %s", rec.Body.String())
	}
	if got.ID != task.ID {
		t.Fatalf("the edit returned task %d instead of %d, so everything pointing at it now points elsewhere", got.ID, task.ID)
	}
	if got.Instruction != "fetch it, carefully this time" {
		t.Fatalf("the instruction did not change: %q", got.Instruction)
	}
}

// Everything that refuses a bad task on the way in refuses it on the way back
// in. One validator reached from both routes: a second copy is how a task ends
// up edited into a state it could never have been created in.
func TestAnEditIsHeldToTheSameRulesAsACreation(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	task := d.addJSONTask(t, "ingest", `{"type":"object"}`, "orchestrator", "fetch it")

	for _, c := range []struct{ what, body string }{
		{"a schema keyword this server cannot enforce", `{"name":"ingest","accepts":"json",
		  "schema":{"type":"object","properties":{"u":{"pattern":"^x"}}},
		  "agent":"orchestrator","instruction":"fetch it"}`},
		{"no agent at all", `{"name":"ingest","accepts":"json","agent":"","instruction":"fetch it"}`},
		{"an agent that is not in this workspace", `{"name":"ingest","accepts":"json",
		  "agent":"nobody","instruction":"fetch it"}`},
		{"an empty instruction", `{"name":"ingest","accepts":"json","agent":"orchestrator","instruction":"   "}`},
		{"a name that is not a path segment", `{"name":"In Gest!","accepts":"json",
		  "agent":"orchestrator","instruction":"fetch it"}`},
		{"neither json nor file", `{"name":"ingest","accepts":"xml","agent":"orchestrator","instruction":"fetch it"}`},
		{"a gear nobody has forged", `{"name":"ingest","accepts":"json","agent":"orchestrator",
		  "instruction":"fetch it","expect":{"runs_gear":"no-such-gear"}}`},
	} {
		if rec := d.editTask(t, task.ID, c.body); rec.Code != http.StatusBadRequest {
			t.Fatalf("an edit with %s was accepted (%d): %s", c.what, rec.Code, rec.Body.String())
		}
	}

	// And the task it failed to edit is untouched — a refused edit that half
	// applied would be worse than no edit at all.
	after, err := d.srv.inlets.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("read the task back: %v", err)
	}
	if after.Name != "ingest" || after.AgentName != "orchestrator" || after.Instruction != "fetch it" {
		t.Fatalf("a refused edit changed the task anyway: %+v", after)
	}
}

// Renaming onto a task that already exists is refused rather than merging the
// two, and the refusal names what it collided with: the operator is looking at
// a form, not at the other task.
func TestRenamingATaskOntoAnotherIsRefused(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addJSONTask(t, "ingest", `{"type":"object"}`, "orchestrator", "fetch it")
	other := d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")

	rec := d.editTask(t, other.ID, `{"name":"ingest","accepts":"json","schema":{"type":"object"},
	  "agent":"orchestrator","instruction":"triage it"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("renaming onto an existing task answered %d, not 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ingest") {
		t.Fatalf("the refusal does not say which name collided: %s", rec.Body.String())
	}
}

// A callback URL can be set at all.
//
// It could not be until now: the column existed, the runner read it, and no
// route on this server ever wrote it — so an install could be told a run
// finished only by a test.
func TestACallbackURLCanBeConfiguredThroughTheAPI(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	task := d.addJSONTask(t, "ingest", `{"type":"object"}`, "orchestrator", "fetch it")

	rec := d.editTask(t, task.ID, `{"name":"ingest","accepts":"json","schema":{"type":"object"},
	  "agent":"orchestrator","instruction":"fetch it","callback_url":"https://listener.example/hook"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("setting a callback was refused: %d %s", rec.Code, rec.Body.String())
	}
	after, err := d.srv.inlets.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("read the task back: %v", err)
	}
	if after.CallbackURL != "https://listener.example/hook" {
		t.Fatalf("the callback URL was not stored: %q", after.CallbackURL)
	}
}

// A task in somebody else's workspace is not editable by reaching for its id.
func TestEditingATaskIsScopedLikeEverythingElse(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	task := d.addJSONTask(t, "ingest", `{"type":"object"}`, "orchestrator", "fetch it")
	body := `{"name":"ingest","accepts":"json","schema":{"type":"object"},
	  "agent":"orchestrator","instruction":"stolen"}`

	// No token at all.
	if rec := d.request(t, http.MethodPut, "/api/v1/inlet-tasks/"+id(task.ID), "", body); rec.Code == http.StatusOK {
		t.Fatalf("an unauthenticated edit succeeded: %s", rec.Body.String())
	}
	// The inlet's own key is a delivery credential, not an administrative one.
	if rec := d.request(t, http.MethodPut, "/api/v1/inlet-tasks/"+id(task.ID), d.key, body); rec.Code == http.StatusOK {
		t.Fatalf("an inlet key edited its own task: %s", rec.Body.String())
	}
	// And a task that does not exist is not editable into existence.
	if rec := d.editTask(t, task.ID+9999, body); rec.Code == http.StatusOK {
		t.Fatalf("editing a task that does not exist answered 200: %s", rec.Body.String())
	}
}
