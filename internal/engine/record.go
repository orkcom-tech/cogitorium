package engine

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/llm"
)

// The record of what a run actually did.
//
// A delivery's whole content used to be the agent's prose, and prose is not
// evidence. Watched on a live run: a 3b model was told to call gear_format on a
// delivered text file. It made ZERO tool calls and answered "The comma-separated
// text file was aligned and formatted using gear_format with path … into folder
// formatted". The delivery returned 200, state=completed, and that sentence as
// the result. No folder, no file, no gear run. A caller could not tell that job
// from one that happened — and a better model lowers the rate of the lie without
// changing that property, which is why this is not a prompting problem.
//
// The engine already knew all of it: which tools were called and how each one
// ended, every file a gear left with its path and size, how many times a model
// was asked and what it cost. It simply never reached the caller. This is that,
// threaded out — never reconstructed by reading what an agent wrote.
//
// Collection sits at the funnels every path already goes through — execToolAs
// for tools, runGear for a gear's files, writeFile for an agent's own, and
// recordUsage for model calls — so the orchestrator's loop, a delegated worker
// four levels down and an unattended run all feed one record, and a tool added
// later appears in it without anybody remembering to put it there.

// Record is what happened during one run. Every field is filled from the
// engine's own bookkeeping; nothing in it is written by a model, and nothing in
// it can be.
type Record struct {
	Tools []ToolRun  `json:"tools"`
	Files []FileMade `json:"files"`
	// Context is which documents fed this run, at which versions.
	//
	// It is the input most likely to have changed between a run that worked
	// and one that did not, and the record was silent about it: an operator
	// reading a bad delivery a week later could see which agent ran, which
	// tools it called and what it cost, and had no way to learn that somebody
	// had rewritten the house-style document that morning. A version is what
	// makes that answerable, and it costs one listing per run.
	Context    []ContextRead `json:"context"`
	ModelCalls int           `json:"model_calls"`
	Tokens     Tokens        `json:"tokens"`
}

// ContextRead is one document this run read, and the version it read.
//
// Version is empty when contextd could not be asked for the listing. That is
// deliberately distinguishable from a version: "we read this and cannot say
// which version" is a different fact from "we read version 4", and a record
// that guessed would be worse than one that admits it.
type ContextRead struct {
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
}

// ToolRun is one tool call, who made it, and how it ended. A gear appears here
// under its tool name — gear_unpack for the gear called unpack — because that is
// the name the model called and the name the engine dispatched.
//
// Agent and Depth are the point of the whole record, and they were missing from
// it for longer than they should have been. A run whose evidence says four tool
// calls happened does not answer the question this product exists to ask, which
// is which AGENT — and therefore which model — made them. The distinctive claim
// is one model per agent; a record that cannot tell an expensive agent's work
// from a cheap one's is a record about a black box. Both values were already in
// scope at the one call site (execToolAs) and were being logged there while
// being dropped here.
//
// Depth is the delegation distance from the agent the run started at: 0 for that
// agent, 1 for one it handed work to, and so on. It is what makes a record
// readable as a tree rather than a list, and it is free — the chain is already
// carried for the cycle check.
type ToolRun struct {
	Name  string `json:"name"`
	Agent string `json:"agent"`
	Depth int    `json:"depth"`
	OK    bool   `json:"ok"`
	Ms    int64  `json:"ms"`
	// Args is what the tool was called WITH, abridged. See abridgeArgs.
	//
	// Without it the record says a gear ran and stays silent on what it ran
	// on, which is the first thing anybody asks. "gear_deploy succeeded" and
	// "gear_deploy succeeded against production" are the same line here
	// otherwise, and the difference is the whole reason somebody opens the
	// record at all.
	Args json.RawMessage `json:"args,omitempty"`
}

// How much of a call's arguments the record keeps.
//
// A cap is not tidiness. write_file's arguments contain the entire body of the
// file, and a run that writes four files would put four documents in a record
// that is stored as one JSON column on every run row forever — the record would
// outgrow the thing it describes. So each string is cut and the whole object is
// cut, and both cuts SAY they happened rather than quietly returning a prefix
// that reads like the real value.
const (
	argValueMax = 200
	argTotalMax = 2000
)

