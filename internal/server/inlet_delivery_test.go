package server

// The delivery path: what a caller outside this install can and cannot make a
// workspace do by posting to a door.
//
// Every test here drives the real handler chain (logging -> authenticate ->
// mux) against a real SQLite database and a provider on a real socket, so a
// refusal is only ever the rule under test.

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// ticketSchema is what the door tells its callers it accepts.
const ticketSchema = `{
  "type": "object",
  "properties": {
    "id":    {"type": "integer", "minimum": 1},
    "title": {"type": "string", "minLength": 3}
  },
  "required": ["id", "title"],
  "additionalProperties": false
}`

// --- 1. The refusal comes before the work, not after. ---------------------

// TestAPayloadThatFailsTheSchemaNeverReachesAModel is the promise a JSON task
// makes: a body that does not match is refused for nothing. Refused AFTER the
// model call would still answer 400, so the only evidence that matters is that
// the provider was never contacted and that the run never started.
func TestAPayloadThatFailsTheSchemaNeverReachesAModel(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addJSONTask(t, "triage", ticketSchema, workspace.OrchestratorName, "Triage this ticket.")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })

	refused := []struct{ name, body, wants string }{
		{"a required field was not sent", `{"title":"disk full"}`, "id is required"},
		{"the wrong type", `{"id":"7","title":"disk full"}`, "expected an integer"},
		{"below the minimum", `{"id":0,"title":"disk full"}`, "must be at least 1"},
		{"too short", `{"id":7,"title":"no"}`, "needs at least 3 characters"},
		{"a field the task does not accept", `{"id":7,"title":"ok","exec":"rm -rf /"}`, "not a field this task accepts"},
		{"not JSON at all", `id=7`, "not valid JSON"},
		{"nothing at all", ``, "the body is empty"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			rec := d.deliver(t, "triage", d.key, "application/json", []byte(c.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400\nbody: %s", rec.Code, rec.Body.String())
			}
			got := d.decode(t, rec)
			if got.State != inlet.StateRefusedSchema {
				t.Fatalf("recorded as %q, want %q\nbody: %s", got.State, inlet.StateRefusedSchema, rec.Body.String())
			}
			// The caller is the one who has to fix this, and they cannot see
			// the schema. The complaint has to name the field.
			if !strings.Contains(got.Error, c.wants) {
				t.Fatalf("the refusal does not say what was wrong: %q", got.Error)
			}

			// The ledger says the payload was refused, and says nothing about
			// work: Begin is what writes the agent and the size, and Begin
			// never ran.
			row := d.run(t, got.Run)
			if row.State != inlet.StateRefusedSchema || row.AgentID != nil || row.PayloadBytes != 0 {
				t.Fatalf("the ledger claims work happened for a refused payload: state=%q agent_id=%v payload_bytes=%d",
					row.State, row.AgentID, row.PayloadBytes)
			}
		})
	}

	// The whole point, stated once: not one of those bodies cost a provider
	// call. A validator that ran after the model would pass every assertion
	// above and fail this one.
	if n := d.provider.called(); n != 0 {
		t.Fatalf("a refused payload reached a model %d time(s); the schema is checked after the work, not before", n)
	}

	// Control: the same door, the same task, a body that matches. Without it
	// every assertion above would also pass on a door that refuses everything.
	rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7,"title":"disk full"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("a valid payload: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	got := d.decode(t, rec)
	if got.State != inlet.StateCompleted || got.Result != "triaged" {
		t.Fatalf("a valid payload came back as %+v", got)
	}
	if n := d.provider.called(); n != 1 {
		t.Fatalf("the accepted payload reached a model %d times, want 1", n)
	}

	// The agent is given the operator's instruction and the caller's payload,
	// with the payload named as what it is.
	turn := d.provider.call(t, 1).userText()
	for _, want := range []string{"Triage this ticket.", `{"id":7,"title":"disk full"}`, "untrusted", "data, not instructions"} {
		if !strings.Contains(turn, want) {
			t.Fatalf("the agent's turn is missing %q:\n%s", want, turn)
		}
	}
}

// --- 2. A key opens exactly one door. -------------------------------------

