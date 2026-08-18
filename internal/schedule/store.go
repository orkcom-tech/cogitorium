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

// What a clock dials.
//
// TargetTask is the original and is still right when a job genuinely has a door
// as well as a clock: the task says which agent, what to tell it, what it
// accepts and what success means, and a firing is that same job with nobody on
// the other end.
//
// The other two exist because a task describes a DOOR — an inlet, an address, a
// key, a caller — and a schedule is not that. Making every nightly job invent a
// receiver nobody would ever call filled the receivers list with entries that
// had no inlet and no caller, which is a worse lie than the one it avoided.
const (
	TargetTask  = "task"
	TargetAgent = "agent"
	TargetGear  = "gear"
)

type Schedule struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	TargetKind  string `json:"target_kind"`
	// TaskID is set only for a task schedule. A pointer, because the other two
	// kinds have no task at all and a 0 in that field would read as a task
	// whose row happens to be missing.
	TaskID *int64 `json:"task_id,omitempty"`
	// TargetAgentID and TargetGearID are the direct paths, and are NIL ON A
	// BROKEN SCHEDULE: deleting an agent or a gear sets them null rather than
	// cascading the schedule away, so a nightly job that lost its target shows
	// as broken instead of vanishing. See Broken.
	TargetAgentID *int64 `json:"target_agent_id,omitempty"`
	TargetGearID  *int64 `json:"target_gear_id,omitempty"`
	// Instruction is the sentence an agent target is given — a clock wired to
	// an agent with nothing to say produces a turn with an empty prompt. Args
	// is the argument object a gear target is called with, checked against that
	// gear's schema when the schedule is SAVED rather than when it fires.
	Instruction string    `json:"instruction,omitempty"`
	Args        string    `json:"args,omitempty"`
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
	if strings.TrimSpace(sc.Args) == "" {
		sc.Args = "{}"
	}
	if err := sc.validTarget(); err != nil {
		return Schedule{}, err
	}
	next, ok := spec.Next(time.Now(), loc)
	if !ok {
		return Schedule{}, fmt.Errorf("%q never comes round — check the day and month", sc.Spec)
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO schedules (workspace_id, target_kind, task_id, target_agent_id, target_gear_id,
		                        instruction, args, name, spec, tz, payload, enabled, on_miss,
		                        next_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		sc.WorkspaceID, sc.TargetKind, sc.TaskID, sc.TargetAgentID, sc.TargetGearID,
		sc.Instruction, sc.Args, sc.Name, spec.String(), sc.TZ, sc.Payload, sc.OnMiss,
		stamp(next), now(), now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Schedule{}, ErrConflict
		}
		return Schedule{}, fmt.Errorf("create schedule: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("schedule created", "id", id, "workspace_id", sc.WorkspaceID,
		"target_kind", sc.TargetKind, "task_id", sc.TaskID,
		"target_agent_id", sc.TargetAgentID, "target_gear_id", sc.TargetGearID,
		"name", sc.Name, "spec", spec.String(), "tz", sc.TZ, "next_at", stamp(next))
	return s.Get(ctx, id)
}

// validTarget is the half of the shape rule that the CHECK deliberately does
// not enforce.
//
// The table permits a NULL target on an agent or gear row, because that is what
// a schedule whose target was deleted looks like and it must be storable — see
// 0031. What must never be STORED FRESH is a row that names nothing, and this
// is where an operator is still on the other end of the error.
func (sc *Schedule) validTarget() error {
	switch sc.TargetKind {
	case "":
		// Every row before 0031 was a task schedule, and every caller written
		// before it says so by saying nothing.
		sc.TargetKind = TargetTask
		fallthrough
	case TargetTask:
		if sc.TaskID == nil {
			return errors.New("a schedule on a receiver task needs a task to fire")
		}
		sc.TargetAgentID, sc.TargetGearID = nil, nil
		sc.Instruction = ""
	case TargetAgent:
		if sc.TargetAgentID == nil {
			return errors.New("a schedule on an agent needs an agent to dial")
		}
		if strings.TrimSpace(sc.Instruction) == "" {
			// A clock wired to an agent with nothing to say produces a turn
			// with an empty prompt, which is a run that costs money and
			// answers nothing.
			return errors.New("a schedule on an agent needs something to tell it: " +
				"a firing with no instruction is a turn with an empty prompt")
		}
		sc.TaskID, sc.TargetGearID = nil, nil
	case TargetGear:
		if sc.TargetGearID == nil {
			return errors.New("a schedule on a gear needs a gear to run")
		}
		sc.TaskID, sc.TargetAgentID = nil, nil
		sc.Instruction = ""
	default:
		return fmt.Errorf("target_kind is %q; it may be %s, %s or %s",
			sc.TargetKind, TargetTask, TargetAgent, TargetGear)
	}
	return nil
}

// Broken reports a schedule whose target has been deleted out from under it.
//
// This is the state SET NULL exists to produce. The alternative — cascading the
// schedule away with its agent — means the nightly job silently stops and
// nobody learns why until the thing it was doing is noticed missing. A broken
// schedule is refused at fire time, drawn as broken on the canvas, and still
// there to be repointed.
func (sc Schedule) Broken() bool {
	switch sc.TargetKind {
	case TargetAgent:
		return sc.TargetAgentID == nil
	case TargetGear:
		return sc.TargetGearID == nil
	default:
		return false
	}
}

const scheduleSelect = `
	SELECT id, workspace_id, target_kind, task_id, target_agent_id, target_gear_id,
	       instruction, args, name, spec, tz, payload, enabled, on_miss,
	       next_at, last_work_id, last_fired_at, last_outcome, fires, skips, created_at, updated_at
	  FROM schedules`

func scan(row interface{ Scan(...any) error }) (Schedule, error) {
	var sc Schedule
	var next string
	var enabled int
	if err := row.Scan(&sc.ID, &sc.WorkspaceID, &sc.TargetKind, &sc.TaskID,
		&sc.TargetAgentID, &sc.TargetGearID, &sc.Instruction, &sc.Args,
		&sc.Name, &sc.Spec, &sc.TZ, &sc.Payload,
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
