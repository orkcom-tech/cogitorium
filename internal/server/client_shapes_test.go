package server

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/client"
)

// Every field the command line's client models must exist in the answer.
//
// This exists because of a bug that shipped nothing but empty strings. The
// first draft of client.Run called its fields "task" and "address"; the server
// calls them task_name and inlet_address. Nothing failed — encoding/json
// ignores a key it was not asked for and leaves the field at its zero value —
// so `cogitorium run 3` printed "run 3  completed  /  agent " and looked like a
// formatting problem rather than a client reading the wrong document.
//
// A test that asserted "the fields are non-empty" would be weaker: half of
// these are legitimately empty (a run that did not fail has no error). What
// makes a client wrong is asking for a key the server does not send, so that is
// what this checks — presence, not value — against responses from the real
// handlers rather than from a fixture written to match.
func TestTheCommandLineAsksForKeysTheServerActuallySends(t *testing.T) {
	d := newDoor(t)

	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })
	accepted := d.decode(t, d.deliverAsync(t, "triage", []byte(`{"id":7}`)))
	waitFor(t, func() bool { return d.runStatus(t, accepted.Run).State == "completed" }, "the run to finish")

	for _, c := range []struct {
		what string
		path string
		into any
	}{
		{"workspaces", "/api/v1/workspaces", []client.Workspace{}},
		{"gears", "/api/v1/gears", []client.Gear{}},
		{"receivers", "/api/v1/workspaces/" + id(d.wsID) + "/inlets", []client.Inlet{}},
		{"the queue", "/api/v1/workspaces/" + id(d.wsID) + "/queue", client.QueueView{}},
		{"a run", "/api/v1/inlet-runs/" + id(accepted.Run), client.Run{}},
	} {
		t.Run(c.what, func(t *testing.T) {
			rec := d.request(t, http.MethodGet, c.path, d.adminTok, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("%s answered %d: %s", c.path, rec.Code, rec.Body.String())
			}
			missing := keysNotSent(reflect.TypeOf(c.into), rec.Body.Bytes(), reflect.TypeOf(c.into).Name())
			if len(missing) > 0 {
				t.Fatalf("the client reads keys %s does not send: %s\n\nserver said: %s",
					c.path, strings.Join(missing, ", "), rec.Body.String())
			}
		})
	}
}

// keysNotSent walks a client type against a real response and reports the json
// tags with nothing behind them.
//
// An empty list is skipped rather than passed: a response that happens to carry
// no rows says nothing about the shape of a row, and treating that as agreement
// is how a check like this quietly stops checking. The cases above all produce
// at least one, and a run of them going empty would show as the assertion never
// having anything to assert about — which is why they are separate subtests.
func keysNotSent(t reflect.Type, raw []byte, where string) []string {
	switch t.Kind() {
	case reflect.Pointer:
		return keysNotSent(t.Elem(), raw, where)
	case reflect.Slice:
		if t == reflect.TypeOf(json.RawMessage{}) {
			return nil
		}
		var rows []json.RawMessage
		if json.Unmarshal(raw, &rows) != nil || len(rows) == 0 {
			return nil
		}
		return keysNotSent(t.Elem(), rows[0], typeName(t.Elem(), where))
	case reflect.Struct:
		var got map[string]json.RawMessage
		if json.Unmarshal(raw, &got) != nil {
			return nil
		}
		var missing []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			sent, ok := got[name]
			if !ok {
				missing = append(missing, typeName(t, where)+"."+f.Name+" wants "+name)
				continue
			}
			missing = append(missing, keysNotSent(f.Type, sent, typeName(t, where)+"."+f.Name)...)
		}
		return missing
	default:
		return nil
	}
}

// typeName prefers the declared name and falls back to where the field was
// reached from, because the nested ones are anonymous structs and "" is not a
// place a reader can go and look.
func typeName(t reflect.Type, where string) string {
	if n := t.Name(); n != "" {
		return n
	}
	return where
}