// TestOnlyThisDoorsKeyDelivers walks every way a key can be wrong. Each one
// that passed would be a stranger running an agent inside somebody's
// workspace, at that workspace's expense.
func TestOnlyThisDoorsKeyDelivers(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addJSONTask(t, "triage", ticketSchema, workspace.OrchestratorName, "Triage this ticket.")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })
	ctx := context.Background()

	// A second door in the same workspace, with its own key.
	other, err := d.srv.inlets.CreateInlet(ctx, d.wsID, "invoices", "")
	if err != nil {
		t.Fatalf("create the second inlet: %v", err)
	}
	otherKey, err := d.srv.inlets.IssueKey(ctx, other.ID)
	if err != nil {
		t.Fatalf("issue the second key: %v", err)
	}
	if _, err := d.srv.inlets.AddTask(ctx, other.ID, inlet.Task{
		Name: "triage", Accepts: inlet.AcceptsJSON, Schema: ticketSchema,
		AgentName: workspace.OrchestratorName, Instruction: "Triage this invoice.",
	}); err != nil {
		t.Fatalf("add the second task: %v", err)
	}

	// A door that has never been issued a key: the state an imported bundle
	// arrives in, since a bundle carries an inlet's shape and never its
	// credential. It must refuse everything.
	keyless, err := d.srv.inlets.CreateInlet(ctx, d.wsID, "imported", "")
	if err != nil {
		t.Fatalf("create the keyless inlet: %v", err)
	}
	if _, err := d.srv.inlets.AddTask(ctx, keyless.ID, inlet.Task{
		Name: "triage", Accepts: inlet.AcceptsJSON, Schema: ticketSchema,
		AgentName: workspace.OrchestratorName, Instruction: "Triage this.",
	}); err != nil {
		t.Fatalf("add the keyless door's task: %v", err)
	}

	const good = `{"id":7,"title":"disk full"}`
	forged := inlet.KeyPrefix + "-tickets-" + strings.Repeat("0", 64)

	for _, c := range []struct{ name, address, key string }{
		{"no credential at all", d.address, ""},
		{"a forged key naming the right door", d.address, forged},
		{"a user token instead of an inlet key", d.address, d.adminTok},
		{"another door's key", d.address, otherKey},
		{"this door's key at the other door", "invoices", d.key},
		{"the key with a character changed", d.address, d.key[:len(d.key)-1] + "0"},
		{"a truncated key", d.address, d.key[:len(d.key)-4]},
		{"any key at a door that has none", "imported", d.key},
		{"an empty key at a door that has none", "imported", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := d.deliverTo(t, c.address, "triage", c.key, "application/json", []byte(good))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401\nbody: %s", rec.Code, rec.Body.String())
			}
			// The answer says how to present a key, and does not confirm
			// anything about the one that was presented.
			if !strings.Contains(rec.Body.String(), "this inlet's key is required") {
				t.Fatalf("the refusal is not about the inlet key: %s", rec.Body.String())
			}
		})
	}

	// Nothing above may have cost a model call, and nothing above may have
	// left a ledger row: the row is written after the key opens the door, so a
	// ledger filling with rows for keys that never worked would be a hole of
	// its own.
	if n := d.provider.called(); n != 0 {
		t.Fatalf("a delivery with a bad key reached a model %d time(s)", n)
	}
	if rows := d.runs(t); len(rows) != 0 {
		t.Fatalf("a delivery with a bad key was recorded as a run: %+v", rows)
	}

	// Control: the right key at the right door works.
	if rec := d.deliver(t, "triage", d.key, "application/json", []byte(good)); rec.Code != http.StatusOK {
		t.Fatalf("the right key at the right door: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	// Rotation retires the previous key on the spot. This is the only answer
	// to a leaked key, so a key that survived it could never be closed.
	rotated, err := d.srv.inlets.IssueKey(ctx, d.inletID)
	if err != nil {
		t.Fatalf("rotate the key: %v", err)
	}
	if rec := d.deliver(t, "triage", d.key, "application/json", []byte(good)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the retired key still delivers: got %d, want 401\nbody: %s", rec.Code, rec.Body.String())
	}
	if rec := d.deliver(t, "triage", rotated, "application/json", []byte(good)); rec.Code != http.StatusOK {
		t.Fatalf("the rotated key does not deliver: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	// An unknown door and an unknown task are both 404, and neither confirms
	// anything to somebody probing with a key they hold.
	if rec := d.deliverTo(t, "no-such-door", "triage", rotated, "application/json", []byte(good)); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown door: got %d, want 404", rec.Code)
	}
	if rec := d.deliver(t, "no-such-task", rotated, "application/json", []byte(good)); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown task: got %d, want 404", rec.Code)
	}
}

// --- 4. A delivery is not a conversation. ---------------------------------

// TestADeliveryAppendsNothingToTheWorkspaceTimeline guards the reason inlets
// do not go through the chat path. buildHistory replays the timeline into every
// orchestrator turn, so a pipeline that appended to it would carry request 199
// into request 200 — at the caller's expense — until the context window ended
// it.
func TestADeliveryAppendsNothingToTheWorkspaceTimeline(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addJSONTask(t, "triage", ticketSchema, workspace.OrchestratorName, "Triage this ticket.")

	// A delivery with a tool call in the middle, so the whole loop runs and
	// not just the shortest path through it.
	d.provider.answers(func(n int, c modelCall) modelReply {
		if n == 1 {
			return asksFor(modelToolCall{ID: "call_1", Name: "agent_list", Args: `{}`})
		}
		return says("triaged")
	})

	before := d.timelineLength(t)
	for i := 0; i < 3; i++ {
		rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7,"title":"disk full"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d: got %d, want 200\nbody: %s", i, rec.Code, rec.Body.String())
		}
		d.provider.reset()
	}
	after := d.timelineLength(t)
	if after != before {
		t.Fatalf("three deliveries appended %d message(s) to the workspace timeline; they must append none", after-before)
	}

	// Control: the operator's own turn does append, so the count above is a
	// real measurement and not a query that never sees anything.
	d.provider.answers(func(n int, c modelCall) modelReply { return says("hello back") })
	if err := d.srv.engine.HandleUserMessage(context.Background(), d.wsID, "hello", func(engine.Event) {}); err != nil {
		t.Fatalf("operator turn: %v", err)
	}
	if d.timelineLength(t) <= after {
		t.Fatalf("the operator's own message did not reach the timeline either, so this test measures nothing")
	}
}

