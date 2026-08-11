package inlet

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// What a task declares success to be.
//
// A delivery's whole account of itself used to be the agent's prose, and a
// caller had no way to tell a job that was done from one that was claimed: a 3b
// model told to run a gear made no tool calls at all and answered "the file was
// formatted using gear_format", and the delivery returned 200 with that
// sentence as the result. Nothing had happened.
//
// An expect block is the operator's half of the answer. It says what has to
// have HAPPENED — this gear ran, that many files appeared — and every check
// reads the run's record (engine.Record, 0018) rather than anything a model
// wrote. That ordering is the whole point: nothing declared here can be
// satisfied by writing a convincing sentence, so a run whose answer is
// beautiful and whose record is empty fails.
//
// Every field is optional and a task with none behaves exactly as it did before
// this existed: nothing is checked, and the agent's answer goes back verbatim.

// Where a task's result comes from. The default is the agent, which is what
// every task did before this: the model's own words are the answer.
//
// AnswerFromGear is for a deterministic job, where the model is a router and
// its narration is not evidence — the last successful gear's stdout is returned
// and the prose is not returned at all. A retelling of a gear's output is one
// paraphrase away from being wrong, and a caller parsing it cannot tell which.
const (
	AnswerFromAgent = "agent"
	AnswerFromGear  = "gear"
)

// gearNameRe is the shape a gear's name can have. It mirrors the rule the gear
// store enforces when a gear is forged, and it is deliberately only a shape
// check: whether a gear by that name EXISTS is proved by the management route
// against the real gear store, where it cannot drift. What this catches is the
// name that could never belong to any gear — most often the tool name, because
// gear_unpack is what an operator reads in a transcript and "unpack" is what
// the gear is called.
var gearNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,48}$`)

// Expect is what a task requires of a run before its answer counts. The zero
// value declares nothing, which is every task written before this and every
// task written without an expect block.
type Expect struct {
	// ProducesFiles is the least number of files that must appear during the
	// run. It counts what the record shows — a gear's output and a file the
	// agent typed out itself both count, because both are files that appeared.
	// It is therefore NOT proof that a gear ran; RunsGear is the field for
	// that question.
	ProducesFiles int `json:"produces_files,omitempty"`
	// RunsGear is a gear that must have run AND exited successfully, named the
	// way the gear is named rather than the way the model calls it.
	RunsGear string `json:"runs_gear,omitempty"`
	// Schema is a JSON Schema the ANSWER must satisfy, in the same subset a
	// task's payload schema uses and enforced by the same validator.
	Schema json.RawMessage `json:"schema,omitempty"`
	// AnswerFrom decides what the caller is given. Empty means the agent.
	AnswerFrom string `json:"answer_from,omitempty"`
}

// Declared reports whether this task expects anything at all. A task that
// declares nothing is never judged, so this is the line between the old
// behaviour and the new one.
func (e Expect) Declared() bool {
	return e.ProducesFiles != 0 || e.RunsGear != "" || e.HasSchema() || e.AnswerFrom != ""
}

// HasSchema treats an absent block and a literal null as the same thing: a task
// that declares no shape for its answer. An empty object is not the same and is
// not caught here — {} still requires the answer to BE JSON, which is a real
// thing to require of a run whose result something downstream will parse.
func (e Expect) HasSchema() bool {
	trimmed := strings.TrimSpace(string(e.Schema))
	return trimmed != "" && trimmed != "null"
}

// AnswersFromGear reports whether the result is the last successful gear's
// stdout rather than the agent's prose.
func (e Expect) AnswersFromGear() bool { return e.AnswerFrom == AnswerFromGear }

// check proves an expect block is one this server can actually enforce, at the
// moment the operator writes it. The alternative is a task that stores a
// promise nobody keeps and refuses — or worse, accepts — every delivery months
// later, when the reader is a caller who cannot fix it.
func (e *Expect) check() error {
	if e.ProducesFiles < 0 {
		return fmt.Errorf("expect.produces_files is the least number of files that must appear during the run, so it cannot be %d — use 0, or leave it out, to require none", e.ProducesFiles)
	}

	e.RunsGear = strings.TrimSpace(e.RunsGear)
	if e.RunsGear != "" {
		if name, ok := strings.CutPrefix(e.RunsGear, "gear_"); ok {
			return fmt.Errorf("expect.runs_gear is the gear's own name, not the tool name a model calls it by: write %q rather than %q", name, e.RunsGear)
		}
		if !gearNameRe.MatchString(e.RunsGear) {
			return fmt.Errorf("expect.runs_gear %q is not a gear name: use lowercase letters, digits and underscores (2-49 characters) starting with a letter", e.RunsGear)
		}
	}

	if e.HasSchema() {
		if err := CheckSchema(string(e.Schema)); err != nil {
			// The complaint from CheckSchema names the task schema, and this
			// one is about the answer. Saying which is the difference between
			// an operator fixing the right field and reading their payload
			// schema twice looking for a keyword that is not in it.
			return fmt.Errorf("expect.schema is the shape the ANSWER must have: %w", err)
		}
	} else {
		e.Schema = nil
	}

	e.AnswerFrom = strings.TrimSpace(e.AnswerFrom)
	switch e.AnswerFrom {
	case "", AnswerFromAgent, AnswerFromGear:
	default:
		return fmt.Errorf("expect.answer_from is %q or %q (got %q): %q returns the agent's own words, %q returns the stdout of the last gear that succeeded and does not return the prose at all",
			AnswerFromAgent, AnswerFromGear, e.AnswerFrom, AnswerFromAgent, AnswerFromGear)
	}
	return nil
}

// encode renders an expect block for its column. A task that declares nothing
// stores the empty string rather than "{}", so a row written before this
// existed and a task written without an expect block are the same row — which
// is what makes "a task with no expect behaves exactly as before" a fact about
// the data rather than a claim about the code.
func (e Expect) encode() (string, error) {
	if !e.Declared() {
		return "", nil
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("store this task's expect block: %w", err)
	}
	return string(raw), nil
}

// decodeExpect reads the column back. An unreadable block is an ERROR and not
// an empty one: silently dropping it would turn a task whose work is checked
// into a task whose work is believed, which is the exact failure this feature
// exists to end — and it would do it quietly, months after anybody looked.
func decodeExpect(raw string) (Expect, error) {
	if strings.TrimSpace(raw) == "" {
		return Expect{}, nil
	}
	var e Expect
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return Expect{}, fmt.Errorf("this task's stored expect block is unreadable, so what it requires of a run cannot be checked: %w", err)
	}
	return e, nil
}
