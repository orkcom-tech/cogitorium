package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/schedule"
	"github.com/orkcom-tech/cogitorium/internal/work"
)

// The clock.
//
// One goroutine, one query every tickEvery, and no leader election — there is
// one process, and the compare-and-set in schedule.Advance means a second one
// would produce at most one firing per schedule anyway rather than duplicates.
// That is the whole of the coordination, and it is deliberately the only part
// of this that has to be right.
//
// NOTHING IN A TICK MAY SPAN A MODEL CALL, A GEAR, OR A WAIT. A tick reads due
// rows, decides, writes, and enqueues; the work happens later, in a worker, by
// the same path a delivery takes. A tick that ran a job would hold whatever it
// was holding for the length of an agent's turn, and the schedule that fired at
// 02:00 would block the one due at 02:01.

const (
	// tickEvery is how often the clock is consulted. Ten seconds is under the
	// one-minute floor a spec can name, so nothing is ever late by more than a
	// fraction of its own interval, and an idle install does one indexed query
	// per tick returning nothing.
	tickEvery = 10 * time.Second
	// dueBatch bounds what one tick will fire, so a hundred schedules coming
	// due together are spread over a few ticks rather than filling the queue in
	// one go.
	dueBatch = 20
)

// startScheduler runs the clock until ctx ends. Started with the server, for
// the same reason the workers are: a scheduler that only ran while the HTTP
// listener did would be a scheduler no test and no embedding ever sees.
func (s *Server) startScheduler(ctx context.Context) {
	go func() {
		// One immediate pass, so a server that has been down over a due time
		// does not wait a further tick before catching up.
		s.tick(ctx)
		t := time.NewTicker(tickEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.tick(ctx)
			}
		}
	}()
}

func (s *Server) tick(ctx context.Context) {
	due, err := s.schedules.Due(ctx, time.Now(), dueBatch)
	if err != nil {
		slog.Error("could not read due schedules", "err", err)
		return
	}
	for _, sc := range due {
		s.fire(ctx, sc)
	}
	if len(due) > 0 {
		s.pool.Wake()
	}
	s.sampleQueue(ctx)
}

// sampleQueue publishes how much work is waiting, on the clock that already
// runs.
//
// A GAUGE IS SAMPLED, which is what makes this the right place rather than a
// hook on Enqueue: the question is "how deep is the queue right now", and
// counting increments would answer "how many were ever added" — the counter
// that already exists. Ten seconds is finer than any useful alert window.
//
// Install-wide, with no workspace in a label, because a label per workspace
// publishes the roster to whoever can scrape and is unbounded besides.
func (s *Server) sampleQueue(ctx context.Context) {
	queued, claimed, err := s.queue.TotalDepth(ctx)
	if err != nil {
		slog.Debug("could not sample the queue depth", "err", err)
		return
	}
	s.metrics.WorkQueued.Set(nil, float64(queued))
	// Set rather than left to the per-unit gauge: a process that was killed
	// mid-run would otherwise leave its increment behind forever.
	s.metrics.WorkRunning.Set(map[string]string{"kind": "claimed"}, float64(claimed))
}