// TestADeliveryThatDelegatesAlsoAppendsNothing is the same rule for the case
// the agent does not handle the payload alone.
//
// It is here because the rule was once broken exactly here, and the defect was
// in internal/engine rather than in this package. delegate() appended a
// "delegation" message to the timeline unconditionally, and RunUnattended
// reaches it through runAgent like any other turn — so a delivery whose agent
// delegated wrote a row after all, against what RunUnattended's own comment
// promises ("the timeline is deliberately untouched"). It is fixed: delegate()
// now appends only when the turn is not unattended, and says why where it does
// it ("the delegation row belongs to the operator's conversation").
//
// What it cost, and therefore what this test holds shut: buildHistory replays
// the LAST 500 timeline rows into every orchestrator turn (ListMessages orders
// by id DESC and reverses). Delegation rows are not themselves replayed as
// turns, so this was never the runaway cost the design was avoiding — but they
// did occupy that window. A workspace behind a busy door pushed the operator's
// own conversation out of its model's context, silently, at the rate its
// callers delivered, and the operator saw delegation entries in their timeline
// with no message before them saying where the work had come from.
func TestADeliveryThatDelegatesAlsoAppendsNothing(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	ctx := context.Background()
	d.addJSONTask(t, "triage", ticketSchema, workspace.OrchestratorName, "Triage this ticket.")

	worker, err := d.spaces.CreateAgent(ctx, d.wsID, "worker", "does the work", d.modelID)
	if err != nil {
		t.Fatalf("create the worker: %v", err)
	}
	orch := d.agent(t, workspace.OrchestratorName)
	if _, err := d.spaces.CreateWire(ctx, d.wsID, orch.ID, worker.ID, "delegates"); err != nil {
		t.Fatalf("wire them: %v", err)
	}

	d.provider.answers(func(n int, c modelCall) modelReply {
		switch n {
		case 1:
			return asksFor(modelToolCall{ID: "c1", Name: "delegate", Args: `{"agent":"worker","task":"look at this ticket"}`})
		case 2:
			return says("the worker looked at it")
		default:
			return says("triaged")
		}
	})

	before := d.timelineLength(t)
	rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7,"title":"disk full"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("the delivery: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if after := d.timelineLength(t); after != before {
		t.Fatalf("a delivery whose agent delegated appended %d message(s) to the workspace timeline; "+
			"delegate() appends one per delegation and RunUnattended does not stop it", after-before)
	}
}

