package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/gear"

	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/metrics"
	"github.com/orkcom-tech/cogitorium/internal/work"
)

// What a queued delivery carries with it.
//
// Everything the run needs travels in the unit rather than being read back from
// the inlet when the worker picks it up. A task can be edited or deleted while
// its deliveries wait, and a job that ran against a different instruction from
// the one it was accepted under would be the worst kind of surprise: the ledger
// row would name a task whose text no longer matches what the agent was told.
type deliveryArgs struct {
	Agent  string          `json:"agent"`
	Prompt string          `json:"prompt"`
	Expect json.RawMessage `json:"expect,omitempty"`
	// Kept for the log line and for a person reading the queue, neither of
	// which can look up an inlet that has since been deleted.
	Address string `json:"address"`
	Task    string `json:"task"`

	// Gear is set instead of Agent when a clock dials a gear with no agent in
	// the loop.
	//
	// A FIELD IN THE ARGS RATHER THAN A NEW work.kind, deliberately. The queue's
	// kind column is `CHECK (kind IN ('delivery','chat','callback'))` and 0022
	// says in its own comment that adding a value later means rebuilding a table
	// that by then holds rows. The args blob exists precisely so a unit can
	// carry everything it needs, and "a delivery that runs a gear" is a delivery
	// — same lane, same ledger row, same queue, same ceiling.
	Gear *gearCall `json:"gear,omitempty"`
}

