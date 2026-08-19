// Package work is the queue: one durable table of claimed rows, and the rule
// that a lane runs one unit at a time.
//
// It exists because "the workspace is busy" was being answered by destroying
// the request. A delivery that met a running turn was settled `failed` with the
// engine's busy error and answered 429 — the same terminal state a genuinely
// broken job gets — so a burst of two hundred tickets was one job done and a
// hundred and ninety-nine losses that a caller could only tell apart from real
// failures by string-matching an error message.
//
// One table rather than several. The lane rule, the queue and the scheduler
// that comes later are one mechanism seen from three angles: the lane is a
// partial unique index, the queue is the same table with a waiting state, and a
// schedule is a writer that enqueues into it. Built separately they would be
// three protocols that have to keep agreeing with each other forever.
//
// Nothing here knows what a unit DOES. The runner is injected by the caller, so
// this package holds no engine, no HTTP and no model — which is also what makes
// it testable against a real database without any of them.
package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ErrNotFound is a unit that is not there, or is no longer in the state the
// caller believed it was in.
var ErrNotFound = errors.New("not found")

// ErrDuplicate is an idempotency key this workspace has already seen. The
// caller answers with the original unit rather than treating it as an error:
// that is what makes a retry idempotent rather than merely refused.
var ErrDuplicate = errors.New("already enqueued under this idempotency key")

// ErrLaneBusy is a lane that is already running something. It is what a caller
// that cannot wait — a chat turn — gets instead of a place in the queue.
var ErrLaneBusy = errors.New("this workspace is already working on something")

// The states a unit passes through. Deliberately four, and deliberately without
// a "failed": a unit that ran and whose work failed is `done` — what happened
// inside it belongs to the ledger row it filled in, not to the queue. `dead`
// means the queue itself gave up on it.
const (
	StateQueued  = "queued"
	StateClaimed = "claimed"
	StateDone    = "done"
	StateDead    = "dead"
)

// What a unit is. A delivery arrived through an inlet; a chat turn is an
// operator typing; a callback is this install telling somebody else a run
// finished. The third has no producer yet and is in the CHECK from the start,
// because adding it later means rebuilding a table that by then holds rows.
const (
	KindDelivery = "delivery"
	KindChat     = "chat"
	KindCallback = "callback"
	// KindPlugin is a plugin's own background task. Its workspace_id is 0: a
	// plugin task is ordinarily the install's work rather than a workspace's,
	// and zero is not a workspace id anywhere in this schema.
	KindPlugin = "plugin"
)

// Unit is one piece of work.
type Unit struct {
	ID          int64
	Kind        string
	WorkspaceID int64
	Lane        string
	Args        string
	IdemKey     string
	State       string
	RunID       *int64
	RunAfter    time.Time
	Deadline    time.Time
	Attempts    int
	MaxAttempts int
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Lane names the serialisation domain a unit belongs to. One workspace, one
// unit at a time — the rule the engine already followed in memory, written
// down where more than one thing can read it.
//
// A function rather than a format string at each call site, because the day a
// lane means something narrower the change must happen once.
func Lane(workspaceID int64) string { return fmt.Sprintf("ws:%d", workspaceID) }

// Store is the queue over one database. It holds no state of its own: two
// Stores over one database are the same queue, which is what makes the claim
// protocol the only thing that has to be right.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func now() time.Time { return time.Now().UTC() }

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// parseStamp reads a stored timestamp. A row this package wrote always parses;
// the zero time is what a caller gets for a column that was left empty, which
// is how "no deadline" is spelled.
func parseStamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		slog.Warn("unreadable timestamp in the work queue", "value", s, "err", err)
		return time.Time{}
	}
	return t
}