// --- 7. A file lands on disk; the agent is given its path. ----------------

// TestAFilePayloadLandsInTheWorkspaceAndTheAgentGetsThePath is the point of a
// file task: the bytes go to the workspace directory and the agent is handed a
// path, because a gear that uploads to a bucket takes a path and no agent
// should be handed a megabyte of base64 in a prompt.
func TestAFilePayloadLandsInTheWorkspaceAndTheAgentGetsThePath(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addFileTask(t, "attach", "image/png", workspace.OrchestratorName, "File this screenshot.")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("filed") })

	pixels := append([]byte("\x89PNG\r\n\x1a\n"), []byte("SECRET-PIXELS-abcdef")...)
	rec := d.deliver(t, "attach", d.key, "image/png", pixels)
	if rec.Code != http.StatusOK {
		t.Fatalf("a file delivery: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	got := d.decode(t, rec)

	row := d.run(t, got.Run)
	if row.PayloadPath == "" {
		t.Fatal("the ledger does not say where the delivered file went")
	}
	if row.PayloadBytes != int64(len(pixels)) {
		t.Fatalf("the ledger recorded %d bytes for a %d byte file", row.PayloadBytes, len(pixels))
	}

	// The file is really there, byte for byte, inside this workspace's own
	// directory and nowhere else.
	root := filepath.Join(d.dir, "workspaces", id(d.wsID))
	full := filepath.Join(root, filepath.FromSlash(row.PayloadPath))
	landed, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("the delivered file is not where the ledger says it is (%s): %v", full, err)
	}
	if !bytes.Equal(landed, pixels) {
		t.Fatalf("the file on disk is not what was delivered: %q", landed)
	}

	// The agent is given ONE path, the workspace-relative one, and not the
	// bytes.
	//
	// It used to be given the absolute path on the machine as well. A live run
	// showed the cost: the model chose the absolute form — it looks more like a
	// real path — put it in an ordinary gear argument, and passed no _files. So
	// nothing was staged, no out/ was collected, and the gear (unsandboxed, with
	// the server's file access) opened the host file anyway, wrote its results
	// where nobody collects them, and printed success. The agent then reported
	// that success truthfully. Handing over two forms of the same thing is an
	// invitation to pick the wrong one.
	turn := d.provider.call(t, 1).userText()
	for _, want := range []string{"File this screenshot.", row.PayloadPath, "image/png"} {
		if !strings.Contains(turn, want) {
			t.Fatalf("the agent's turn is missing %q:\n%s", want, turn)
		}
	}
	if strings.Contains(turn, full) {
		t.Errorf("the agent's turn carries the absolute path %q, which no gear can use and which puts this machine's layout in the model's context:\n%s", full, turn)
	}
	if strings.Contains(turn, "SECRET-PIXELS") {
		t.Fatalf("the file's bytes were put in the agent's prompt instead of its path:\n%s", turn)
	}

	// The run number is in the filename, so a second caller cannot replace the
	// bytes under an agent that has already been handed the path.
	second := d.decode(t, d.deliver(t, "attach", d.key, "image/png", []byte("\x89PNG\r\n\x1a\nsecond")))
	if p := d.run(t, second.Run).PayloadPath; p == row.PayloadPath {
		t.Fatalf("two deliveries wrote the same path %q", p)
	}
}

