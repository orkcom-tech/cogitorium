package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

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
		if _, aErr := s.schedules.Advance(context.WithoutCancel(ctx),
			schedule.Schedule{ID: sc.ID, NextAt: next}, next, schedule.OutcomeFailed, nil); aErr != nil {
			slog.Error("could not record a failed firing", "schedule", sc.ID, "err", aErr)
		}
		return
	}
	slog.Info("schedule fired", "schedule", sc.ID, "name", sc.Name, "unit", unitID, "next_at", next.UTC())
}

// enqueueScheduled turns a firing into the same unit an HTTP delivery produces.
//
// The same unit deliberately: a scheduled job and a delivered one are the same
// work with a different trigger, and two paths into the engine would be two
// places for the record, the expectation and the callback to diverge.
func (s *Server) enqueueScheduled(ctx context.Context, sc schedule.Schedule) (int64, error) {
	task, err := s.inlets.GetTask(ctx, sc.TaskID)
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

	runID, err := s.inlets.Accept(ctx, sc.WorkspaceID, in.ID, in.Address, task.Name, task.AgentName)
	if err != nil {
		return 0, err
	}
	prompt := jsonDeliveryPrompt(task, in.Address, []byte(sc.Payload))
	if err := s.inlets.Queued(ctx, runID, agent.ID, int64(len(sc.Payload)), ""); err != nil {
		return 0, err
	}
	unit, err := s.queue.Enqueue(ctx, work.Unit{
		Kind: work.KindDelivery, WorkspaceID: sc.WorkspaceID, Lane: work.Lane(sc.WorkspaceID),
		Args: deliveryArgsJSON(task, in.Address, prompt), RunID: &runID,
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