// Enqueue puts a unit in the queue and returns it.
//
// A duplicate idempotency key is not an error the caller has to handle as one:
// ErrDuplicate comes back with the unit that already exists, so a caller
// retrying a delivery is answered with the original run rather than being told
// off. Being idempotent is the point; refusing would just move the problem to
// whoever wrote the retry loop.
func (s *Store) Enqueue(ctx context.Context, u Unit) (Unit, error) {
	if u.Lane == "" {
		return Unit{}, errors.New("a unit with no lane cannot be serialised against anything")
	}
	if u.Args == "" {
		u.Args = "{}"
	}
	if u.MaxAttempts <= 0 {
		u.MaxAttempts = 1
	}
	if u.RunAfter.IsZero() {
		u.RunAfter = now()
	}
	at := stamp(now())

	var key any
	if u.IdemKey != "" {
		key = u.IdemKey
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO work (kind, workspace_id, lane, args, idem_key, state, run_id,
		                   run_after, deadline, attempts, max_attempts, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		u.Kind, u.WorkspaceID, u.Lane, u.Args, key, StateQueued, u.RunID,
		stamp(u.RunAfter), deadlineText(u.Deadline), u.MaxAttempts, at, at)
	if err != nil {
		if u.IdemKey != "" && strings.Contains(err.Error(), "UNIQUE constraint failed") {
			existing, findErr := s.ByIdem(ctx, u.Kind, u.WorkspaceID, u.IdemKey)
			if findErr != nil {
				return Unit{}, fmt.Errorf("enqueue: %w", err)
			}
			return existing, ErrDuplicate
		}
		return Unit{}, fmt.Errorf("enqueue: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Unit{}, fmt.Errorf("enqueue: %w", err)
	}
	return s.Get(ctx, id)
}

func deadlineText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return stamp(t)
}

const unitSelect = `
	SELECT id, kind, workspace_id, lane, args, COALESCE(idem_key, ''), state, run_id,
	       run_after, deadline, attempts, max_attempts, last_error, created_at, updated_at
	  FROM work`

func scanUnit(row interface{ Scan(...any) error }) (Unit, error) {
	var u Unit
	var runAfter, deadline, created, updated string
	if err := row.Scan(&u.ID, &u.Kind, &u.WorkspaceID, &u.Lane, &u.Args, &u.IdemKey, &u.State,
		&u.RunID, &runAfter, &deadline, &u.Attempts, &u.MaxAttempts, &u.LastError,
		&created, &updated); err != nil {
		return Unit{}, err
	}
	u.RunAfter, u.Deadline = parseStamp(runAfter), parseStamp(deadline)
	u.CreatedAt, u.UpdatedAt = parseStamp(created), parseStamp(updated)
	return u, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Unit, error) {
	u, err := scanUnit(s.db.QueryRowContext(ctx, unitSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Unit{}, fmt.Errorf("work unit %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Unit{}, fmt.Errorf("read work unit %d: %w", id, err)
	}
	return u, nil
}

// ByIdem finds what a caller's key already produced.
func (s *Store) ByIdem(ctx context.Context, kind string, wsID int64, key string) (Unit, error) {
	u, err := scanUnit(s.db.QueryRowContext(ctx,
		unitSelect+` WHERE kind = ? AND workspace_id = ? AND idem_key = ?`, kind, wsID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Unit{}, ErrNotFound
	}
	if err != nil {
		return Unit{}, fmt.Errorf("read work unit by key: %w", err)
	}
	return u, nil
}

// Claim takes the oldest runnable unit whose lane is free, marks it claimed and
// returns it. ErrNotFound means there was nothing to take, which is the ordinary
// answer and not a fault.
//
// The lane rule is enforced by the partial unique index, not by the NOT EXISTS
// below. That subquery only avoids PICKING a row that would certainly be
// refused; if it were the guarantee, this queue would be correct on SQLite and
// silently broken under any database that lets two writers proceed at once. The
// unique-violation retry underneath is what makes the index the authority: a
// claim that loses a race simply tries the next candidate.
func (s *Store) Claim(ctx context.Context) (Unit, error) {
	at := stamp(now())
	for attempt := 0; attempt < 3; attempt++ {
		row := s.db.QueryRowContext(ctx,
			`UPDATE work
			    SET state = ?, attempts = attempts + 1, updated_at = ?
			  WHERE id = (
			        SELECT w.id FROM work w
			         WHERE w.state = ?
			           AND w.run_after <= ?
			           AND NOT EXISTS (
			                 SELECT 1 FROM work c
			                  WHERE c.lane = w.lane AND c.state = ?)
			         ORDER BY w.run_after, w.id
			         LIMIT 1)
			  RETURNING id, kind, workspace_id, lane, args, COALESCE(idem_key, ''), state, run_id,
			            run_after, deadline, attempts, max_attempts, last_error, created_at, updated_at`,
			StateClaimed, at, StateQueued, at, StateClaimed)
		u, err := scanUnit(row)
		if errors.Is(err, sql.ErrNoRows) {
			return Unit{}, ErrNotFound
		}
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				// Somebody claimed this lane between the subquery and the
				// write. Try again; a different lane's unit is waiting.
				continue
			}
			return Unit{}, fmt.Errorf("claim work: %w", err)
		}
		return u, nil
	}
	return Unit{}, ErrNotFound
}

// Settle finishes a unit. errText empty means it did what it was for; a unit
// whose WORK failed is still settled done, because what happened inside it
// belongs to the ledger row rather than to the queue.
func (s *Store) Settle(ctx context.Context, id int64, errText string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE work SET state = ?, last_error = ?, updated_at = ? WHERE id = ? AND state = ?`,
		StateDone, errText, stamp(now()), id, StateClaimed)
	if err != nil {
		return fmt.Errorf("settle work unit %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("work unit %d: %w", id, ErrNotFound)
	}
	return nil
}

// Kill takes a unit out of the queue without running it: a cancel, or a claim
// nothing can finish. It is deliberately allowed from either live state — an
// operator cancelling does not care whether the unit had already started, and a
// queue that could only cancel one of the two would leave the other unreachable.
func (s *Store) Kill(ctx context.Context, id int64, reason string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE work SET state = ?, last_error = ?, updated_at = ? WHERE id = ? AND state IN (?, ?)`,
		StateDead, reason, stamp(now()), id, StateQueued, StateClaimed)
	if err != nil {
		return fmt.Errorf("kill work unit %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("work unit %d: %w", id, ErrNotFound)
	}
	return nil
}

// ReleaseClaims marks every claimed unit dead at startup.
//
// A claim is held by a process, and this install runs one. So a claimed row
// found at boot belongs to a process that is gone, and its work stopped
// wherever it was — mid-model-call, mid-gear, having already written files.
// Marking it dead rather than requeueing it is the same decision max_attempts:1
// makes: re-running work that may already have spent tokens and sent something
// outward is not a recovery, it is a second execution nobody asked for.
//
// This is what stands in for lease expiry and fencing while there is one
// process. When there is a second, those become necessary and this becomes
// wrong — that is a deliberate boundary, not an omission.
func (s *Store) ReleaseClaims(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE work SET state = ?, last_error = ?, updated_at = ? WHERE state = ?`,
		StateDead, "the server restarted while this was running", stamp(now()), StateClaimed)
	if err != nil {
		return 0, fmt.Errorf("release claims: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		slog.Warn("work units were running when this server last stopped; they are marked dead, not retried", "units", n)
	}
	return n, nil
}

// Depth is what is waiting and what is running in one lane, which is the answer
// to the only two questions an operator has about a queue.
func (s *Store) Depth(ctx context.Context, lane string) (queued, claimed int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FILTER (WHERE state = ?), COUNT(*) FILTER (WHERE state = ?)
		   FROM work WHERE lane = ?`,
		StateQueued, StateClaimed, lane).Scan(&queued, &claimed)
	if err != nil {
		return 0, 0, fmt.Errorf("read queue depth: %w", err)
	}
	return queued, claimed, nil
}

// TotalDepth is Depth across every lane.
//
// Separate from Depth rather than a special case of it: Depth's whole job is to
// answer "is this workspace busy", which is a per-lane question the queue is
// built around, and an empty lane there means the lane called "" rather than
// all of them. A caller that wanted the install-wide number and passed "" would
// silently read zero — which is exactly what a metrics gauge would then publish
// as "nothing is waiting".
func (s *Store) TotalDepth(ctx context.Context) (queued, claimed int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FILTER (WHERE state = ?), COUNT(*) FILTER (WHERE state = ?) FROM work`,
		StateQueued, StateClaimed).Scan(&queued, &claimed)
	if err != nil {
		return 0, 0, fmt.Errorf("read total queue depth: %w", err)
	}
	return queued, claimed, nil
}

// Waiting lists a lane's queue, oldest first, so an operator can see position
// rather than only depth.
func (s *Store) Waiting(ctx context.Context, lane string, limit int) ([]Unit, error) {
	if limit <= 0 {
		limit = 50
	}
	// The running unit first, then the queue in the order it will run. Spelled
	// as an explicit CASE rather than as an alphabetical accident: sorting by
	// state text puts 'queued' above 'claimed', which reads as a queue whose
	// running job is last in line.
	rows, err := s.db.QueryContext(ctx,
		unitSelect+` WHERE lane = ? AND state IN (?, ?)
		             ORDER BY CASE state WHEN 'claimed' THEN 0 ELSE 1 END, run_after, id
		             LIMIT ?`,
		lane, StateClaimed, StateQueued, limit)
	if err != nil {
		return nil, fmt.Errorf("list the queue: %w", err)
	}
	defer rows.Close()
	var out []Unit
	for rows.Next() {
		u, err := scanUnit(rows)
		if err != nil {
			return nil, fmt.Errorf("list the queue: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Prune removes settled units older than the window. Nothing else in this
// schema prunes anything, and a queue is the one table that would otherwise
// grow forever by design rather than by accident: every delivery, every chat
// turn, one row each.
func (s *Store) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := stamp(now().Add(-olderThan))
	res, err := s.db.ExecContext(ctx,
		// <= rather than <: timestamps here are RFC3339 to the second, so a
		// unit settled within the same second as the cutoff is not "older
		// than" it by a strict comparison and would survive every sweep on a
		// quiet install.
		`DELETE FROM work WHERE state IN (?, ?) AND updated_at <= ?`, StateDone, StateDead, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune the work queue: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ClaimNow inserts a unit already claimed, or refuses because the lane is busy.
//
// It exists for work that cannot wait in a queue: an operator's chat turn has a
// person holding a stream and possibly an approval on screen, so parking it in
// a row would leave a browser waiting on something nobody can see. A chat turn
// either gets the lane immediately or is told the workspace is busy, which is
// what it did before any of this existed.
//
// The point is that it takes the SAME lane deliveries queue behind. Two latches
// that cannot see each other would let a chat turn and a delivery run at once in
// one workspace — and the turn state they would share holds the egress budget,
// both anti-worm taint latches and the run record, keyed by workspace.
func (s *Store) ClaimNow(ctx context.Context, u Unit) (Unit, error) {
	if u.Lane == "" {
		return Unit{}, errors.New("a unit with no lane cannot be serialised against anything")
	}
	if u.Args == "" {
		u.Args = "{}"
	}
	at := stamp(now())
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO work (kind, workspace_id, lane, args, state, run_id,
		                   run_after, deadline, attempts, max_attempts, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', 1, 1, ?, ?)`,
		u.Kind, u.WorkspaceID, u.Lane, u.Args, StateClaimed, u.RunID, at, at, at)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Unit{}, ErrLaneBusy
		}
		return Unit{}, fmt.Errorf("claim the lane: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Unit{}, fmt.Errorf("claim the lane: %w", err)
	}
	return s.Get(ctx, id)
}
