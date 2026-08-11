package server

// What a task requires, held against what a run recorded.
//
// This is the defect the whole feature comes from, and the first test here is
// it: a 3b model was told to call a gear on a delivered file, made ZERO tool
// calls, and answered "The comma-separated text file was aligned and formatted
// using gear_format …". The delivery returned 200, state=completed, and that
// sentence as the result. No folder, no file, no gear run — and nothing in the
// response a caller could have used to tell that job from one that happened.
//
// Every test drives the real handler chain against a real SQLite database, with
// a provider on a real socket saying exactly what a model of that size said.
// Where a gear has to actually run it is a real gear in a real subprocess.

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// claimedItRan is the sentence from the live run, near enough verbatim. It is
// the whole problem in one string: fluent, specific, and about work that never
// happened.
const claimedItRan = "The comma-separated text file was aligned and formatted using gear_format " +
	"with path inlets/tickets/1-payload.txt into folder formatted"

// theRecord is the run's record as it appears ON THE WIRE. It is spelled out
// here rather than decoded into engine.Record because the claim being tested is
// that the record reached the caller: a test that unmarshalled with the same
// struct the server marshalled with would pass on a field that was renamed at
// both ends at once.
type theRecord struct {
	Tools []struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Ms   int64  `json:"ms"`
	} `json:"tools"`
	Files []struct {
		Path  string `json:"path"`
		Bytes int64  `json:"bytes"`
	} `json:"files"`
	ModelCalls int `json:"model_calls"`
	Tokens     struct {
		In  int `json:"in"`
		Out int `json:"out"`
	} `json:"tokens"`
}

func (r theRecord) ran(name string) bool {
	for _, t := range r.Tools {
		if t.Name == name && t.OK {
			return true
		}
	}
	return false
}

func decodeRecord(t *testing.T, where string, raw []byte) theRecord {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		t.Fatalf("%s carries no record at all, so nothing about this run can be checked without believing it", where)
	}
	var r theRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("%s carries something that is not a record: %s (%v)", where, raw, err)
	}
	return r
}

