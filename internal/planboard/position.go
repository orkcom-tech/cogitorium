package planboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Bind attaches a plan. agentID nil binds it to the whole workspace, and then
// every agent in that workspace advances one shared position.
func (s *Store) Bind(ctx context.Context, planboardID, wsID int64, agentID *int64) (Binding, error) {
	var agent any
	if agentID != nil {
		agent = *agentID
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO planboard_bindings (planboard_id, workspace_id, agent_id, created_at) VALUES (?, ?, ?, ?)`,
		planboardID, wsID, agent, now())
	// Binding twice is what a person does when they are not sure whether they
	// already did. It is the same attachment either way, so say yes.
	if err != nil && !isDuplicate(err) {
		return Binding{}, fmt.Errorf("bind planboard %d to workspace %d: %w", planboardID, wsID, err)
	}
	return s.binding(ctx, planboardID, wsID, agentID)
}

func isDuplicate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

const bindingCols = `
	SELECT b.id, b.planboard_id, p.name, b.workspace_id, b.agent_id, COALESCE(a.name, '')
	FROM planboard_bindings b
	JOIN planboards p ON p.id = b.planboard_id
	LEFT JOIN agents a ON a.id = b.agent_id`

func scanBinding(row interface{ Scan(...any) error }) (Binding, error) {
	var b Binding
	if err := row.Scan(&b.ID, &b.PlanboardID, &b.Planboard, &b.WorkspaceID, &b.AgentID, &b.Agent); err != nil {
		return Binding{}, err
	}
	return b, nil
}

func (s *Store) binding(ctx context.Context, planboardID, wsID int64, agentID *int64) (Binding, error) {
	q := bindingCols + ` WHERE b.planboard_id = ? AND b.workspace_id = ? AND b.agent_id IS NULL`
	args := []any{planboardID, wsID}
	if agentID != nil {
		q = bindingCols + ` WHERE b.planboard_id = ? AND b.workspace_id = ? AND b.agent_id = ?`
		args = append(args, *agentID)
	}
	b, err := scanBinding(s.db.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		// By name, because the person reading this is looking at a name. An id
		// is this package's business and means nothing on a screen.
		var name string
		if lookupErr := s.db.QueryRowContext(ctx, `SELECT name FROM planboards WHERE id = ?`, planboardID).Scan(&name); lookupErr != nil || name == "" {
			return Binding{}, fmt.Errorf("that plan is not attached here: %w", ErrNotFound)
		}
		return Binding{}, fmt.Errorf("%q is not attached here: %w", name, ErrNotFound)
	}
	return b, err
}

// Unbind detaches the plan and forgets where it had got to. Re-binding starts
// the plan again, which is what detaching then attaching plainly means.
func (s *Store) Unbind(ctx context.Context, planboardID, wsID int64, agentID *int64) error {
	b, err := s.binding(ctx, planboardID, wsID, agentID)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM planboard_bindings WHERE id = ?`, b.ID); err != nil {
		return fmt.Errorf("unbind planboard %d: %w", planboardID, err)
	}
	slog.Info("planboard unbound", "planboard", b.Planboard, "workspace_id", wsID)
	return nil
}

// Bindings lists what is attached in a workspace, agent-bound and
// workspace-wide alike.
func (s *Store) Bindings(ctx context.Context, wsID int64) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx, bindingCols+` WHERE b.workspace_id = ? ORDER BY p.name`, wsID)
	if err != nil {
		return nil, fmt.Errorf("bindings in workspace %d: %w", wsID, err)
	}
	defer rows.Close()
	out := []Binding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// state reads a binding's position, creating it at step one on first sight.
// First use and a fresh start are the same thing, so there is no separate
// call to begin a plan.
func (s *Store) state(ctx context.Context, bindingID int64) (State, error) {
	var st State
	err := s.db.QueryRowContext(ctx,
		`SELECT step, cycle, blocked_note, updated_at FROM planboard_state WHERE binding_id = ?`, bindingID).
		Scan(&st.Step, &st.Cycle, &st.BlockedNote, &st.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO planboard_state (binding_id, step, cycle, updated_at) VALUES (?, 1, 0, ?)`,
			bindingID, now()); err != nil {
			return State{}, fmt.Errorf("start planboard binding %d: %w", bindingID, err)
		}
		return State{Step: 1, Cycle: 0, UpdatedAt: now()}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("position of planboard binding %d: %w", bindingID, err)
	}
	return st, nil
}

// State is the position, for a reader that already has a binding.
func (s *Store) State(ctx context.Context, bindingID int64) (State, error) {
	return s.state(ctx, bindingID)
}

// Active is what this agent works by right now: the plans bound to it, plus
// the plans bound to its whole workspace, each with the one step in front of
// it.
//
// Ordered by name so a run that walks two plans walks them in the same order
// every time. Two plans is unusual and legal; the ordering matters because
// unusual-and-legal is exactly where nondeterminism hides.
func (s *Store) Active(ctx context.Context, wsID, agentID int64) ([]Assignment, error) {
	rows, err := s.db.QueryContext(ctx, bindingCols+`
		WHERE b.workspace_id = ? AND (b.agent_id IS NULL OR b.agent_id = ?)
		ORDER BY p.name`, wsID, agentID)
	if err != nil {
		return nil, fmt.Errorf("active planboards for agent %d: %w", agentID, err)
	}
	defer rows.Close()

	bindings := []Binding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := []Assignment{}
	for _, b := range bindings {
		p, err := s.Get(ctx, b.PlanboardID)
		if err != nil {
			return nil, err
		}
		st, err := s.state(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		cur, ok := stepAt(p.Steps, st.Step)
		if !ok {
			// The plan has no step there. Saving clamps positions, so this is
			// a plan with no steps at all, which Save refuses to create.
			slog.Warn("planboard has no step at its position", "planboard", p.Name, "step", st.Step)
			continue
		}
		out = append(out, Assignment{Planboard: p, Binding: b, State: st, Current: cur})
	}
	return out, nil
}

func stepAt(steps []Step, ordinal int) (Step, bool) {
	for _, st := range steps {
		if st.Ordinal == ordinal {
			return st, true
		}
	}
	return Step{}, false
}

// BeginRun applies the mode at the start of a run.
//
// Only restart does anything: it pulls the position back to step one, so the
// procedure is walked from the top. Resume is the absence of this.
func (s *Store) BeginRun(ctx context.Context, bindingID int64) error {
	var mode string
	if err := s.db.QueryRowContext(ctx, `
		SELECT p.mode FROM planboard_bindings b
		JOIN planboards p ON p.id = b.planboard_id WHERE b.id = ?`, bindingID).Scan(&mode); err != nil {
		return fmt.Errorf("mode of planboard binding %d: %w", bindingID, err)
	}
	if Mode(mode) != ModeRestart {
		return nil
	}
	if _, err := s.state(ctx, bindingID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE planboard_state SET step = 1, blocked_note = '', updated_at = ? WHERE binding_id = ?`,
		now(), bindingID)
	return err
}

