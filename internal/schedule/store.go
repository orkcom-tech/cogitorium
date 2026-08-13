package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("a schedule with that name already exists in this workspace")
)

// What became of a firing, as the row records it. Three words rather than a
// free-text note, because the first question about a schedule is always "did
// last night's run happen" and an answer that has to be read is an answer
// nobody aggregates.
const (
	OutcomeFired   = "fired"
	OutcomeSkipped = "skipped"
	OutcomeFailed  = "failed"
)

type Schedule struct {
	ID          int64     `json:"id"`
	WorkspaceID int64     `json:"workspace_id"`
	TaskID      int64     `json:"task_id"`
	Name        string    `json:"name"`
	Spec        string    `json:"spec"`
	TZ          string    `json:"tz"`
	Payload     string    `json:"payload"`
	Enabled     bool      `json:"enabled"`
	OnMiss      string    `json:"on_miss"`
	NextAt      time.Time `json:"next_at"`
	LastWorkID  *int64    `json:"last_work_id,omitempty"`
	LastFiredAt string    `json:"last_fired_at,omitempty"`
	LastOutcome string    `json:"last_outcome,omitempty"`
	Fires       int64     `json:"fires"`
	Skips       int64     `json:"skips"`
	CreatedAt   string    `json:"created_at"`
	UpdatedAt   string    `json:"updated_at"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
func now() string              { return stamp(time.Now()) }

// Create validates everything it can before anything is stored.
//
// The spec, the zone and the on-miss rule are all checked here rather than at
// fire time, because this is the only moment the person who typed them is still
// looking at them. A schedule that parses at 02:00 and fails at 02:00 is a
// schedule nobody finds until the job stops happening.
func (s *Store) Create(ctx context.Context, sc Schedule) (Schedule, error) {
	spec, err := Parse(sc.Spec)
	if err != nil {
		return Schedule{}, err
	}
	loc, err := Location(sc.TZ)
	if err != nil {
		return Schedule{}, err
	}
	if sc.Name = strings.TrimSpace(sc.Name); sc.Name == "" {
		return Schedule{}, errors.New("a schedule needs a name, so an operator can say which one they mean")
	}
	if sc.OnMiss == "" {
		sc.OnMiss = "skip"
	}
	if sc.OnMiss != "skip" && sc.OnMiss != "run" {
		return Schedule{}, fmt.Errorf("on_miss is %q; it may be `skip` or `run`", sc.OnMiss)
	}
	if strings.TrimSpace(sc.Payload) == "" {
		sc.Payload = "{}"
	}
	next, ok := spec.Next(time.Now(), loc)
	if !ok {
		return Schedule{}, fmt.Errorf("%q never comes round — check the day and month", sc.Spec)
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO schedules (workspace_id, task_id, name, spec, tz, payload, enabled, on_miss,
		                        next_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		sc.WorkspaceID, sc.TaskID, sc.Name, spec.String(), sc.TZ, sc.Payload, sc.OnMiss,
		stamp(next), now(), now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Schedule{}, ErrConflict
		}
		return Schedule{}, fmt.Errorf("create schedule: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("schedule created", "id", id, "workspace_id", sc.WorkspaceID, "task_id", sc.TaskID,
		"name", sc.Name, "spec", spec.String(), "tz", sc.TZ, "next_at", stamp(next))
	return s.Get(ctx, id)
}

const scheduleSelect = `
	SELECT id, workspace_id, task_id, name, spec, tz, payload, enabled, on_miss,
	       next_at, last_work_id, last_fired_at, last_outcome, fires, skips, created_at, updated_at
	  FROM schedules`

func scan(row interface{ Scan(...any) error }) (Schedule, error) {
	var sc Schedule
	var next string
	var enabled int
	if err := row.Scan(&sc.ID, &sc.WorkspaceID, &sc.TaskID, &sc.Name, &sc.Spec, &sc.TZ, &sc.Payload,
		&enabled, &sc.OnMiss, &next, &sc.LastWorkID, &sc.LastFiredAt, &sc.LastOutcome,
		&sc.Fires, &sc.Skips, &sc.CreatedAt, &sc.UpdatedAt); err != nil {
		return Schedule{}, err
	}
	sc.Enabled = enabled != 0
	if t, err := time.Parse(time.RFC3339, next); err == nil {
		sc.NextAt = t
	}
	return sc, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Schedule, error) {
	sc, err := scan(s.db.QueryRowContext(ctx, scheduleSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, fmt.Errorf("schedule %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("read schedule %d: %w", id, err)
	}
	return sc, nil
}

func (s *Store) List(ctx context.Context, wsID int64) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, scheduleSelect+` WHERE workspace_id = ? ORDER BY name`, wsID)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		sc, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list schedules: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// Due lists the schedules that should have fired by now.
func (s *Store) Due(ctx context.Context, at time.Time, limit int) ([]Schedule, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		scheduleSelect+` WHERE enabled = 1 AND next_at <= ? ORDER BY next_at, id LIMIT ?`,
		stamp(at), limit)
	if err != nil {
		return nil, fmt.Errorf("read due schedules: %w", err)
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		sc, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("read due schedules: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// Advance moves a schedule past the firing it has just had, and records what
// became of it.
//
// The `next_at = ?` guard is what makes the tick safe to run more than once:
// two ticks that both read the same due row race here, and exactly one of them
// writes. That is a compare-and-set rather than a lock, so nothing is held
// while a unit is being enqueued.
func (s *Store) Advance(ctx context.Context, sc Schedule, next time.Time, outcome string, workID *int64) (bool, error) {
	fires, skips := 0, 0
	switch outcome {
	case OutcomeSkipped:
		skips = 1
	default:
		fires = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE schedules
		    SET next_at = ?, last_work_id = ?, last_fired_at = ?, last_outcome = ?,
		        fires = fires + ?, skips = skips + ?, updated_at = ?
		  WHERE id = ? AND next_at = ?`,
		stamp(next), workID, now(), outcome, fires, skips, now(), sc.ID, stamp(sc.NextAt))
	if err != nil {
		return false, fmt.Errorf("advance schedule %d: %w", sc.ID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetEnabled turns a schedule on or off, and re-bases its next firing when it
// comes back — a schedule switched on after a fortnight should run next at its
// next proper time, not fire immediately for every tick it was off for.
func (s *Store) SetEnabled(ctx context.Context, id int64, on bool) (Schedule, error) {
	sc, err := s.Get(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	next := sc.NextAt
	if on {
		spec, err := Parse(sc.Spec)
		if err != nil {
			return Schedule{}, err
		}
		loc, err := Location(sc.TZ)
		if err != nil {
			return Schedule{}, err
		}
		if t, ok := spec.Next(time.Now(), loc); ok {
			next = t
		}
	}
	enabled := 0
	if on {
		enabled = 1
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE schedules SET enabled = ?, next_at = ?, updated_at = ? WHERE id = ?`,
		enabled, stamp(next), now(), id); err != nil {
		return Schedule{}, fmt.Errorf("set schedule %d enabled: %w", id, err)
	}
	return s.Get(ctx, id)
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete schedule %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("schedule %d: %w", id, ErrNotFound)
	}
	slog.Info("schedule deleted", "id", id)
	return nil
}

// NoteUnit records which queued unit a firing produced, so "did last night's
// job run" is answerable from the schedule's own row rather than by searching a
// queue that prunes itself.
func (s *Store) NoteUnit(ctx context.Context, id, unitID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE schedules SET last_work_id = ?, updated_at = ? WHERE id = ?`, unitID, now(), id); err != nil {
		return fmt.Errorf("note the unit of schedule %d: %w", id, err)
	}
	return nil
}