// recordOn pulls the record out of a delivery response.
func recordOn(t *testing.T, rec *httptest.ResponseRecorder) theRecord {
	t.Helper()
	var body struct {
		Did json.RawMessage `json:"did"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the delivery answer is not JSON: %s", rec.Body.String())
	}
	return decodeRecord(t, "the delivery response", body.Did)
}

// addExpectingTask puts a job behind the door with what it requires of a run.
// It goes through the store because the management route is tested on its own
// below; here the task is setup, not the subject.
func (d *door) addExpectingTask(t *testing.T, name, accepts string, exp inlet.Expect) inlet.Task {
	t.Helper()
	task, err := d.srv.inlets.AddTask(context.Background(), d.inletID, inlet.Task{
		Name: name, Accepts: accepts, Schema: "{}",
		AgentName: workspace.OrchestratorName, Instruction: "do the job",
		Expect: exp,
	})
	if err != nil {
		t.Fatalf("add task %q: %v", name, err)
	}
	return task
}

// mustSay fails the test with the whole message when a complaint does not carry
// something the operator needs. Every fragment asked for below is either what
// was expected or what the record showed — the two halves a message has to have
// for the reader not to go looking.
func mustSay(t *testing.T, what, message string, fragments ...string) {
	t.Helper()
	for _, want := range fragments {
		if !strings.Contains(message, want) {
			t.Fatalf("%s does not mention %q, so whoever reads it at 3am has to go looking:\n%s", what, want, message)
		}
	}
}

// --- 1. The reported failure. ---------------------------------------------

// TestAnAnswerAboutWorkThatNeverHappenedIsRefused reproduces the live run and
// holds the new answer to it: the gear the task requires never ran, the record
// says so, and the delivery is refused with the prose nowhere near the result.
//
// The control at the bottom is the constraint this feature lives under. The
// same door, the same model, the same sentence — and a task that requires
// nothing still answers 200 with that sentence, exactly as it did before any of
// this existed.
func TestAnAnswerAboutWorkThatNeverHappenedIsRefused(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addExpectingTask(t, "format", inlet.AcceptsJSON, inlet.Expect{RunsGear: "format"})
	d.addExpectingTask(t, "format_unchecked", inlet.AcceptsJSON, inlet.Expect{})

	// The model of the live run: no tool calls, one confident sentence.
	d.provider.answers(func(n int, c modelCall) modelReply { return says(claimedItRan) })

	rec := d.deliver(t, "format", d.key, "application/json", []byte(`{"path":"usage.csv"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a delivery that did nothing answered %d; the caller must not be told this worked\nbody: %s",
			rec.Code, rec.Body.String())
	}
	got := d.decode(t, rec)
	if got.State != inlet.StateRefusedExpectation {
		t.Fatalf("the run ended as %q, want %q", got.State, inlet.StateRefusedExpectation)
	}
	if got.Result != "" {
		t.Fatalf("the agent's sentence came back as the result of a run that did nothing: %q", got.Result)
	}
	if strings.Contains(got.Error, claimedItRan) {
		t.Fatalf("the refusal quotes the agent's account of the work, which is the one thing that is not evidence:\n%s", got.Error)
	}
	// What was expected, and what the record shows. Both, or the reader goes
	// digging for the half that is missing.
	mustSay(t, "the refusal", got.Error, `"format"`, "no tool calls at all", "no files appeared", "1 model call")

	// The record is on the response, and its empty tools list IS the answer to
	// "did it do anything".
	did := recordOn(t, rec)
	if len(did.Tools) != 0 || len(did.Files) != 0 {
		t.Fatalf("the record shows work for a run that made no tool calls: %+v", did)
	}
	if did.ModelCalls != 1 {
		t.Fatalf("the record counts %d model calls for one answered turn", did.ModelCalls)
	}

	// And the ledger row says the same thing, in the same words.
	row := d.run(t, got.Run)
	if row.State != inlet.StateRefusedExpectation || row.Error != got.Error || row.Result != "" {
		t.Fatalf("the row and the response tell different stories:\nrow:      %s / %q / %q\nresponse: %s / %q / %q",
			row.State, row.Result, row.Error, got.State, got.Result, got.Error)
	}
	if !strings.Contains(string(row.Did), `"tools":[]`) {
		t.Fatalf("the row's record does not carry an empty tool list, which is the fact being recorded: %s", row.Did)
	}
	decodeRecord(t, "the ledger row", row.Did)

	// The control: the same everything, minus the expectation.
	d.provider.reset()
	rec = d.deliver(t, "format_unchecked", d.key, "application/json", []byte(`{"path":"usage.csv"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("a task that requires nothing answered %d; it must behave exactly as it did before expectations existed\nbody: %s",
			rec.Code, rec.Body.String())
	}
	unchecked := d.decode(t, rec)
	if unchecked.State != inlet.StateCompleted || unchecked.Result != claimedItRan {
		t.Fatalf("a task that requires nothing changed its answer: %s / %q", unchecked.State, unchecked.Result)
	}
}

// --- 2. Files are counted, not described. ---------------------------------

// TestFilesAreCountedFromTheRecordAndNotFromTheProse. The first half is a model
// saying it wrote a file; the second is a model writing one. Nothing in the two
// answers distinguishes them, which is the point — the record does.
//
// The second half also pins what produces_files means: write_file counts. A file
// the agent typed out itself is a file that appeared, and a record that hid it
// would be lying about the workspace to keep a check tidy.
func TestFilesAreCountedFromTheRecordAndNotFromTheProse(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addExpectingTask(t, "summarise", inlet.AcceptsJSON, inlet.Expect{ProducesFiles: 1})

	d.provider.answers(func(n int, c modelCall) modelReply {
		return says("I wrote the summary to summary.md as asked.")
	})
	rec := d.deliver(t, "summarise", d.key, "application/json", []byte(`{"of":"the quarter"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a run that produced no file answered %d\nbody: %s", rec.Code, rec.Body.String())
	}
	got := d.decode(t, rec)
	if got.State != inlet.StateRefusedExpectation {
		t.Fatalf("the run ended as %q, want %q", got.State, inlet.StateRefusedExpectation)
	}
	mustSay(t, "the refusal", got.Error, "at least 1 file", "no files appeared")

	// Now the same task, with a model that actually writes the file.
	d.provider.reset()
	d.provider.answers(func(n int, c modelCall) modelReply {
		if n == 1 {
			return asksFor(modelToolCall{
				ID: "c1", Name: "write_file",
				Args: `{"path":"summary.md","content":"the quarter was fine\n"}`,
			})
		}
		return says("I wrote the summary to summary.md as asked.")
	})
	rec = d.deliver(t, "summarise", d.key, "application/json", []byte(`{"of":"the quarter"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("a run that wrote the file answered %d\nbody: %s", rec.Code, rec.Body.String())
	}
	did := recordOn(t, rec)
	if len(did.Files) != 1 || did.Files[0].Path != "summary.md" {
		t.Fatalf("the file the agent wrote is not in the record, so produces_files counts something else: %+v", did.Files)
	}
	if !did.ran("write_file") {
		t.Fatalf("write_file is not in the record's tool list: %+v", did.Tools)
	}
}

// --- 3. The answer comes from the gear, or the run failed. ----------------

// TestTheAnswerIsTheGearsOutputWhenTheTaskSaysSo runs a real gear in a real
// subprocess on a really delivered file, and requires that what comes back is
// what the gear printed and not what the agent said about it.
//
// For a deterministic job the model is a router: its retelling of a gear's
// output is one paraphrase away from being wrong, with nothing in the response
// to tell a caller which they got. So the prose is not returned at all, and
// this test fails if it appears anywhere in the result.
func TestTheAnswerIsTheGearsOutputWhenTheTaskSaysSo(t *testing.T) {
	t.Parallel()
	d := newRunningDoor(t)
	d.forge(t, carryGear)
	d.addExpectingTask(t, "carry", inlet.AcceptsFile, inlet.Expect{
		RunsGear: "carry", ProducesFiles: 1, AnswerFrom: inlet.AnswerFromGear,
	})
	d.handToGear("carry", "")

	rec := d.deliver(t, "carry", d.key, "text/csv", []byte("region,units\nnorth,17\n"))
	if rec.Code != http.StatusOK {
		t.Fatalf("the delivery answered %d\nbody: %s", rec.Code, rec.Body.String())
	}
	if notes := d.provider.complaints(); len(notes) > 0 {
		t.Fatalf("the scripted agent could not do its part: %v", notes)
	}
	got := d.decode(t, rec)
	if got.State != inlet.StateCompleted {
		t.Fatalf("the run ended as %q: %s", got.State, got.Error)
	}

	// carryGear prints the workspace path of every file it was handed, which is
	// where the delivered file landed.
	row := d.run(t, got.Run)
	if !reportsLine(got.Result, row.PayloadPath) {
		t.Fatalf("the result is not the gear's output. The gear prints the path it opened (%q); this came back:\n%q",
			row.PayloadPath, got.Result)
	}
	if strings.Contains(got.Result, theAnswer) {
		t.Fatalf("the agent's own words came back as the result of a task that takes its answer from the gear:\n%q", got.Result)
	}
	if row.Result != got.Result {
		t.Fatalf("the ledger kept a different result from the one the caller got:\nrow:      %q\nresponse: %q", row.Result, got.Result)
	}

	did := recordOn(t, rec)
	if !did.ran("gear_carry") {
		t.Fatalf("the gear is not in the record, so the task's requirement was met by something else: %+v", did.Tools)
	}
	if len(did.Files) == 0 {
		t.Fatalf("the gear produced a file and the record has none: %+v", did)
	}
}

// TestATaskWhoseAnswerIsAGearsOutputFailsWhenNoGearRan is the other half, and
// the reason the check reads the tool list rather than the output string: a
// gear that ran and printed nothing leaves the same empty string as a delivery
// in which no gear ran at all. The first is an empty answer; the second is a
// job that did not happen, and it is a failure.
func TestATaskWhoseAnswerIsAGearsOutputFailsWhenNoGearRan(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addExpectingTask(t, "carry", inlet.AcceptsJSON, inlet.Expect{AnswerFrom: inlet.AnswerFromGear})

	d.provider.answers(func(n int, c modelCall) modelReply {
		return says("Done — the gear processed the file and everything looks good.")
	})
	rec := d.deliver(t, "carry", d.key, "application/json", []byte(`{"of":"the quarter"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a delivery in which no gear ran answered %d\nbody: %s", rec.Code, rec.Body.String())
	}
	got := d.decode(t, rec)
	if got.State != inlet.StateRefusedExpectation {
		t.Fatalf("the run ended as %q, want %q — an empty answer is not the same as a job that did not happen",
			got.State, inlet.StateRefusedExpectation)
	}
	if got.Result != "" {
		t.Fatalf("something came back as the result: %q", got.Result)
	}
	mustSay(t, "the refusal", got.Error, "no gear ran", "no tool calls at all")
}

// --- 4. The shape of the answer is a different failure. -------------------

// TestAnAnswerInTheWrongShapeIsItsOwnFailure. The two refusals are kept apart
// because they want different people woken up: "the model produced garbage" is
// a prompting problem with the work possibly done, and "the work did not
// happen" is an outage. The last case here is what enforces the order — a run
// that failed both is reported as the second, because nobody told a gear never
// ran needs to hear that the sentence about it was also not JSON.
func TestAnAnswerInTheWrongShapeIsItsOwnFailure(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	const bucket = `{"type":"object","required":["bucket"],"properties":{"bucket":{"enum":["invoice","receipt"]}}}`

	d.addExpectingTask(t, "classify", inlet.AcceptsJSON, inlet.Expect{Schema: json.RawMessage(bucket)})
	d.addExpectingTask(t, "classify_and_file", inlet.AcceptsJSON, inlet.Expect{
		Schema: json.RawMessage(bucket), RunsGear: "unpack",
	})

	const prose = "It is an invoice, filed under the invoice bucket."
	d.provider.answers(func(n int, c modelCall) modelReply { return says(prose) })

	rec := d.deliver(t, "classify", d.key, "application/json", []byte(`{"doc":"x"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("an answer in the wrong shape answered %d\nbody: %s", rec.Code, rec.Body.String())
	}
	got := d.decode(t, rec)
	if got.State != inlet.StateRefusedOutputSchema {
		t.Fatalf("the run ended as %q, want %q", got.State, inlet.StateRefusedOutputSchema)
	}
	// The operator needs the answer itself here: a model that ignored its
	// instructions gives itself away in the first line of what it wrote.
	mustSay(t, "the refusal", got.Error, "not valid JSON", prose, "1 model call")

	// The work happened, the shape was wrong: same run, JSON this time.
	d.provider.reset()
	d.provider.answers(func(n int, c modelCall) modelReply { return says(`{"bucket":"invoice"}`) })
	rec = d.deliver(t, "classify", d.key, "application/json", []byte(`{"doc":"x"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("an answer that fits the declared shape answered %d\nbody: %s", rec.Code, rec.Body.String())
	}
	if fits := d.decode(t, rec); fits.State != inlet.StateCompleted || fits.Result != `{"bucket":"invoice"}` {
		t.Fatalf("a fitting answer was not returned as it was written: %s / %q", fits.State, fits.Result)
	}

	// Both wrong at once: the record outranks the shape.
	d.provider.reset()
	d.provider.answers(func(n int, c modelCall) modelReply { return says(prose) })
	rec = d.deliver(t, "classify_and_file", d.key, "application/json", []byte(`{"doc":"x"}`))
	both := d.decode(t, rec)
	if both.State != inlet.StateRefusedExpectation {
		t.Fatalf("a run that did no work and answered in the wrong shape was reported as %q; "+
			"the work is what somebody is paged about, so it is reported first", both.State)
	}
}

// --- 5. The operator is told at the moment they write it. -----------------

// TestATaskCannotRequireAGearNobodyHasForged. A typo in a gear name does not
// fail loudly: it refuses every delivery for the rest of the task's life, with
// a message about work that never happened — which reads exactly like a broken
// pipeline and sends whoever is paged to look at everything except the one word
// that is wrong. So it is refused here, while the operator who typed it is
// still on the screen.
func TestATaskCannotRequireAGearNobodyHasForged(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	path := "/api/v1/inlets/" + id(d.inletID) + "/tasks"

	rec := d.request(t, http.MethodPost, path, d.adminTok,
		`{"name":"unpack","accepts":"json","agent":"orchestrator","instruction":"unpack it",
		  "expect":{"runs_gear":"unpack"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a task requiring a gear nobody has forged was accepted: %d %s", rec.Code, rec.Body.String())
	}
	mustSay(t, "the refusal", rec.Body.String(), "unpack", "Forge it first")

	// The same request against a gear that exists is stored, and comes back
	// carrying what it requires.
	d.forge(t, carryGear)
	rec = d.request(t, http.MethodPost, path, d.adminTok,
		`{"name":"carry","accepts":"json","agent":"orchestrator","instruction":"carry it",
		  "expect":{"runs_gear":"carry","produces_files":2,"answer_from":"gear",
		            "schema":{"type":"object"}}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("a task requiring a forged gear was refused: %d %s", rec.Code, rec.Body.String())
	}
	var created inlet.Task
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("the created task is not a task: %s", rec.Body.String())
	}
	if created.Expect.RunsGear != "carry" || created.Expect.ProducesFiles != 2 || !created.Expect.AnswersFromGear() {
		t.Fatalf("what the task requires did not survive the route: %+v", created.Expect)
	}
	if len(created.Expect.Schema) == 0 {
		t.Fatal("the answer schema did not survive the route, so the answer would be checked against nothing")
	}

	// And an expect block this server cannot enforce is a 400 rather than a
	// 500: it is something the operator wrote.
	rec = d.request(t, http.MethodPost, path, d.adminTok,
		`{"name":"broken","accepts":"json","agent":"orchestrator","instruction":"do it",
		  "expect":{"answer_from":"whatever"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unenforceable expect block answered %d: %s", rec.Code, rec.Body.String())
	}
}

// --- 6. A task that requires nothing is the task it was. ------------------

// TestATaskThatRequiresNothingIsTheTaskItWasBefore is the constraint the whole
// feature lives under, tested on its own rather than as a footnote to another
// case: every task written before expectations existed must still behave to the
// byte as it did, and the ONLY difference in the response is that did is now on
// it.
//
// It is checked at both ends. In the column, because a task that declares
// nothing storing "{}" would be a row that differs from the row it was, and the
// next reader of that column would have to know that "{}" and "" mean the same
// thing — the kind of distinction that survives exactly until somebody writes a
// query. And in the response, with the model from the live run saying the same
// untrue sentence: with nothing declared, that sentence is still the result and
// the delivery is still a 200. Refusing it here would be this feature deciding
// what every existing pipeline meant.
func TestATaskThatRequiresNothingIsTheTaskItWasBefore(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	task := d.addJSONTask(t, "triage", "{}", workspace.OrchestratorName, "triage it")

	var stored string
	if err := d.db.QueryRow(`SELECT expect FROM inlet_tasks WHERE id = ?`, task.ID).Scan(&stored); err != nil {
		t.Fatalf("read the task's expect column: %v", err)
	}
	if stored != "" {
		t.Fatalf("a task that declares nothing stored %q in its expect column. It has to be the empty string: "+
			"that is what makes \"a task written before this existed is unchanged\" a fact about the row "+
			"rather than a claim about the code", stored)
	}

	d.provider.answers(func(n int, c modelCall) modelReply { return says(claimedItRan) })

	rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"path":"usage.csv"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("a task that requires nothing answered %d\nbody: %s", rec.Code, rec.Body.String())
	}
	got := d.decode(t, rec)
	if got.State != inlet.StateCompleted {
		t.Fatalf("the run ended as %q, want %q", got.State, inlet.StateCompleted)
	}
	if got.Result != claimedItRan {
		t.Fatalf("the agent's answer did not come back as it was written:\nwant %q\ngot  %q", claimedItRan, got.Result)
	}
	if got.Error != "" {
		t.Fatalf("a completed run carries an error: %q", got.Error)
	}

	// The one addition. It is unconditional — there is no flag to turn it off,
	// because a caller who has to ask for the evidence is a caller who will not.
	did := recordOn(t, rec)
	if did.ModelCalls != 1 || len(did.Tools) != 0 {
		t.Fatalf("the record is on the response but does not describe this run: %+v", did)
	}

	row := d.run(t, got.Run)
	if row.State != inlet.StateCompleted || row.Result != claimedItRan || row.Error != "" {
		t.Fatalf("the row and the response tell different stories:\nrow:      %s / %q / %q\nresponse: %s / %q / %q",
			row.State, row.Result, row.Error, got.State, got.Result, got.Error)
	}
}

// --- 7. The record is kept on the way down, too. --------------------------

// TestTheRecordSurvivesAFailure. A run that wrote two files and then fell over
// wrote two files, and the moment somebody is woken up is the moment they need
// to know it: whoever is deciding whether to retry has to know what is already
// on disk, and a failure with an empty record and a failure after half the job
// are two different pages at 3am.
//
// The task declares nothing, on purpose. The record is not a thing expectations
// switch on — it is what every run reports — so this holds the failing path of
// a perfectly ordinary task.
func TestTheRecordSurvivesAFailure(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addJSONTask(t, "halfway", "{}", workspace.OrchestratorName, "do the job")

	// Two files, then the failure the engine refuses to pass off as an answer:
	// a model that stops with nothing to say.
	d.provider.answers(func(n int, c modelCall) modelReply {
		switch n {
		case 1:
			return asksFor(modelToolCall{ID: "c1", Name: "write_file",
				Args: `{"path":"notes/one.md","content":"first\n"}`})
		case 2:
			return asksFor(modelToolCall{ID: "c2", Name: "write_file",
				Args: `{"path":"notes/two.md","content":"second\n"}`})
		}
		return says("")
	})

	rec := d.deliver(t, "halfway", d.key, "application/json", []byte(`{"of":"the quarter"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a run that produced no answer answered %d\nbody: %s", rec.Code, rec.Body.String())
	}
	got := d.decode(t, rec)
	if got.State != inlet.StateFailed {
		t.Fatalf("the run ended as %q, want %q", got.State, inlet.StateFailed)
	}

	did := recordOn(t, rec)
	if len(did.Files) != 2 {
		t.Fatalf("the run wrote two files before it fell over and the record reports %d. "+
			"Whoever is deciding whether to retry is now guessing at what is already on disk: %+v",
			len(did.Files), did.Files)
	}
	for i, want := range []string{"notes/one.md", "notes/two.md"} {
		if did.Files[i].Path != want {
			t.Fatalf("the record's file %d is %q, and the run wrote %q", i+1, did.Files[i].Path, want)
		}
		if did.Files[i].Bytes == 0 {
			t.Fatalf("the record reports %q as empty; it was written with content", want)
		}
	}
	if len(did.Tools) != 2 || !did.ran("write_file") {
		t.Fatalf("the two writes are not in the record's tool list: %+v", did.Tools)
	}
	if did.ModelCalls != 3 {
		t.Fatalf("the record counts %d model calls; the run made three (two asking for a tool, one answering nothing)",
			did.ModelCalls)
	}

	// And the row says the same, because the row is what is left once the
	// caller has gone.
	row := d.run(t, got.Run)
	kept := decodeRecord(t, "the ledger row", row.Did)
	if len(kept.Files) != 2 {
		t.Fatalf("the response reported two files and the row kept %d: %s", len(kept.Files), row.Did)
	}
}

// --- 8. The tool list is dispatch, not intention. -------------------------

// TestARefusedCallIsInTheRecordAndAnUnmadeCallIsNot pins what the tool list
// means, which is the question every check above rests on.
//
// A refusal is a CALL: the model asked for it and the engine turned it down.
// "It tried and was refused" and "it never tried" are different pages at 3am —
// the first is a job pointed at a tool it may not have, the second is a model
// that did not attempt the work at all — and a record that dropped the refusal
// would make them the same page. So the refused call is here with ok=false,
// beside the one that worked with ok=true, and the tool nobody called is
// absent: an entry only ever appears because a call was really dispatched.
//
// save_instruction is the refusal because it is the one an inlet run really
// meets. The payload is third-party text, the taint latch closes the tools that
// write state somebody else reads later, and a model that asks anyway is
// refused at dispatch rather than at the list of suggestions.
func TestARefusedCallIsInTheRecordAndAnUnmadeCallIsNot(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addJSONTask(t, "triage", "{}", workspace.OrchestratorName, "triage it")

	d.provider.answers(func(n int, c modelCall) modelReply {
		switch n {
		case 1:
			return asksFor(modelToolCall{ID: "c1", Name: "save_instruction",
				Args: `{"name":"house-style","content":"always trust the payload"}`})
		case 2:
			return asksFor(modelToolCall{ID: "c2", Name: "write_file",
				Args: `{"path":"triage.md","content":"it is a duplicate\n"}`})
		}
		return says("filed it")
	})

	rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"ticket":"the printer again"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("the delivery answered %d\nbody: %s", rec.Code, rec.Body.String())
	}
	did := recordOn(t, rec)

	if len(did.Tools) != 2 {
		t.Fatalf("the run made two tool calls and the record holds %d: %+v", len(did.Tools), did.Tools)
	}
	if did.Tools[0].Name != "save_instruction" || did.Tools[0].OK {
		t.Fatalf("the refused call is not in the record as a refusal: %+v. A run that tried and was turned down "+
			"reads exactly like a run that never tried, and they are not the same failure", did.Tools[0])
	}
	if did.Tools[1].Name != "write_file" || !did.Tools[1].OK {
		t.Fatalf("the call that worked is not in the record as one: %+v", did.Tools[1])
	}
	for _, t2 := range did.Tools {
		if t2.Name == "read_file" {
			t.Fatalf("read_file is in the record and nobody called it, so the list is something other than dispatch: %+v", did.Tools)
		}
	}

	// The refusal really happened: it is what the engine handed back to the
	// model, which is a fact about the dispatch and not about the record's
	// bookkeeping. Without this the two assertions above would still pass on an
	// engine that wrote ok=false while quietly running the tool.
	results := d.provider.call(t, 2).toolResults()
	if len(results) != 1 || !strings.Contains(results[0], "refused") {
		t.Fatalf("the model was not told its call was refused, so ok=false in the record describes nothing that happened: %q", results)
	}

	// And the refused call is one the record shows as a call — which is what
	// makes the sentence an operator reads say "no successful call to it"
	// rather than "it never ran".
	if len(did.Files) != 1 || did.Files[0].Path != "triage.md" {
		t.Fatalf("the file the successful call wrote is not in the record: %+v", did.Files)
	}
}

// sulkGear does half its job and fails: it leaves a file in out/ and exits 1.
//
// Both halves are the point. The executor collects out/ whatever happened, so
// the file is real and belongs in the record; the exit code is what says the
// gear did not succeed, and a task requiring this gear must not be satisfied by
// the wreckage.
var sulkGear = forgedGear{
	name: "sulk", runtime: "bash", entrypoint: "main.sh",
	source: "set -eu\n" +
		"cat >/dev/null\n" +
		"printf 'half of it\\n' > out/half.txt\n" +
		"printf 'the second half is missing\\n' >&2\n" +
		"exit 1\n",
}

// TestAGearThatRanAndFailedIsNotAGearThatSucceeded. The record distinguishes a
// gear that was never called from one that was called and exited non-zero, and
// runs_gear is satisfied by neither — but the two send different people to
// different places, so the refusal has to carry which one it was.
//
// This is also the case where files and success come apart: the gear wrote a
// file before it fell over, that file is genuinely in the workspace, and it
// must not add up to "the gear ran".
func TestAGearThatRanAndFailedIsNotAGearThatSucceeded(t *testing.T) {
	t.Parallel()
	d := newRunningDoor(t)
	d.forge(t, sulkGear)
	d.addExpectingTask(t, "sulk", inlet.AcceptsJSON, inlet.Expect{RunsGear: "sulk"})

	d.provider.answers(func(n int, c modelCall) modelReply {
		if n == 1 {
			return asksFor(modelToolCall{ID: "c1", Name: "gear_sulk", Args: `{"` + filesArg + `":[]}`})
		}
		return says("the sulk gear ran and produced the output file")
	})

	rec := d.deliver(t, "sulk", d.key, "application/json", []byte(`{"of":"the quarter"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a delivery whose gear exited non-zero answered %d\nbody: %s", rec.Code, rec.Body.String())
	}
	got := d.decode(t, rec)
	if got.State != inlet.StateRefusedExpectation {
		t.Fatalf("the run ended as %q, want %q", got.State, inlet.StateRefusedExpectation)
	}
	// "no successful call" rather than "it never ran", and the list below it
	// showing the failed call, is the difference between somebody reading
	// stderr and somebody reading the prompt.
	mustSay(t, "the refusal", got.Error, "no successful call", "gear_sulk (failed")

	did := recordOn(t, rec)
	if len(did.Tools) != 1 || did.Tools[0].Name != "gear_sulk" || did.Tools[0].OK {
		t.Fatalf("the failed gear call is not in the record as a failed call: %+v", did.Tools)
	}
	if len(did.Files) != 1 || did.Files[0].Path == "" {
		t.Fatalf("the gear wrote a file before it fell over and the record does not have it: %+v. "+
			"That file is in the workspace whatever the exit code was", did.Files)
	}
}

// --- 9. A defect, left failing. -------------------------------------------

// TestProducesFilesCountsFilesAndNotWrites is FAILING against the code as it
// stands, and it is not a test of something that was never claimed:
// expect.produces_files is documented as "the least number of files that must
// APPEAR during the run", and the record appends one entry per write with no
// regard for whether the path is one it already holds.
//
// So an agent that writes the same file twice — a model correcting itself, or
// looping, both of which is what small models do — satisfies produces_files: 2
// with one file on disk. The refusal an operator would have got is not merely
// absent: the record they are shown lists that path twice, which reads as two
// files, and the whole point of this record is that it does not overstate what
// happened. Writing the same path twice is one file appearing, and the engine
// knows it — writeFile computes `replaced` and puts it in the tool result.
//
// The fix belongs in the record rather than in judge: any other reader of
// Did.Files (the ledger, the runs panel, the next check somebody adds) has the
// same wrong count otherwise.
func TestProducesFilesCountsFilesAndNotWrites(t *testing.T) {
	t.Parallel()
	d := newDoor(t)
	d.addExpectingTask(t, "twofiles", inlet.AcceptsJSON, inlet.Expect{ProducesFiles: 2})

	// One path, written twice. The second write replaces the first.
	d.provider.answers(func(n int, c modelCall) modelReply {
		switch n {
		case 1:
			return asksFor(modelToolCall{ID: "c1", Name: "write_file",
				Args: `{"path":"summary.md","content":"a first draft\n"}`})
		case 2:
			return asksFor(modelToolCall{ID: "c2", Name: "write_file",
				Args: `{"path":"summary.md","content":"a better draft, replacing the first\n"}`})
		}
		return says("I wrote the two summaries as asked.")
	})

	rec := d.deliver(t, "twofiles", d.key, "application/json", []byte(`{"of":"the quarter"}`))

	// What is actually on disk, counted from the disk. Nothing here is taken
	// from the record, because the record is the thing under suspicion.
	onDisk := workspaceFiles(t, d.wsRoot())
	if len(onDisk) != 1 {
		t.Fatalf("this test writes one path twice and the workspace holds %d files: %v", len(onDisk), onDisk)
	}

	did := recordOn(t, rec)
	if len(did.Files) != len(onDisk) {
		t.Errorf("the record reports %d files and %d exist. It counts writes, not files: %+v\n"+
			"A path written twice is one file, and a task requiring two of them is now satisfied by one",
			len(did.Files), len(onDisk), did.Files)
	}
	if rec.Code != http.StatusInternalServerError {
		got := d.decode(t, rec)
		t.Errorf("a run that produced ONE file answered %d for a task requiring two, with state %q. "+
			"The count came from two writes to %q", rec.Code, got.State, onDisk[0])
	}
}

// workspaceFiles lists the regular files in a workspace directory, workspace-
// relative, so a test can compare what a record claims against what is there.
func workspaceFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		found = append(found, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk the workspace directory %s: %v", root, err)
	}
	return found
}