// gearCall is a gear firing carried in a unit.
//
// The NAME travels beside the id for the same reason the address and task do:
// whoever reads the queue or the log cannot look up a gear that has since been
// deleted, and "gear 41" is not something anybody can act on.
type gearCall struct {
	ID   int64           `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// runDelivery is what a worker does with a delivery unit: run the agent, hold
// the result against what the task declared success to be, and settle the
// ledger row.
//
// It returns an error only when the QUEUE should record one. The delivery's own
// outcome — including its failures — belongs to the ledger row, which is what
// the caller reads. A run that failed is a unit that did its job.
func (s *Server) runDelivery(ctx context.Context, u work.Unit) error {
	if u.RunID == nil {
		return errors.New("a delivery unit with no ledger row: nothing could report what happened")
	}
	runID := *u.RunID
	// The ledger is written whatever happens to the worker's context, for the
	// same reason it always was: a row left saying "running" forever is exactly
	// what this table exists to prevent.
	ledgerCtx := context.WithoutCancel(ctx)

	var args deliveryArgs
	if err := json.Unmarshal([]byte(u.Args), &args); err != nil {
		cause := fmt.Errorf("this delivery's stored arguments could not be read: %w", err)
		s.settle(ledgerCtx, runID, inlet.StateFailed, "", cause.Error(), engine.Record{})
		return cause
	}

	if args.Gear != nil {
		return s.runScheduledGear(ctx, ledgerCtx, runID, args)
	}

	if err := s.inlets.Start(ledgerCtx, runID); err != nil {
		// The row is not where it was expected — cancelled while it waited, or
		// already settled. Either way there is nothing left to run, and running
		// it anyway would overwrite a decision somebody already made.
		slog.Info("a queued delivery was no longer waiting when its turn came", "run_id", runID, "err", err)
		return nil
	}

	// Tell the engine which unit this run belongs to, so every durable row it
	// writes carries the correlation those tables have never had.
	s.engine.SetWorkFor(u.WorkspaceID, u.ID)
	out, err := s.engine.RunUnattended(ctx, u.WorkspaceID, args.Agent, args.Prompt)
	if err != nil {
		// A run that never began leaves a file nobody will ever read. What
		// reaches here is narrower than it used to be — a modelless agent is
		// refused at the door now, and the worker holds the lane so it cannot
		// meet a busy workspace — but the case that remains is real: the task's
		// agent can be deleted while its deliveries wait.
		//
		// Deliberately NOT every failure: a run that read the file and then
		// fell over used it, and whoever decides what to do next may want to
		// see what arrived.
		if neverBegan(err) {
			if run, getErr := s.inlets.GetRun(ledgerCtx, runID); getErr == nil && run.PayloadPath != "" {
				s.reclaimPayload(ledgerCtx, u.WorkspaceID, runID, run.PayloadPath)
			}
		}
		s.failedRun(ledgerCtx, runID, err, out.Did)
		return nil
	}

	var expect inlet.Expect
	if len(args.Expect) > 0 {
		if err := json.Unmarshal(args.Expect, &expect); err != nil {
			slog.Warn("a delivery's stored expectation could not be read; the run is judged on nothing",
				"run_id", runID, "err", err)
		}
	}
	// A run stopped by a ceiling is not a broken job. It gets its own state so
	// a caller can tell "you told me to stop this" from "it went wrong", and
	// does not retry the one that must not be retried.
	v := judge(expect, out)
	if v.refused() {
		slog.Warn("inlet run refused by what its task requires", "run_id", runID, "state", v.state,
			"address", args.Address, "task", args.Task, "tools", len(out.Did.Tools),
			"files", len(out.Did.Files), "err", v.err)
		s.settle(ledgerCtx, runID, v.state, "", v.err.Error(), out.Did)
		return nil
	}
	s.settle(ledgerCtx, runID, v.state, v.result, "", out.Did)
	return nil
}

// failedRun settles a delivery that produced no answer, keeping the three kinds
// of failure apart because they want three different reactions: wait and retry,
// fix the workspace, or stop.
//
// The record goes in even here, and this is the case it matters most for: a run
// that unpacked an archive and wrote four files before falling over did that
// work, and whoever decides whether to retry needs to know what is already on
// disk.
func (s *Server) failedRun(ctx context.Context, runID int64, cause error, did engine.Record) {
	state := inlet.StateFailed
	switch {
	case errors.Is(cause, engine.ErrBudget):
		// Not a broken job: the operator drew this line. Saying so in the state
		// rather than only in the error text is what lets a caller stop instead
		// of retrying — and retrying a run that was deliberately stopped is how
		// a ceiling turns into a bill.
		state = inlet.StateRefusedBudget
	case errors.Is(ctx.Err(), context.Canceled):
		state = inlet.StateInterrupted
	}
	slog.Error("inlet run failed", "run_id", runID, "state", state,
		"tools", len(did.Tools), "files", len(did.Files), "err", cause)
	s.settle(ctx, runID, state, "", cause.Error(), did)
}

// runWork is what the pool calls, and the one place that decides what a unit
// means. A kind with no runner is a bug rather than a no-op: the unit would
// otherwise settle successfully having done nothing, which is the exact shape
// of failure this whole ledger exists to make impossible.
func (s *Server) runWork(ctx context.Context, u work.Unit) error {
	// Counted around the whole unit rather than inside each runner, so a kind
	// that grows a third runner is measured without anybody remembering to.
	s.metrics.WorkRunning.Add(map[string]string{"kind": u.Kind}, 1)
	defer s.metrics.WorkRunning.Add(map[string]string{"kind": u.Kind}, -1)

	err := s.runWorkOf(ctx, u)
	s.metrics.WorkUnits.Inc(map[string]string{"kind": u.Kind, "outcome": metrics.Outcome(err)})
	return err
}

func (s *Server) runWorkOf(ctx context.Context, u work.Unit) error {
	switch u.Kind {
	case work.KindDelivery:
		return s.runDelivery(ctx, u)
	case work.KindCallback:
		return s.runCallback(ctx, u)
	case work.KindPlugin:
		return s.runPluginTask(ctx, u)
	}
	return fmt.Errorf("nothing in this server knows how to run a %q unit", u.Kind)
}

// runPluginTask runs work a plugin scheduled for itself.
//
// max_attempts is 1 like everything else on this queue, and for the same
// reason: re-running work that may already have sent something outward is a
// second execution nobody asked for, not a recovery. A plugin that wants a
// retry enqueues one, which is a decision its author made rather than one this
// server made for them.
func (s *Server) runPluginTask(ctx context.Context, u work.Unit) error {
	var args struct {
		Plugin string          `json:"plugin"`
		Export string          `json:"export"`
		Args   json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(u.Args), &args); err != nil {
		return fmt.Errorf("this unit's arguments are not readable: %w", err)
	}
	if args.Plugin == "" || args.Export == "" {
		return fmt.Errorf("a plugin unit needs a plugin and an export")
	}

	// A plugin that was disabled or removed between enqueue and now. Not an
	// error worth alarming about: the operator switched it off, and the unit
	// dying quietly with a reason is what should happen.
	if s.backends == nil {
		return fmt.Errorf("this install runs no plugin backends")
	}
	slog.Info("running a plugin's own task", "plugin", args.Plugin, "export", args.Export, "unit", u.ID)
	return s.backends.invoke(ctx, args.Plugin, args.Export, args.Args)
}

// runScheduledGear is a gear firing with nobody watching.
//
// It goes through the SAME ledger row a delivery gets — accepted, running,
// settled — because "did last night's backup run" has to be answerable in the
// same place as every other question about this workspace. What it does not do
// is call a model: there is no agent, and that is the whole point of the path.
//
// The approval is re-checked by the executor rather than trusted from creation
// time. A gear edited since its schedule was written drops back to pending, and
// gear.ErrNotApproved is what stops it — the clock is the caller here and there
// is no second gate behind it.
func (s *Server) runScheduledGear(ctx, ledgerCtx context.Context, runID int64, args deliveryArgs) error {
	if err := s.inlets.Start(ledgerCtx, runID); err != nil {
		slog.Info("a queued gear firing was no longer waiting when its turn came", "run_id", runID, "err", err)
		return nil
	}

	g, err := s.gears.Get(ctx, args.Gear.ID)
	if err != nil {
		// Deleted between the firing and the worker picking it up. Said
		// plainly, because the schedule is still there and still pointing at
		// nothing.
		cause := fmt.Errorf("gear %s (%d) is gone, so this schedule has nothing to run: %w",
			args.Gear.Name, args.Gear.ID, err)
		s.settle(ledgerCtx, runID, inlet.StateFailed, "", cause.Error(), engine.Record{})
		return nil
	}

	body := "{}"
	if len(args.Gear.Args) > 0 {
		body = string(args.Gear.Args)
	}
	// No Caller fields: not a dry run, and not on behalf of an agent. The
	// executor's own check refuses a pending or disabled gear here, which is
	// what makes the approval gate hold on a path with no human in it.
	started := time.Now()
	res, runErr := s.gearExec.Run(ctx, g, body, gear.Caller{})
	elapsed := time.Since(started).Milliseconds()
	s.metrics.GearSeconds.Observe(nil, time.Since(started).Seconds())

	// The record is built from the executor's own result rather than from
	// anything the gear said about itself, exactly as a delegated tool call is.
	// The FILES matter as much as the exit code here: a nightly job that wrote
	// four files and then fell over did that work, and whoever decides whether
	// to run it again needs to know what is already on disk.
	did := engine.Record{
		Tools: []engine.ToolRun{{
			Name: g.Name, OK: runErr == nil && res.ExitCode == 0 && !res.TimedOut, Ms: elapsed,
		}},
		Files: []engine.FileMade{},
	}
	for _, f := range res.Produced {
		did.Files = append(did.Files, engine.FileMade{Path: f.Path, Bytes: f.Bytes})
	}

	s.metrics.GearRuns.Inc(map[string]string{"outcome": gearOutcome(runErr, res)})

	switch {
	case errors.Is(runErr, gear.ErrNotApproved):
		// Not a broken job: somebody edited this gear and it has not been read
		// since. Its own state, so a reader is not sent to look for a bug.
		slog.Warn("a scheduled gear was refused: it is not approved",
			"run_id", runID, "gear", g.Name, "status", g.Status)
		s.settle(ledgerCtx, runID, inlet.StateRefusedExpectation, "", runErr.Error(), did)
	case runErr != nil:
		slog.Error("a scheduled gear failed", "run_id", runID, "gear", g.Name, "err", runErr)
		s.settle(ledgerCtx, runID, inlet.StateFailed, "", runErr.Error(), did)
	case res.ExitCode != 0 || res.TimedOut:
		// A non-zero exit IS the failure, and the stderr is what somebody
		// reading this at nine in the morning actually needs.
		why := fmt.Sprintf("exit %d", res.ExitCode)
		if res.TimedOut {
			why = "timed out"
		}
		if trimmed := strings.TrimSpace(res.Stderr); trimmed != "" {
			why += ": " + trimmed
		}
		slog.Error("a scheduled gear ended badly", "run_id", runID, "gear", g.Name,
			"exit_code", res.ExitCode, "timed_out", res.TimedOut)
		s.settle(ledgerCtx, runID, inlet.StateFailed, res.Stdout, why, did)
	default:
		slog.Info("a scheduled gear ran", "run_id", runID, "gear", g.Name,
			"ms", elapsed, "files", len(did.Files))
		s.settle(ledgerCtx, runID, inlet.StateCompleted, res.Stdout, "", did)
	}
	return nil
}

// gearOutcome is the one label a dashboard reads, and it keeps "refused" apart
// from "broke" for the same reason the ledger states do: a gear that was not
// approved is a decision somebody has to make, and a gear that exited 1 is a
// bug somebody has to fix. Alerting on them together pages the wrong person.
func gearOutcome(err error, res gear.Result) string {
	switch {
	case errors.Is(err, gear.ErrNotApproved):
		return "refused"
	case err != nil:
		return "failed"
	case res.TimedOut:
		return "timed_out"
	case res.ExitCode != 0:
		return "nonzero_exit"
	default:
		return "ok"
	}
}
