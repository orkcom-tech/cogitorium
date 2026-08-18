package inlet

import (
	"context"
	"fmt"
	"strings"
)

// Asking the record questions.
//
// The record answers "what did this run do" for one run at a time, and that is
// the wrong shape for every question anybody actually has after a week of
// running:
//
//	which runs called the gear I have just decided was wrong
//	which runs read the document somebody rewrote on Tuesday
//	which runs this agent did any work in at all
//	which runs failed, and did the failures do anything before they failed
//
// Answering those meant fetching every run and reading its JSON by hand.
//
// The filtering is SQL over the stored JSON rather than a decode-and-scan loop
// in Go, so a workspace with fifty thousand runs does not become fifty thousand
// JSON parses to answer "did this gear ever run". json_each is SQLite's own,
// present in every build this product ships with, and the paths are the record's
// own field names — which is why Record's JSON tags are the schema here and
// renaming one is a breaking change to this query, said out loud so nobody
// renames one casually.

// Query is what to look for. Every field is optional; an empty Query is the
// plain listing.
type Query struct {
	// Tool matches a tool the run called — "gear_deploy", "write_file". A gear
	// appears under its tool name, which is what the model called and what the
	// engine dispatched.
	Tool string
	// Agent matches an agent that did work in the run, anywhere in the
	// delegation tree, not only the agent the run started at.
	Agent string
	// Context matches a document the run read, by path.
	Context string
	// File matches a file the run produced, by workspace-relative path.
	File string
	// State matches the run's terminal state.
	State string
	// Failed narrows to the states that mean the work did not land: failed,
	// interrupted, and the two refusals. It is a separate flag from State
	// because "show me what went wrong" is one question and enumerating four
	// state names is how it goes unasked.
	Failed bool
	Limit  int
}

// failedStates are every state in which the work did not land.
//
// refused_expectation and refused_output_schema are in here and are NOT the
// same as failed: one means the record did not meet what the task declared
// success to be, the other that the answer did not fit the declared shape. They
// are separate states for a reason (see migration 0018) and they are grouped
// here only because "what went wrong" wants all of them.
var failedStates = []string{StateFailed, StateInterrupted, StateRefusedExpectation, StateRefusedOutputSchema}

// IIF(json_valid(did), did, '{}') guards every json_each below.
//
// An empty `did` is not a record of nothing, it is NO RECORD — a run from
// before records were kept, or one still in flight — and json_each on an empty
// string is a hard SQL ERROR, not an empty result: without the guard, one
// unrecorded run in a workspace makes every query in this file fail. Standing
// in an empty object makes such a row simply not match, which is the correct
// answer: we cannot say it called that gear, and we must not say it did not.

// FindRuns answers a Query.
func (s *Store) FindRuns(ctx context.Context, wsID int64, q Query) ([]Run, error) {
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	where := []string{"workspace_id = ?"}
	args := []any{wsID}

	// EXISTS over json_each rather than LIKE over the raw column: a LIKE for
	// "deploy" would match a run that wrote a file called deploy.md, an agent
	// called deploy, or the word inside a recorded argument. The question is
	// "did it call this tool", and the answer has to come from the tools list.
	if q.Tool != "" {
		where = append(where, `EXISTS (SELECT 1 FROM json_each(IIF(json_valid(inlet_runs.did), inlet_runs.did, '{}'), '$.tools')
			WHERE json_extract(value, '$.name') = ?)`)
		args = append(args, q.Tool)
	}
	if q.Agent != "" {
		where = append(where, `EXISTS (SELECT 1 FROM json_each(IIF(json_valid(inlet_runs.did), inlet_runs.did, '{}'), '$.tools')
			WHERE json_extract(value, '$.agent') = ?)`)
		args = append(args, q.Agent)
	}
	if q.Context != "" {
		where = append(where, `EXISTS (SELECT 1 FROM json_each(IIF(json_valid(inlet_runs.did), inlet_runs.did, '{}'), '$.context')
			WHERE json_extract(value, '$.path') = ?)`)
		args = append(args, q.Context)
	}
	if q.File != "" {
		where = append(where, `EXISTS (SELECT 1 FROM json_each(IIF(json_valid(inlet_runs.did), inlet_runs.did, '{}'), '$.files')
			WHERE json_extract(value, '$.path') = ?)`)
		args = append(args, q.File)
	}
	if q.State != "" {
		where = append(where, "state = ?")
		args = append(args, q.State)
	}
	if q.Failed {
		where = append(where, "state IN (?, ?, ?, ?)")
		for _, st := range failedStates {
			args = append(args, st)
		}
	}

	// A row whose did is empty has NO RECORD — it ran before records were kept,
	// or it is still in flight — and that is not the same as a record showing
	// nothing happened. Every json_each above yields nothing for it, so it
	// simply does not match, which is the right answer: we cannot say it called
	// that gear, and we must not say it did not.
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		runSelect+" WHERE "+strings.Join(where, " AND ")+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return nil, fmt.Errorf("query inlet runs: %w", err)
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan inlet run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
