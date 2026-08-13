package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
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
	switch u.Kind {
	case work.KindDelivery:
		return s.runDelivery(ctx, u)
	case work.KindCallback:
		return s.runCallback(ctx, u)
	}
	return fmt.Errorf("nothing in this server knows how to run a %q unit", u.Kind)
}
