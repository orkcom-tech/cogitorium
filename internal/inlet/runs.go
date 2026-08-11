package inlet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
)

// The run ledger answers one question: did job 4471 happen, and what became of
// it. It is modelled on search_queries, which solved the same problem — the row
// is written BEFORE the work, so a crash between the POST and the answer still
// leaves evidence, and it is settled exactly once by a guarded UPDATE, so two
// writers cannot leave a row claiming both that the agent answered and that the
// payload was refused.

// Run states. Two are live and four are terminal; nothing else may be written,
// because the CHECK on the table enumerates exactly these.
const (
	// StateAccepted: the key opened the door and the task exists. Written
	// before the payload is even looked at.
	StateAccepted = "accepted"
	// StateRefusedSchema: the payload did not match the task's schema. No model
	// was called, and the caller got a 400.
	StateRefusedSchema = "refused_schema"
	// StateRunning: the agent is working.
	StateRunning = "running"
	// StateCompleted: the agent answered, and the answer is in `result`.
	StateCompleted = "completed"
	// StateFailed: something went wrong after the payload was accepted, and
	// `error` says what.
	StateFailed = "failed"
	// StateInterrupted: the server stopped while this run was live. Only
	// startup reconciliation writes it — an in-flight run cannot survive the
	// process that held it, and a row left saying "running" forever would read
	// as a job still going.
	StateInterrupted = "interrupted"
)

// Run is one delivery, whatever became of it.
type Run struct {
	ID           int64  `json:"id"`
	WorkspaceID  int64  `json:"workspace_id"`
	InletID      *int64 `json:"inlet_id"`
	InletAddress string `json:"inlet_address"`
	TaskName     string `json:"task_name"`
	AgentID      *int64 `json:"agent_id"`
	AgentName    string `json:"agent_name"`
	PayloadBytes int64  `json:"payload_bytes"`
	PayloadPath  string `json:"payload_path"`
	State        string `json:"state"`
	Result       string `json:"result"`
	Error        string `json:"error"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// Accept records a delivery that got past the door, before the payload is
// looked at. Its id is the run number the caller is answered with, so it has to
// exist before anything can go wrong with the payload.
func (s *Store) Accept(ctx context.Context, wsID, inletID int64, address, taskName, agentName string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO inlet_runs (workspace_id, inlet_id, inlet_address, task_name, agent_name, state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		wsID, inletID, address, taskName, agentName, StateAccepted, now(), now())
	if err != nil {
		return 0, fmt.Errorf("record the inlet run: %w", err)
	}
	return res.LastInsertId()
}

// Begin moves an accepted run to running once the payload has been checked and,
// for a file task, landed in the workspace. The guard is what makes it safe to
// call from a path that may also have settled the row already: a run that was
// refused stays refused.
func (s *Store) Begin(ctx context.Context, id, agentID, payloadBytes int64, payloadPath string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE inlet_runs
		    SET state = ?, agent_id = ?, payload_bytes = ?, payload_path = ?, updated_at = ?
		  WHERE id = ? AND state = ?`,
		StateRunning, agentID, payloadBytes, payloadPath, now(), id, StateAccepted)
	if err != nil {
		return fmt.Errorf("start inlet run %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("inlet run %d: %w", id, ErrNotFound)
	}
	return nil
}

// Settle writes the outcome exactly once. A second attempt affects no rows and
// returns ErrNotFound, which the caller logs: it means two things tried to
// finish one run, and that is worth seeing rather than silently overwriting.
func (s *Store) Settle(ctx context.Context, id int64, state, result, errText string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE inlet_runs
		    SET state = ?, result = ?, error = ?, updated_at = ?
		  WHERE id = ? AND state IN (?, ?)`,
		state, result, errText, now(), id, StateAccepted, StateRunning)
	if err != nil {
		return fmt.Errorf("settle inlet run %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("inlet run %d: %w", id, ErrNotFound)
	}
	return nil
}

// SettleOrLog is Settle for the paths that have already decided what to tell
// the caller. The answer is in hand by then, so a ledger hiccup is logged and
// swallowed rather than turned into a failure the caller sees for a job that
// actually ran.
func (s *Store) SettleOrLog(ctx context.Context, id int64, state, result, errText string) {
	if err := s.Settle(ctx, id, state, result, errText); err != nil {
		slog.Warn("could not settle the inlet run record", "run_id", id, "state", state, "err", err)
	}
}

// GetRun is the direct answer to "did job 4471 happen".
func (s *Store) GetRun(ctx context.Context, id int64) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, runSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("inlet run %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Run{}, fmt.Errorf("get inlet run %d: %w", id, err)
	}
	return r, nil
}

// WorkspaceOfRun resolves a ledger row back to its workspace so reading one run
// is gated by the same rule as everything else in that workspace.
func (s *Store) WorkspaceOfRun(ctx context.Context, id int64) (int64, error) {
	var wsID int64
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM inlet_runs WHERE id = ?`, id).Scan(&wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("inlet run %d: %w", id, ErrNotFound)
	}
	return wsID, err
}

const runSelect = `SELECT id, workspace_id, inlet_id, inlet_address, task_name, agent_id, agent_name,
       payload_bytes, payload_path, state, result, error, created_at, updated_at
  FROM inlet_runs`

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var inletID, agentID sql.NullInt64
	if err := row.Scan(&r.ID, &r.WorkspaceID, &inletID, &r.InletAddress, &r.TaskName, &agentID, &r.AgentName,
		&r.PayloadBytes, &r.PayloadPath, &r.State, &r.Result, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Run{}, err
	}
	if inletID.Valid {
		r.InletID = &inletID.Int64
	}
	if agentID.Valid {
		r.AgentID = &agentID.Int64
	}
	return r, nil
}

// ListRuns is the operator's answer to "did job 4471 happen". A ledger nobody
// can read is not a ledger, it is a table.
func (s *Store) ListRuns(ctx context.Context, wsID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, runSelect+` WHERE workspace_id = ? ORDER BY id DESC LIMIT ?`, wsID, limit)
	if err != nil {
		return nil, fmt.Errorf("list inlet runs: %w", err)
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

// ReconcileRuns runs at startup. A run lives in one process's memory, so
// nothing left accepted or running can ever finish after a restart; saying so
// in the row is what keeps the ledger from implying a job is still going.
func (s *Store) ReconcileRuns(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE inlet_runs
		    SET state = ?, updated_at = ?,
		        error = 'the server stopped while this run was in flight; whether the agent finished its work is not known'
		  WHERE state IN (?, ?)`,
		StateInterrupted, now(), StateAccepted, StateRunning)
	if err != nil {
		return 0, fmt.Errorf("reconcile inlet runs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