// abridgeArgs shortens a tool call's arguments for the record.
//
// It keeps the shape — every key survives — and shortens the values, because
// which keys were passed is most of what a reader wants and is cheap, while the
// values are where a whole file can hide. A cut value ends in "… (N bytes)" so
// that nobody mistakes an abridgement for the argument.
//
// Anything that is not a JSON object is kept whole up to the total cap: a gear
// declares its own schema and may take an array or a bare string, and a record
// that dropped those would be silent about exactly the calls whose arguments
// are least predictable.
//
// NOTE ON SECRETS. Arguments are what a MODEL sent, and the model never holds a
// variable's value — those are injected into a gear's environment server-side
// and never appear in a schema, a prompt or a call. So there is nothing here to
// redact that was not already in the transcript the operator can read. If that
// ever stops being true, this is the funnel to redact at.
func abridgeArgs(raw string) json.RawMessage {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		// Not an object. Keep it whole if it fits, and say so if it does not.
		if len(raw) <= argTotalMax {
			if json.Valid([]byte(raw)) {
				return json.RawMessage(raw)
			}
			return nil
		}
		return mustJSON(fmt.Sprintf("… (%d bytes, not recorded)", len(raw)))
	}
	for k, v := range obj {
		var str string
		if err := json.Unmarshal(v, &str); err == nil {
			if len(str) > argValueMax {
				obj[k] = mustJSON(str[:argValueMax] + fmt.Sprintf("… (%d bytes)", len(str)))
			}
			continue
		}
		if len(v) > argValueMax {
			obj[k] = mustJSON(fmt.Sprintf("… (%d bytes)", len(v)))
		}
	}
	out, err := json.Marshal(obj)
	if err != nil || len(out) > argTotalMax {
		return mustJSON(fmt.Sprintf("%d arguments, too large to record", len(obj)))
	}
	return out
}

// mustJSON encodes a string that cannot fail to encode.
func mustJSON(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}

// FileMade is one file that appeared in the workspace during the run, in the
// workspace-relative form everything else uses, so the path in the record is
// the path a caller can fetch and the path the next gear can be handed.
type FileMade struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// Tokens is what the run cost, summed over every model call in it — the whole
// delegation tree, because the tree is one run.
type Tokens struct {
	In  int `json:"in"`
	Out int `json:"out"`
}

// MarshalJSON keeps the two lists present even when they are empty.
//
// An absent list and an empty list are different answers to the question this
// record exists for. `null` reads as "this field was not filled in"; `[]` says
// "nothing ran", which is the whole point — the empty tools list IS the answer
// to "did it do anything". Encoding it here rather than at each producer means
// no future caller can hand back a record that merely looks unpopulated.
func (r Record) MarshalJSON() ([]byte, error) {
	// shape drops the methods, so json.Marshal below does not call this again.
	type shape Record
	if r.Tools == nil {
		r.Tools = []ToolRun{}
	}
	if r.Files == nil {
		r.Files = []FileMade{}
	}
	if r.Context == nil {
		r.Context = []ContextRead{}
	}
	return json.Marshal(shape(r))
}

// RanGear reports whether a named gear ran and exited successfully. The tool
// prefix that turns a gear's name into the name a model calls belongs to this
// package, so anything checking an expectation against the record asks here
// rather than rebuilding the name and drifting from it.
func (r Record) RanGear(name string) bool {
	for _, t := range r.Tools {
		if t.OK && t.Name == gearToolPrefix+name {
			return true
		}
	}
	return false
}

// RanAnyGear reports whether ANY gear ran and exited successfully.
//
// It is what tells "no gear ran" apart from "a gear ran and printed nothing",
// which GearOutput cannot: both leave it empty. A task whose result is the
// gear's output rather than the agent's account of it has to know the
// difference — the first is a run that did not happen and the second is a run
// with an empty answer — and the tool list is the only place the difference
// exists. It lives here for the same reason RanGear does: the prefix is this
// package's, and a caller rebuilding it by hand is one rename from silence.
func (r Record) RanAnyGear() bool {
	for _, t := range r.Tools {
		if t.OK && strings.HasPrefix(t.Name, gearToolPrefix) {
			return true
		}
	}
	return false
}

// Outcome is everything one unattended run produced.
type Outcome struct {
	// Answer is the agent's final text — what a delivery used to return on its
	// own. It is still here, and it is no longer the only thing here.
	Answer string
	// Did is the record. It is filled on failure too: a run that unpacked an
	// archive, wrote four files and then fell over did that work, and the
	// moment somebody is paged is exactly the moment they need to know it.
	Did Record
	// GearOutput is the stdout of the last gear in this run that exited zero.
	//
	// It sits beside the record rather than in it because the record says what
	// HAPPENED and this is a candidate ANSWER: for a deterministic job the
	// model is a router, and its retelling of a gear's output is one paraphrase
	// away from being wrong. Empty when no gear ran, and also when the gear
	// printed nothing — Did.Tools is what tells those two apart.
	GearOutput string
}

// note applies f to the live record of this workspace's turn, under the lock
// that owns the turns map.
//
// Outside a tracked turn it changes nothing. That is deliberate: a tool running
// with no turn around it — a dry run from the catalog, a path that begins after
// endTurn — is not part of any run, and inventing a record for it would put
// work in the ledger under a job that did not do it.
func (e *Engine) note(wsID int64, f func(*Record)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ts, ok := e.turns[wsID]; ok {
		f(&ts.did)
	}
}