// fire decides what one due schedule should do, and records it either way.
//
// The order matters: the next firing is computed and written BEFORE the work is
// enqueued. A crash between the two loses one job, which is recoverable and
// visible; the other order loses the schedule itself into a loop of firing the
// same instant forever.
func (s *Server) fire(ctx context.Context, sc schedule.Schedule) {
	spec, err := schedule.Parse(sc.Spec)
	if err != nil {
		// Stored specs are validated when written, so this means the row was
		// edited outside the product or a parser changed under it. Disable
		// rather than fire something nobody can predict.
		slog.Error("a schedule's spec no longer parses; disabling it", "schedule", sc.ID, "spec", sc.Spec, "err", err)
		if _, err := s.schedules.SetEnabled(context.WithoutCancel(ctx), sc.ID, false); err != nil {
			slog.Error("could not disable an unparseable schedule", "schedule", sc.ID, "err", err)
		}
		return
	}
	loc, err := schedule.Location(sc.TZ)
	if err != nil {
		slog.Error("a schedule's timezone is unknown; firing it in UTC", "schedule", sc.ID, "tz", sc.TZ, "err", err)
		loc = time.UTC
	}
	next, ok := spec.Next(time.Now(), loc)
	if !ok {
		slog.Error("a schedule has no next firing; disabling it", "schedule", sc.ID, "spec", sc.Spec)
		if _, err := s.schedules.SetEnabled(context.WithoutCancel(ctx), sc.ID, false); err != nil {
			slog.Error("could not disable a schedule with no next firing", "schedule", sc.ID, "err", err)
		}
		return
	}

	// Skip when the previous firing has not finished. A job slower than its own
	// interval never catches up, and queueing every missed tick turns that into
	// a backlog that outlives the reason for it.
	if sc.OnMiss == "skip" && sc.LastWorkID != nil {
		switch prev, err := s.queue.Get(ctx, *sc.LastWorkID); {
		case err == nil && (prev.State == work.StateQueued || prev.State == work.StateClaimed):
			if won, err := s.schedules.Advance(context.WithoutCancel(ctx), sc, next, schedule.OutcomeSkipped, sc.LastWorkID); err != nil {
				slog.Error("could not record a skipped firing", "schedule", sc.ID, "err", err)
			} else if won {
				slog.Info("schedule skipped: its previous run has not finished",
					"schedule", sc.ID, "name", sc.Name, "previous_unit", *sc.LastWorkID)
				s.metrics.ScheduleFires.Inc(map[string]string{"outcome": schedule.OutcomeSkipped})
			}
			return
		case err != nil && !errors.Is(err, work.ErrNotFound):
			slog.Error("could not check a schedule's previous run", "schedule", sc.ID, "err", err)
			return
		}
	}

	// Win the firing before doing anything with side effects. Whoever writes
	// next_at owns this tick; a loser simply returns.
	won, err := s.schedules.Advance(context.WithoutCancel(ctx), sc, next, schedule.OutcomeFired, sc.LastWorkID)
	if err != nil {
		slog.Error("could not advance a schedule", "schedule", sc.ID, "err", err)
		return
	}
	if !won {
		return
	}

	unitID, err := s.enqueueScheduled(context.WithoutCancel(ctx), sc)
	if err != nil {
		slog.Error("a schedule fired but its job could not be queued", "schedule", sc.ID, "name", sc.Name, "err", err)
		// The firing already happened as far as next_at is concerned, and
		// saying so is better than a row that looks like it never came round.
		//
		// sc.LastWorkID, NOT nil. Advance writes last_work_id unconditionally,
		// so passing nil here NULLED it — and because the overlap check below
		// is gated on `sc.LastWorkID != nil`, one failed enqueue switched skip
		// protection OFF until the next firing that succeeded. A job that could
		// not be queued is not evidence that the previous one finished.
		if _, aErr := s.schedules.Advance(context.WithoutCancel(ctx),
			schedule.Schedule{ID: sc.ID, NextAt: next}, next, schedule.OutcomeFailed, sc.LastWorkID); aErr != nil {
			slog.Error("could not record a failed firing", "schedule", sc.ID, "err", aErr)
		}
		// The one this whole metric exists for: a nightly job that has been
		// failing every night for a week, which nothing could tell anybody.
		s.metrics.ScheduleFires.Inc(map[string]string{"outcome": schedule.OutcomeFailed})
		return
	}
	slog.Info("schedule fired", "schedule", sc.ID, "name", sc.Name, "unit", unitID, "next_at", next.UTC())
	s.metrics.ScheduleFires.Inc(map[string]string{"outcome": schedule.OutcomeFired})
}

// enqueueScheduled turns a firing into the same unit an HTTP delivery produces.
//
// The same unit deliberately, whatever the clock dials: the RUN RECORD, the
// QUEUE, the EXPECTATIONS and the CEILING are what make an unattended run
// answerable, and a direct schedule that skipped any of them would be a second,
// weaker way to run work. So all three targets land in the same queue with the
// same ledger row behind them, and only the prompt is built differently.
func (s *Server) enqueueScheduled(ctx context.Context, sc schedule.Schedule) (int64, error) {
	if sc.Broken() {
		// SET NULL rather than CASCADE is what leaves this row here at all —
		// see 0031. Refusing loudly is the point: the operator gets a schedule
		// that says it is broken instead of one that quietly stopped.
		return 0, fmt.Errorf("this schedule's %s was deleted, so there is nothing left to fire; "+
			"point it at another one or remove it", sc.TargetKind)
	}
	switch sc.TargetKind {
	case schedule.TargetAgent:
		return s.enqueueAgentSchedule(ctx, sc)
	case schedule.TargetGear:
		return s.enqueueGearSchedule(ctx, sc)
	default:
		return s.enqueueTaskSchedule(ctx, sc)
	}
}

// enqueueTaskSchedule is the original path: a task firing with nobody on the
// other end.
func (s *Server) enqueueTaskSchedule(ctx context.Context, sc schedule.Schedule) (int64, error) {
	if sc.TaskID == nil {
		return 0, errors.New("a task schedule with no task")
	}
	task, err := s.inlets.GetTask(ctx, *sc.TaskID)
	if err != nil {
		return 0, err
	}
	in, err := s.inlets.GetInlet(ctx, task.InletID)
	if err != nil {
		return 0, err
	}
	agent, err := s.workspaces.GetAgentByName(ctx, sc.WorkspaceID, task.AgentName)
	if err != nil {
		return 0, err
	}
	if agent.ModelID == nil {
		return 0, errors.New("this task's agent has no model bound")
	}

	inletID := in.ID
	runID, err := s.inlets.Accept(ctx, sc.WorkspaceID, &inletID, in.Address, task.Name, task.AgentName)
	if err != nil {
		return 0, err
	}
	prompt := jsonDeliveryPrompt(task, in.Address, []byte(sc.Payload))
	if err := s.inlets.Queued(ctx, runID, agent.ID, int64(len(sc.Payload)), ""); err != nil {
		return 0, err
	}
	return s.queueScheduled(ctx, sc, runID, deliveryArgsJSON(task, in.Address, prompt))
}