// TestAHostileFilenameCannotDecideWhereTheFileGoes: the name comes from
// outside and it decides a path on this machine. What survives has to be a
// label, never a direction.
func TestAHostileFilenameCannotDecideWhereTheFileGoes(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addFileTask(t, "attach", "image/png", workspace.OrchestratorName, "File this screenshot.")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("filed") })

	// Go's own multipart reader applies filepath.Base to a part's filename
	// (RFC 7578 §4.2), so the two obvious traversals below are already blunted
	// before this server sees them. They stay because that is a fact about a
	// dependency, not a rule this code keeps: the Windows-separator case and
	// the names that are nothing but dots are what actually reach the cleaner,
	// and TestSafeFileNameKeepsALabelAndNeverADirection covers the rest
	// directly, where no library is standing in front of it.
	root := filepath.Join(d.dir, "workspaces", id(d.wsID))
	hostile := []struct{ name, filename string }{
		{"a relative traversal", "../../../../etc/passwd.png"},
		{"an absolute path", "/etc/cron.d/evil.png"},
		{"a windows path", `..\..\windows\system32\evil.png`},
		{"a traversal with nothing else in it", "../.."},
		{"a name that is nothing once it is cleaned", "...."},
		{"a bare separator", "/"},
		{"a name that is only punctuation", "-._"},
	}
	for _, c := range hostile {
		t.Run(c.name, func(t *testing.T) {
			body, contentType := multipartFile(t, c.filename, "image/png", []byte("\x89PNG\r\n\x1a\nx"))
			rec := d.deliver(t, "attach", d.key, contentType, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d, want 200 — the name is neutered, not a reason to refuse\nbody: %s", rec.Code, rec.Body.String())
			}
			row := d.run(t, d.decode(t, rec).Run)

			// The recorded path is relative, inside the door's own folder, and
			// has no way left in it to name a parent.
			if filepath.IsAbs(row.PayloadPath) || strings.Contains(row.PayloadPath, "..") {
				t.Fatalf("the delivered path can leave the workspace: %q", row.PayloadPath)
			}
			if !strings.HasPrefix(row.PayloadPath, "inlets/tickets/") {
				t.Fatalf("the file did not land in this door's folder: %q", row.PayloadPath)
			}
			full := filepath.Join(root, filepath.FromSlash(row.PayloadPath))
			if _, err := os.Stat(full); err != nil {
				t.Fatalf("nothing at the path the ledger reports (%s): %v", full, err)
			}
		})
	}

	// Nothing was written anywhere but inside this workspace. Walking the whole
	// data directory is what catches an escape that a per-case assertion would
	// miss, because a file that left the root is not at the path we would look
	// at.
	var strays []string
	err := filepath.WalkDir(d.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".png") && !strings.HasPrefix(path, root+string(filepath.Separator)) {
			strays = append(strays, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the data directory: %v", err)
	}
	if len(strays) > 0 {
		t.Fatalf("a delivered file was written outside its workspace: %v", strays)
	}
}

