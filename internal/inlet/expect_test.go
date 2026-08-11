package inlet

// What a task declares success to be, on its own: what the column keeps, what
// is refused while the operator is still there to read the complaint, and what
// a complaint about an ANSWER says as against one about a payload.
//
// Same fixture as the rest of this package — a real SQLite database in a temp
// dir, built by the real migration runner — because half of what is promised
// here is about the column: a task that declares nothing must store the same
// bytes it stored before this feature existed.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// expectFixture is a store with one door on it, ready for tasks.
func expectFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s, wsID := newFixture(t)
	in, err := s.CreateInlet(context.Background(), wsID, "tickets", "")
	if err != nil {
		t.Fatalf("create inlet: %v", err)
	}
	return s, in.ID
}

// storedExpect reads the column itself rather than the decoded struct. The
// promise below is about what is on disk, and a getter that helpfully turned
// NULL into an empty block would hide exactly the thing being checked.
func storedExpect(t *testing.T, s *Store, taskID int64) string {
	t.Helper()
	var raw string
	if err := s.db.QueryRow(`SELECT expect FROM inlet_tasks WHERE id = ?`, taskID).Scan(&raw); err != nil {
		t.Fatalf("read the expect column of task %d: %v", taskID, err)
	}
	return raw
}

// TestATaskThatDeclaresNothingStoresNothing is the constraint the whole feature
// is allowed to exist under: a task with no expect block behaves exactly as it
// did before there was such a thing.
//
// It is checked at the column rather than at the API, because that is where the
// claim can actually be false. A task that stored "{}" would read back as a
// task with an expect block — declaring nothing today, but one careless default
// away from declaring something tomorrow, for every task ever written.
func TestATaskThatDeclaresNothingStoresNothing(t *testing.T) {
	t.Parallel()
	s, inletID := expectFixture(t)
	ctx := context.Background()

	task, err := s.AddTask(ctx, inletID, Task{
		Name: "triage", Accepts: AcceptsJSON,
		AgentName: "orchestrator", Instruction: "triage it",
	})
	if err != nil {
		t.Fatalf("add a task with no expect block: %v", err)
	}
	if raw := storedExpect(t, s, task.ID); raw != "" {
		t.Fatalf("a task that declares nothing stored %q in its expect column; it must store nothing at all, "+
			"so that it is the same row a task written before this feature existed would have", raw)
	}
	if task.Expect.Declared() {
		t.Fatalf("a task with no expect block came back declaring something: %+v", task.Expect)
	}

	// And it comes back the same way through every read: the delivery path
	// finds its task with TaskByName, and the panel lists them.
	byName, err := s.TaskByName(ctx, inletID, "triage")
	if err != nil {
		t.Fatalf("find the task by name: %v", err)
	}
	if byName.Expect.Declared() {
		t.Fatalf("TaskByName returned a task declaring something: %+v", byName.Expect)
	}
	listed, err := s.ListTasks(ctx, inletID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list tasks: %v (%d tasks)", err, len(listed))
	}
	if listed[0].Expect.Declared() {
		t.Fatalf("ListTasks returned a task declaring something: %+v", listed[0].Expect)
	}
}

// TestAnExpectBlockSurvivesTheRoundTrip: what the operator wrote is what will
// be compared against the record. A gear name that came back with a stray space
// on it would match nothing and read, in the ledger, as work that never
// happened — which is indistinguishable from the failure this feature exists to
// report, and would be caused by this feature.
func TestAnExpectBlockSurvivesTheRoundTrip(t *testing.T) {
	t.Parallel()
	s, inletID := expectFixture(t)
	ctx := context.Background()

	const answerSchema = `{"type":"object","required":["bucket"],"properties":{"bucket":{"type":"string"}}}`
	task, err := s.AddTask(ctx, inletID, Task{
		Name: "unpack", Accepts: AcceptsFile,
		AgentName: "orchestrator", Instruction: "unpack it",
		Expect: Expect{
			ProducesFiles: 2,
			RunsGear:      "  unpack\n",
			Schema:        json.RawMessage(answerSchema),
			AnswerFrom:    AnswerFromGear,
		},
	})
	if err != nil {
		t.Fatalf("add a task with an expect block: %v", err)
	}

	read, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read the task back: %v", err)
	}
	if read.Expect.RunsGear != "unpack" {
		t.Fatalf("the gear name came back as %q; it is compared against the record verbatim, so it is stored trimmed", read.Expect.RunsGear)
	}
	if read.Expect.ProducesFiles != 2 {
		t.Fatalf("produces_files came back as %d, want 2", read.Expect.ProducesFiles)
	}
	if !read.Expect.AnswersFromGear() {
		t.Fatalf("answer_from came back as %q, want %q", read.Expect.AnswerFrom, AnswerFromGear)
	}
	// The schema is compared as JSON rather than as bytes: it goes through a
	// column and back, and no promise is made about its spacing.
	var want, got any
	if err := json.Unmarshal([]byte(answerSchema), &want); err != nil {
		t.Fatalf("the test's own schema is not JSON: %v", err)
	}
	if err := json.Unmarshal(read.Expect.Schema, &got); err != nil {
		t.Fatalf("the stored answer schema is not JSON: %v", err)
	}
	if !jsonEqual(want, got) {
		t.Fatalf("the answer schema came back different:\nwrote: %s\nread:  %s", answerSchema, read.Expect.Schema)
	}
}