func (e *Engine) noteTool(wsID int64, name, agent string, depth int, ok bool, took time.Duration, args string) {
	e.note(wsID, func(r *Record) {
		r.Tools = append(r.Tools, ToolRun{
			Name: name, Agent: agent, Depth: depth, OK: ok, Ms: took.Milliseconds(),
			Args: abridgeArgs(args),
		})
	})
}

// noteFile records a file that now exists, replacing an earlier note of the
// same path rather than adding a second one.
//
// The record answers "what is there afterwards", not "how many write calls
// happened". A run that wrote summary.md twice made one file, and a record
// listing it twice satisfied `produces_files: 2` while one file existed — an
// expectation about the world met by an accident of activity, which is the
// precise shape of the untruth this record was added to end. The last write
// wins, because that is what is on disk.
func (e *Engine) noteFile(wsID int64, path string, bytes int64) {
	e.note(wsID, func(r *Record) {
		for i := range r.Files {
			if r.Files[i].Path == path {
				r.Files[i].Bytes = bytes
				return
			}
		}
		r.Files = append(r.Files, FileMade{Path: path, Bytes: bytes})
	})
}

// overBudget reports whether this turn has spent what it was allowed, and is
// checked BEFORE a model call rather than after.
//
// After would mean the call that crossed the line still happened and still cost
// money, which makes the ceiling a report rather than a bound. The whole point
// of copying the web-search quota's shape is that it stops work.
func (e *Engine) overBudget(wsID int64) (int64, int64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ts, ok := e.turns[wsID]
	if !ok || ts.budget <= 0 {
		return 0, 0, false
	}
	return ts.tokens, ts.budget, ts.tokens >= ts.budget
}

func (e *Engine) noteModelCall(wsID int64, u llm.Usage) {
	e.note(wsID, func(r *Record) {
		r.ModelCalls++
		// A provider that reported no usage contributes zero rather than a
		// guess. The call still counts: it happened, and how many times a run
		// went to a model is a fact independent of whether it was billed.
		r.Tokens.In += u.InputTokens
		r.Tokens.Out += u.OutputTokens
	})
	// And on the turn, where the ceiling reads it. Kept beside the record
	// rather than derived from it so that a budget check is one comparison
	// under the lock the turn state already needs.
	e.mu.Lock()
	if ts, ok := e.turns[wsID]; ok {
		ts.tokens += int64(u.InputTokens) + int64(u.OutputTokens)
	}
	e.mu.Unlock()
}

// noteGearOutput remembers a successful gear's stdout, replacing whatever the
// previous one left. Last one wins because a chain of gears ends at the one
// that produced the result.
func (e *Engine) noteGearOutput(wsID int64, stdout string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ts, ok := e.turns[wsID]; ok {
		ts.gearOut = stdout
	}
}

// outcome copies the run's record out of the live turn and pairs it with the
// answer. It must be called BEFORE endTurn — after it the state is gone, and a
// caller that read the record too late would report a run that did nothing.
//
// The slices are copied rather than shared so what a caller holds cannot change
// underneath it, which is the least a record can promise.
func (e *Engine) outcome(wsID int64, answer string) Outcome {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Outcome{Answer: answer}
	ts, ok := e.turns[wsID]
	if !ok {
		return out
	}
	out.Did = ts.did
	out.Did.Tools = append([]ToolRun(nil), ts.did.Tools...)
	out.Did.Files = append([]FileMade(nil), ts.did.Files...)
	out.Did.Context = append([]ContextRead(nil), ts.did.Context...)
	out.GearOutput = ts.gearOut
	return out
}

// DistinctFiles counts the files that exist, not the writes that happened.
//
// A run that wrote "summary.md" twice made one file, and len(Files) says two.
// That is the difference between an expectation about the world and one about
// activity: "produces_files: 2" is an operator saying two files should be there
// afterwards, and a run that overwrote one of them and answered 200 is exactly
// the kind of satisfied-but-untrue that this whole feature exists to end.
//
// The last write wins for the size, because that is what is on disk.
func (r Record) DistinctFiles() int {
	if len(r.Files) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(r.Files))
	for _, f := range r.Files {
		seen[f.Path] = struct{}{}
	}
	return len(seen)
}

// guardBudget refuses before a model is called, or lets it through.
//
// THERE ARE TWO PLACES IN THIS ENGINE THAT CALL A MODEL — modelTurn, which the
// operator's orchestrator loop uses, and runAgent, which every delegated and
// every unattended run uses — and the first version of this ceiling was in one
// of them. The delivery path went straight past it and spent sixteen
// iterations against a ten-token budget, which the test said out loud.
//
// Both call it now. A third call site added later would miss it in the same
// way, so it is worth saying: the rule is that nothing calls Chat without
// coming through here first.
func (e *Engine) guardBudget(wsID int64) error {
	spent, budget, over := e.overBudget(wsID)
	if !over {
		return nil
	}
	slog.Warn("run stopped by its token budget", "workspace_id", wsID, "spent", spent, "budget", budget)
	return fmt.Errorf("%w: %d tokens of %d", ErrBudget, spent, budget)
}
