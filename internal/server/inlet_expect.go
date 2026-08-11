package server

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
)

// Where a task's expectations meet a run's record.
//
// One vocabulary, two sides: the task says what success IS (inlet.Expect), the
// run reports what HAPPENED (engine.Record), and every check below reads the
// record and nothing else. Not the answer, not its length, not whether it
// mentions a gear — a run whose prose is beautiful and whose record is empty
// fails here, which is the entire reason this file exists.
//
// It sits in the server package because this is the one place both halves are
// already in hand. The ledger deliberately does not know the record's shape
// (inlet.Run.Did is raw JSON, and says why), and the engine has no business
// knowing what an inlet task requires.

// verdict is what a task's expectations made of one run: either the result the
// caller is given, or the state it is refused with and the sentence saying why.
type verdict struct {
	// state is inlet.StateCompleted, or one of the two refusals a run can end
	// in once it has actually run.
	state string
	// result is what goes back to the caller and into the ledger's result
	// column. On a refusal it is empty: a result that did not meet the task is
	// not a result, and the complaint below carries what was produced.
	result string
	err    error
}

func (v verdict) refused() bool { return v.err != nil }

// judge holds a run against what its task declared.
//
// A task that declares nothing is not judged at all: same result, same state,
// same response as before any of this existed.
//
// The order is the order an operator needs the answer in. The record comes
// first because "the work did not happen" outranks every question about the
// shape of the sentence describing it: told that a gear never ran, nobody needs
// to hear that the answer was also not valid JSON.
func judge(exp inlet.Expect, out engine.Outcome) verdict {
	done := verdict{state: inlet.StateCompleted, result: out.Answer}
	if !exp.Declared() {
		return done
	}

	if exp.RunsGear != "" && !out.Did.RanGear(exp.RunsGear) {
		// "no successful call to it" rather than "it never ran": the record
		// distinguishes a gear that was never called from one that was called
		// and exited non-zero, both of which land here, and the list below
		// says which. A lead sentence that guessed would be wrong half the
		// time, and it is the half where somebody is already reading stderr.
		return expectationRefused(fmt.Errorf(
			"this task requires the gear %q to have run and succeeded, and the record holds no successful call to it. %s",
			exp.RunsGear, showRecord(out.Did)))
	}

	if exp.ProducesFiles > 0 && out.Did.DistinctFiles() < exp.ProducesFiles {
		return expectationRefused(fmt.Errorf(
			"this task requires at least %s to appear during the run, and %d did. %s",
			plural(exp.ProducesFiles, "file"), out.Did.DistinctFiles(), showRecord(out.Did)))
	}

	if exp.AnswersFromGear() {
		// The tool list decides this, never the output string: a gear that ran
		// and printed nothing leaves GearOutput empty, and so does a run in
		// which no gear ran at all. The first is an empty answer and the second
		// is a job that did not happen.
		if !out.Did.RanAnyGear() {
			return expectationRefused(fmt.Errorf(
				"this task's answer is the output of the gear that did the work, and no gear ran in this delivery. %s",
				showRecord(out.Did)))
		}
		// The agent's prose is not returned at all. For a deterministic job the
		// model is a router, and its retelling of a gear's output is one
		// paraphrase away from being wrong — with nothing in the response to
		// tell a caller which they got.
		done.result = out.GearOutput
	}

	if exp.HasSchema() {
		if err := inlet.ValidateAnswer(string(exp.Schema), done.result); err != nil {
			// A different state from the one above, on purpose: "the model
			// produced garbage" and "the work did not happen" want different
			// people woken up. This one is a prompting or model problem and the
			// work may well be done; the record beside it says which.
			return verdict{state: inlet.StateRefusedOutputSchema, err: fmt.Errorf(
				"the answer does not fit the shape this task declares: %w\nthe answer was: %s\n%s",
				err, showAnswer(done.result), showRecord(out.Did))}
		}
	}
	return done
}

func expectationRefused(err error) verdict {
	return verdict{state: inlet.StateRefusedExpectation, err: err}
}

// showRecord renders what the run actually did, and it is appended to every
// refusal above rather than left for the reader to go and look up.
//
// A message that says only what was expected sends whoever is paged to the
// ledger to find out what happened instead; at 3am that is the difference
// between "the gear was never called" and "the gear was called and exited 1",
// which are two different people's problems and are both one line away here.
func showRecord(did engine.Record) string {
	return fmt.Sprintf("What this run did: %s; %s; %s.",
		showTools(did.Tools), showFiles(did.Files), showCost(did))
}

// listCap bounds both lists. A run that called forty tools has a first few that
// say what went wrong, and forty lines in an error field help nobody — the full
// record is on the response and in the ledger row either way.
const listCap = 8

func showTools(tools []engine.ToolRun) string {
	if len(tools) == 0 {
		// The empty list is the answer to "did it do anything", so it is said
		// as plainly as it can be said.
		return "no tool calls at all"
	}
	parts := make([]string, 0, listCap)
	for i, t := range tools {
		if i == listCap {
			parts = append(parts, fmt.Sprintf("and %d more", len(tools)-listCap))
			break
		}
		outcome := "failed"
		if t.OK {
			outcome = "ok"
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %dms)", t.Name, outcome, t.Ms))
	}
	return fmt.Sprintf("%s: %s", plural(len(tools), "tool call"), strings.Join(parts, ", "))
}

func showFiles(files []engine.FileMade) string {
	if len(files) == 0 {
		return "no files appeared"
	}
	parts := make([]string, 0, listCap)
	for i, f := range files {
		if i == listCap {
			parts = append(parts, fmt.Sprintf("and %d more", len(files)-listCap))
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%d bytes)", f.Path, f.Bytes))
	}
	return fmt.Sprintf("%s: %s", plural(len(files), "file"), strings.Join(parts, ", "))
}

// showCost names what the run spent. It is here because a refusal with one
// model call and no tools is a model that answered off the top of its head,
// while six model calls and no tools is a model that tried and could not — and
// the two want different fixes.
func showCost(did engine.Record) string {
	return fmt.Sprintf("%s, %d tokens in and %d out",
		plural(did.ModelCalls, "model call"), did.Tokens.In, did.Tokens.Out)
}

// showAnswer quotes what the run produced, capped. An operator told their
// answer did not fit a schema needs to see the answer, and a model that
// ignored its instructions gives itself away in the first line of it.
func showAnswer(answer string) string {
	const quoteLimit = 300
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return "(nothing at all)"
	}
	if len(trimmed) > quoteLimit {
		// Cut on a rune boundary. An answer is whatever a model produced, so it
		// may be full of multi-byte characters, and half of one would go into
		// the response as a replacement character — a corruption the operator
		// would reasonably read as the model's own output.
		cut := quoteLimit
		for cut > 0 && !utf8.RuneStart(trimmed[cut]) {
			cut--
		}
		return fmt.Sprintf("%q… (%d bytes in all)", trimmed[:cut], len(answer))
	}
	return fmt.Sprintf("%q", trimmed)
}

// plural writes a count as English. "1 files appeared" is the sort of sentence
// a reader stops on, wondering whether it means something they missed.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