func jsonEqual(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

// TestAnExpectBlockThisServerCannotEnforceIsRefusedWhenItIsWritten. Each of
// these would otherwise be stored and then either checked wrongly or not at
// all, and the first anybody would hear of it is a pipeline refusing every
// delivery months later — read by a caller who cannot fix it.
func TestAnExpectBlockThisServerCannotEnforceIsRefusedWhenItIsWritten(t *testing.T) {
	t.Parallel()
	s, inletID := expectFixture(t)
	ctx := context.Background()

	refused := []struct {
		name   string
		expect Expect
		wants  string
	}{
		{
			"the tool name instead of the gear name",
			Expect{RunsGear: "gear_unpack"},
			`write "unpack"`,
		},
		{
			"a gear name no gear could have",
			Expect{RunsGear: "Unpack It!"},
			"is not a gear name",
		},
		{
			"a negative file count",
			Expect{ProducesFiles: -1},
			"cannot be -1",
		},
		{
			"an answer source that is neither",
			Expect{AnswerFrom: "the gear"},
			`"agent" or "gear"`,
		},
		{
			"an answer schema this server cannot enforce",
			Expect{Schema: json.RawMessage(`{"type":"object","properties":{"id":{"pattern":"^[0-9]+$"}}}`)},
			"pattern",
		},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.AddTask(ctx, inletID, Task{
				Name: "triage", Accepts: AcceptsJSON,
				AgentName: "orchestrator", Instruction: "triage it",
				Expect: c.expect,
			})
			if err == nil {
				t.Fatalf("this expect block was stored: %+v", c.expect)
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Fatalf("the refusal does not tell the operator what to write instead.\nwant: %s\ngot:  %v", c.wants, err)
			}
		})
	}

	// Control: a block built only from what is enforced is stored, so the
	// refusals above are about each mistake and not about expect blocks in
	// general.
	if _, err := s.AddTask(ctx, inletID, Task{
		Name: "triage", Accepts: AcceptsJSON,
		AgentName: "orchestrator", Instruction: "triage it",
		Expect: Expect{ProducesFiles: 1, RunsGear: "unpack", AnswerFrom: AnswerFromAgent},
	}); err != nil {
		t.Fatalf("an enforceable expect block was refused: %v", err)
	}
}

// TestAStoredExpectBlockThatWillNotDecodeFailsTheReadLoudly.
//
// The quiet alternative is the one that matters: a task whose requirements
// silently became "none" would go on answering 200 for runs that did nothing,
// which is precisely the failure this feature exists to end — restored, and
// invisible, months after anybody looked at the row.
func TestAStoredExpectBlockThatWillNotDecodeFailsTheReadLoudly(t *testing.T) {
	t.Parallel()
	s, inletID := expectFixture(t)
	ctx := context.Background()

	task, err := s.AddTask(ctx, inletID, Task{
		Name: "unpack", Accepts: AcceptsJSON,
		AgentName: "orchestrator", Instruction: "unpack it",
		Expect: Expect{RunsGear: "unpack"},
	})
	if err != nil {
		t.Fatalf("add the task: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE inlet_tasks SET expect = ? WHERE id = ?`, "{not json", task.ID); err != nil {
		t.Fatalf("corrupt the column: %v", err)
	}

	if _, err := s.TaskByName(ctx, inletID, "unpack"); err == nil {
		t.Fatal("a task whose expect block cannot be read was returned as a task; " +
			"the delivery path would then run it and check nothing")
	} else if !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("the complaint does not say what is wrong with the row: %v", err)
	}
}

// TestAnAnswerIsCheckedAgainstTheShapeTheTaskDeclares. The validator is the one
// payloads already use — one dialect of JSON Schema in this server — and what
// has to differ is the wording: an operator told "the payload" is wrong goes to
// read what their caller sent, and the thing that failed was what their own
// agent produced.
func TestAnAnswerIsCheckedAgainstTheShapeTheTaskDeclares(t *testing.T) {
	t.Parallel()

	const bucket = `{
	  "type": "object",
	  "required": ["bucket"],
	  "properties": {"bucket": {"enum": ["invoice", "receipt"]}},
	  "additionalProperties": false
	}`

	refused := []struct{ name, answer, wants string }{
		{"prose where JSON was declared", `I put it in the invoice bucket.`, "the answer is not valid JSON"},
		{"nothing at all", ``, "the answer is empty"},
		{"a missing field", `{}`, "bucket is required but the answer does not have it"},
		{"a value outside the enum", `{"bucket":"maybe"}`, "not one of the allowed values"},
		{"a field the schema does not declare", `{"bucket":"invoice","note":"hi"}`, "expect.schema declares"},
		{"an array where an object was declared", `[]`, "the answer: expected an object, got an array"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateAnswer(bucket, c.answer)
			if err == nil {
				t.Fatalf("this answer was accepted: %s", c.answer)
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Fatalf("the complaint is about the wrong thing, or does not say what.\nanswer: %s\nwant:   %s\ngot:    %v",
					c.answer, c.wants, err)
			}
			// Whatever it says, it must not send the operator to look at what
			// the caller sent.
			if strings.Contains(err.Error(), "the payload") {
				t.Fatalf("a complaint about an answer names the payload, which is the one place the mistake is not: %v", err)
			}
		})
	}

	if err := ValidateAnswer(bucket, `{"bucket":"receipt"}`); err != nil {
		t.Fatalf("an answer that fits was refused: %v", err)
	}
	// An empty schema still requires the answer to BE JSON, which is a real
	// thing to require of a result something downstream will parse.
	if err := ValidateAnswer(`{}`, `done!`); err == nil {
		t.Fatal("an empty schema accepted prose; it declares no fields, not no shape")
	}
}