// Done closes the current step and moves to the next one. Past the last step
// the plan wraps: the cycle count goes up and the position returns to one.
//
// Wrapping rather than stopping, because a plan that stops is a workflow that
// silently does nothing on its second night. A plan meant to run once is a
// plan bound, walked, and unbound — which is a decision somebody makes, not a
// state the engine invents.
func (s *Store) Done(ctx context.Context, bindingID int64, note string) (State, error) {
	st, err := s.state(ctx, bindingID)
	if err != nil {
		return State{}, err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM planboard_steps
		WHERE planboard_id = (SELECT planboard_id FROM planboard_bindings WHERE id = ?)`, bindingID).Scan(&count); err != nil {
		return State{}, fmt.Errorf("step count for planboard binding %d: %w", bindingID, err)
	}
	if count == 0 {
		return State{}, ErrEmpty
	}

	next, cycle := st.Step+1, st.Cycle
	if next > count {
		next, cycle = 1, cycle+1
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE planboard_state SET step = ?, cycle = ?, blocked_note = '', updated_at = ? WHERE binding_id = ?`,
		next, cycle, now(), bindingID); err != nil {
		return State{}, fmt.Errorf("advance planboard binding %d: %w", bindingID, err)
	}
	slog.Info("planboard step done", "binding_id", bindingID, "was", st.Step, "now", next, "cycle", cycle, "note", note)
	return State{Step: next, Cycle: cycle, UpdatedAt: now()}, nil
}

// Blocked records why a step could not be finished and leaves the position
// where it is, so the next run meets the same step — and is told what stopped
// the last one.
func (s *Store) Blocked(ctx context.Context, bindingID int64, reason string) (State, error) {
	st, err := s.state(ctx, bindingID)
	if err != nil {
		return State{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE planboard_state SET blocked_note = ?, updated_at = ? WHERE binding_id = ?`,
		reason, now(), bindingID); err != nil {
		return State{}, fmt.Errorf("record block on planboard binding %d: %w", bindingID, err)
	}
	slog.Info("planboard step blocked", "binding_id", bindingID, "step", st.Step, "reason", reason)
	st.BlockedNote = reason
	return st, nil
}

// Reset puts the position back to step one without touching the cycle count.
// The cycles happened; a person moving the marker is not un-happening them.
func (s *Store) Reset(ctx context.Context, bindingID int64) (State, error) {
	if _, err := s.state(ctx, bindingID); err != nil {
		return State{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE planboard_state SET step = 1, blocked_note = '', updated_at = ? WHERE binding_id = ?`,
		now(), bindingID); err != nil {
		return State{}, fmt.Errorf("reset planboard binding %d: %w", bindingID, err)
	}
	return s.state(ctx, bindingID)
}

// Seek moves the position to a chosen step, for a person who knows the plan
// got ahead of the world. Out of range is refused rather than clamped: a
// caller asking for step nine of seven has a wrong idea about the plan, and
// quietly giving them step seven hides it.
func (s *Store) Seek(ctx context.Context, bindingID int64, step int) (State, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM planboard_steps
		WHERE planboard_id = (SELECT planboard_id FROM planboard_bindings WHERE id = ?)`, bindingID).Scan(&count); err != nil {
		return State{}, err
	}
	if step < 1 || step > count {
		return State{}, fmt.Errorf("step %d is outside this plan, which has %d", step, count)
	}
	if _, err := s.state(ctx, bindingID); err != nil {
		return State{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE planboard_state SET step = ?, blocked_note = '', updated_at = ? WHERE binding_id = ?`,
		step, now(), bindingID); err != nil {
		return State{}, err
	}
	return s.state(ctx, bindingID)
}

// SetState puts a position back exactly as it was, for a rollback. Not Seek:
// a restore carries the cycle count too, and it must not refuse a position
// that was legal when it was recorded.
func (s *Store) SetState(ctx context.Context, bindingID int64, step, cycle int) error {
	if step < 1 {
		step = 1
	}
	if _, err := s.state(ctx, bindingID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE planboard_state SET step = ?, cycle = ?, updated_at = ? WHERE binding_id = ?`,
		step, cycle, now(), bindingID)
	return err
}