// TestSafeFileNameKeepsALabelAndNeverADirection is the cleaner on its own, so
// the property is stated where it is implemented: whatever a caller names their
// upload, what comes out is one path segment.
func TestSafeFileNameKeepsALabelAndNeverADirection(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"../../../../etc/passwd.png",
		"/etc/cron.d/evil.png",
		`..\..\windows\system32\evil.png`,
		"..",
		"../..",
		"....",
		".",
		"/",
		"//",
		"-._",
		"ev\x00il.png",
		"\x00",
		"photo (1).jpg",
		"~/.ssh/authorized_keys",
		"$(rm -rf /).png",
		"a;b|c&d.png",
		strings.Repeat("x", 400) + ".png",
		"",
	} {
		got := safeFileName(name, "image/png")
		if got == "" {
			t.Fatalf("%q cleaned to an empty filename, which would make the run number the whole name", name)
		}
		if strings.ContainsAny(got, `/\`) {
			t.Fatalf("%q cleaned to %q, which still names a directory", name, got)
		}
		if strings.Contains(got, "\x00") {
			t.Fatalf("%q cleaned to %q, which still carries a NUL", name, got)
		}
		if got == "." || got == ".." || strings.HasPrefix(got, ".") {
			t.Fatalf("%q cleaned to %q, which names a directory or hides the file", name, got)
		}
		if len(got) > 80 {
			t.Fatalf("%q cleaned to %d characters; a filename this long is a limit somebody's filesystem enforces instead", name, len(got))
		}
	}

	// A raw body names nothing, so the media type does — and an unknown type
	// gets no extension rather than an invented one.
	if got := safeFileName("", "image/png"); !strings.HasSuffix(got, ".png") {
		t.Fatalf("an unnamed PNG upload became %q", got)
	}
	if got := safeFileName("", "application/x-nothing-in-particular"); strings.Contains(got, ".") {
		t.Fatalf("an unnamed upload of an unknown type was given the extension %q", got)
	}
}

// TestAFileTaskRefusesWhatItDoesNotAccept: the declared content type is the
// task's own statement about what may arrive, and it is checked before a byte
// is written or a model is called.
func TestAFileTaskRefusesWhatItDoesNotAccept(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addFileTask(t, "attach", "image/png", workspace.OrchestratorName, "File this screenshot.")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("filed") })

	for _, c := range []struct{ name, contentType string }{
		{"a different type entirely", "text/plain"},
		{"a sibling image type", "image/jpeg"},
		{"a script wearing no type at all", ""},
		{"an executable", "application/x-mach-binary"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := d.deliver(t, "attach", d.key, c.contentType, []byte("#!/bin/sh\nrm -rf /\n"))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400\nbody: %s", rec.Code, rec.Body.String())
			}
			got := d.decode(t, rec)
			if got.State != inlet.StateRefusedSchema {
				t.Fatalf("recorded as %q, want %q", got.State, inlet.StateRefusedSchema)
			}
			if !strings.Contains(got.Error, "image/png") {
				t.Fatalf("the refusal does not say what the task accepts: %q", got.Error)
			}
		})
	}
	if n := d.provider.called(); n != 0 {
		t.Fatalf("a body of the wrong type reached a model %d time(s)", n)
	}

	// And nothing was written: a refused body must not leave a file behind for
	// somebody to find later.
	root := filepath.Join(d.dir, "workspaces", id(d.wsID))
	if entries, err := os.ReadDir(filepath.Join(root, "inlets", d.address)); err == nil && len(entries) > 0 {
		t.Fatalf("a refused body was written into the workspace anyway: %v", entries)
	}
}

// multipartFile builds the body an HTML form or curl -F would send, with the
// part's own Content-Type set explicitly — CreateFormFile would label
// everything application/octet-stream, and then the content-type check under
// test would be answering about the wrong thing.
func multipartFile(t *testing.T, filename, contentType string, body []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	header.Set("Content-Type", contentType)
	part, err := w.CreatePart(header)
	if err != nil {
		t.Fatalf("build multipart part: %v", err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatalf("write multipart part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// --- 8. The ledger: one row per delivery, settled once. -------------------

// TestTheLedgerRecordsEachDeliveryOnceWithTheRightEnding covers the three
// endings an operator has to be able to tell apart: the agent answered, the
// caller sent the wrong thing, and this workspace is not finished. Reading the
// wrong one sends somebody to debug the wrong system.
func TestTheLedgerRecordsEachDeliveryOnceWithTheRightEnding(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addJSONTask(t, "triage", ticketSchema, workspace.OrchestratorName, "Triage this ticket.")
	ctx := context.Background()

	// The row must exist BEFORE the work, so the provider — which is the work —
	// looks the run up while the engine is waiting on it. A row written after
	// the answer would pass every assertion below and fail this one.
	d.provider.answers(func(n int, c modelCall) modelReply {
		var state string
		var agentID *int64
		err := d.db.QueryRow(
			`SELECT state, agent_id FROM inlet_runs ORDER BY id DESC LIMIT 1`).Scan(&state, &agentID)
		switch {
		case err != nil:
			d.provider.note("no ledger row existed when the model was called: %v", err)
		case state != inlet.StateRunning:
			d.provider.note("the ledger said %q while the model was being called, want %q", state, inlet.StateRunning)
		case agentID == nil:
			d.provider.note("the ledger did not say which agent was doing the work while it was being done")
		}
		return says("triaged")
	})

	// 1. The agent answered.
	rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7,"title":"disk full"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("a delivery that should complete: got %d\nbody: %s", rec.Code, rec.Body.String())
	}
	if notes := d.provider.complaints(); len(notes) > 0 {
		t.Fatalf("the ledger was wrong while the work was happening: %s", strings.Join(notes, "; "))
	}
	completed := d.decode(t, rec)
	assertSettledOnce(t, d, completed.Run, inlet.StateCompleted)
	if row := d.run(t, completed.Run); row.Result != "triaged" || row.Error != "" {
		t.Fatalf("a completed run recorded result=%q error=%q", row.Result, row.Error)
	}

	// 2. The caller sent the wrong thing. Nobody should go looking at this
	// workspace for that.
	rec = d.deliver(t, "triage", d.key, "application/json", []byte(`{"title":"no id"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a refused delivery: got %d\nbody: %s", rec.Code, rec.Body.String())
	}
	refused := d.decode(t, rec)
	assertSettledOnce(t, d, refused.Run, inlet.StateRefusedSchema)

	// 3. This workspace is not finished: the task's agent exists but has no
	// model. That is what an imported bundle naming a model this install does
	// not have leaves behind, and nothing the caller sends can fix it.
	half, err := d.spaces.CreateWorkspaceSpec(ctx, "half-built", "",
		workspace.AgentSpec{Name: workspace.OrchestratorName, Role: "triage tickets"}, d.adminID)
	if err != nil {
		t.Fatalf("create the half-built workspace: %v", err)
	}
	halfDoor, err := d.srv.inlets.CreateInlet(ctx, half.ID, "half-built", "")
	if err != nil {
		t.Fatalf("create its inlet: %v", err)
	}
	halfKey, err := d.srv.inlets.IssueKey(ctx, halfDoor.ID)
	if err != nil {
		t.Fatalf("issue its key: %v", err)
	}
	// Through the store, not the API: the management route refuses a task whose
	// target has no model, which is exactly why this state only arrives by
	// import — and the delivery path still has to answer for it.
	if _, err := d.srv.inlets.AddTask(ctx, halfDoor.ID, inlet.Task{
		Name: "triage", Accepts: inlet.AcceptsJSON, Schema: ticketSchema,
		AgentName: workspace.OrchestratorName, Instruction: "Triage this ticket.",
	}); err != nil {
		t.Fatalf("add its task: %v", err)
	}

	before := d.provider.called()
	rec = d.deliverTo(t, "half-built", "triage", halfKey, "application/json", []byte(`{"id":7,"title":"disk full"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a delivery to an agent with no model: got %d, want 503\nbody: %s", rec.Code, rec.Body.String())
	}
	broken := d.decode(t, rec)
	if broken.State != inlet.StateFailed {
		t.Fatalf("recorded as %q, want %q", broken.State, inlet.StateFailed)
	}
	// The operator has to be told what to do, and the caller has to be able to
	// tell this apart from their own mistake.
	if !strings.Contains(broken.Error, "bind one on the blueprint") {
		t.Fatalf("the failure does not say how to fix it: %q", broken.Error)
	}
	if n := d.provider.called(); n != before {
		t.Fatalf("a delivery to a model-less agent still called a provider %d time(s)", n-before)
	}
	assertSettledOnce(t, d, broken.Run, inlet.StateFailed)

	// One row per delivery, in the ledger of the workspace it was delivered to,
	// and never two deliveries under one run number: a ledger that reused a
	// number could not answer "did job 4471 happen".
	if rows := d.runs(t); len(rows) != 2 {
		t.Fatalf("this workspace's ledger holds %d rows for the 2 deliveries it received", len(rows))
	}
	seen := map[int64]bool{completed.Run: true, refused.Run: true, broken.Run: true}
	if len(seen) != 3 {
		t.Fatalf("two deliveries were given the same run number: %v", seen)
	}
}

// assertSettledOnce proves the terminal state is what it should be AND that it
// cannot be written again. The guard is the whole reason a row cannot end up
// claiming both that the agent answered and that the payload was refused.
func assertSettledOnce(t *testing.T, d *door, runID int64, want string) {
	t.Helper()
	ctx := context.Background()

	row := d.run(t, runID)
	if row.State != want {
		t.Fatalf("run %d ended as %q, want %q (error: %q)", runID, row.State, want, row.Error)
	}

	err := d.srv.inlets.Settle(ctx, runID, inlet.StateFailed, "", "a second writer finished this run")
	if err == nil {
		t.Fatalf("run %d was settled a second time, so a finished run can be overwritten", runID)
	}
	after := d.run(t, runID)
	if after.State != row.State || after.Result != row.Result || after.Error != row.Error {
		t.Fatalf("the second settle changed run %d: %q/%q -> %q/%q",
			runID, row.State, row.Error, after.State, after.Error)
	}
}