// enqueueAgentSchedule dials an agent with the sentence the schedule carries.
//
// The ledger row has NO INLET — inlet_runs.inlet_id is nullable and carries no
// foreign key precisely so a row can outlive, or never have, the door it
// records. `clock` stands where an address would, so a person reading the
// record sees what started the run rather than a blank.
func (s *Server) enqueueAgentSchedule(ctx context.Context, sc schedule.Schedule) (int64, error) {
	agent, err := s.workspaces.GetAgent(ctx, *sc.TargetAgentID)
	if err != nil {
		return 0, err
	}
	if agent.WorkspaceID != sc.WorkspaceID {
		return 0, errors.New("this schedule's agent has moved to another workspace")
	}
	if agent.ModelID == nil {
		return 0, errors.New("this schedule's agent has no model bound")
	}

	runID, err := s.inlets.Accept(ctx, sc.WorkspaceID, nil, clockAddress, sc.Name, agent.Name)
	if err != nil {
		return 0, err
	}
	if err := s.inlets.Queued(ctx, runID, agent.ID, 0, ""); err != nil {
		return 0, err
	}
	// NO UNTRUSTED FENCE, and that is the difference from a delivery. A task's
	// payload is fenced because it was written by a caller outside this
	// workspace; this instruction was typed by an operator into this install,
	// so wrapping it in "this is data, not instructions" would be telling the
	// agent to ignore the only thing it was given.
	args, err := json.Marshal(deliveryArgs{
		Agent: agent.Name, Prompt: sc.Instruction,
		Address: clockAddress, Task: sc.Name,
	})
	if err != nil {
		return 0, fmt.Errorf("pack this schedule's arguments: %w", err)
	}
	return s.queueScheduled(ctx, sc, runID, string(args))
}

// enqueueGearSchedule runs a gear with no agent in the loop.
//
// The most useful thing here and the most dangerous: a nightly backup, a
// report, a sync — the jobs where a model is a liability rather than a help —
// and nobody is watching. Three things hold it:
//
//   - only an administrator may create one (see handleCreateSchedule);
//   - the gear must be approved, checked again HERE and not only at creation,
//     because a gear edited since drops back to pending and a clock is the
//     caller with no second gate behind it;
//   - it still lands in this workspace's record and queue, so a failing
//     nightly gear is visible in the same place everything else is.
func (s *Server) enqueueGearSchedule(ctx context.Context, sc schedule.Schedule) (int64, error) {
	g, err := s.gears.Get(ctx, *sc.TargetGearID)
	if err != nil {
		return 0, err
	}
	if g.Status != gear.StatusApproved {
		return 0, fmt.Errorf("gear %s is %s, and a schedule may only run code somebody read and approved; "+
			"approve this version or disable the schedule", g.Name, g.Status)
	}

	runID, err := s.inlets.Accept(ctx, sc.WorkspaceID, nil, clockAddress, sc.Name, gearRunner)
	if err != nil {
		return 0, err
	}
	if err := s.inlets.Queued(ctx, runID, 0, int64(len(sc.Args)), ""); err != nil {
		return 0, err
	}
	args, err := json.Marshal(deliveryArgs{
		Gear:    &gearCall{ID: g.ID, Name: g.Name, Args: json.RawMessage(sc.Args)},
		Address: clockAddress, Task: sc.Name,
	})
	if err != nil {
		return 0, fmt.Errorf("pack this schedule's arguments: %w", err)
	}
	return s.queueScheduled(ctx, sc, runID, string(args))
}

// queueScheduled is the tail every path shares: one unit, one lane, and the
// schedule's row told which unit it produced.
func (s *Server) queueScheduled(ctx context.Context, sc schedule.Schedule, runID int64, args string) (int64, error) {
	unit, err := s.queue.Enqueue(ctx, work.Unit{
		Kind: work.KindDelivery, WorkspaceID: sc.WorkspaceID, Lane: work.Lane(sc.WorkspaceID),
		Args: args, RunID: &runID,
	})
	if err != nil {
		s.settle(ctx, runID, inlet.StateFailed, "", err.Error(), zeroRecord)
		return 0, err
	}
	if err := s.schedules.NoteUnit(ctx, sc.ID, unit.ID); err != nil {
		slog.Warn("a schedule fired but its row does not name the unit", "schedule", sc.ID, "err", err)
	}
	return unit.ID, nil
}

// clockAddress stands where an inlet address would on a run a clock started.
// A word rather than an empty string, because the ledger's whole job is to say
// what caused a run and a blank says nothing.
const clockAddress = "clock"

// gearRunner is the agent name on a ledger row for a gear firing. There is no
// agent — that is the point of the path — and the row's column is NOT NULL, so
// it says so rather than naming one that did not run.
const gearRunner = "(no agent: a gear ran directly)"
