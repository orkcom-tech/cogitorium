package inlet

import (
	"context"
	"database/sql"
	"encoding/json"
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

// Run states. Three are live and six are terminal; nothing else may be written,
// because the CHECK on the table enumerates exactly these.
const (
	// StateAccepted: the key opened the door and the task exists. Written
	// before the payload is even looked at.
	StateAccepted = "accepted"
	// StateQueued: the payload is checked and stored, and the delivery is
	// waiting for its workspace to be free.
	//
	// This state is why the queue exists. A delivery that met a busy workspace
	// used to be settled StateFailed — the same terminal state a genuinely
	// broken job gets — so a burst of two hundred tickets was one job done and
	// a hundred and ninety-nine rows a caller could only tell apart from real
	// failures by string-matching an error message. Waiting is not failing.
	StateQueued = "queued"
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
	// StateRefusedExpectation: the run's RECORD did not meet what the task
	// declared success to be — the gear it names never ran, or the files it
	// requires never appeared. The agent may well have answered beautifully;
	// that is not what was checked, and a beautiful answer over an empty record
	// is the exact failure this state exists to name.
	StateRefusedExpectation = "refused_expectation"
	// StateRefusedBudget: the run reached the token ceiling an operator set for
	// one delivery, and was stopped before its next model call.
	//
	// Its own state because a caller outside must be able to tell "your job hit
	// the ceiling" from "we broke". The first reaction is to send less or ask
	// for more headroom; the second is to retry — and retrying a job that was
	// deliberately stopped is how a ceiling turns into a bill.
	StateRefusedBudget = "refused_budget"
	// StateRefusedOutputSchema: the answer did not fit the shape the task
	// declared. Kept apart from the one above because they want different
	// reactions from whoever is paged: "the model produced garbage" is a
	// prompting or model problem, "the work did not happen" is an outage.
	StateRefusedOutputSchema = "refused_output_schema"
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
	// Did is the record of what the run actually did — which tools ran, which
	// files appeared, what it cost — passed through exactly as the engine
	// produced it. It is raw JSON rather than a typed field because the ledger
	// is not the thing that decides what a run does: a package that stores
	// deliveries has no business importing the engine to spell out the shape,
	// and re-declaring it here would give the record two definitions, which is
	// how they drift.
	//
	// Null means no record was kept: the run has not settled yet, or the row
	// predates the column. That is deliberately NOT the same as a record
	// showing nothing happened — which is an object with empty lists, and is
	// itself the answer to "did it do anything".
	Did       json.RawMessage `json:"did"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

// Accept records a delivery that got past the door, before the payload is
// looked at. Its id is the run number the caller is answered with, so it has to
// exist before anything can go wrong with the payload.
// inletID is a POINTER because a run need not have a door. inlet_runs.inlet_id
// is nullable and carries no foreign key deliberately (see 0016), and a clock
// firing directly at an agent or a gear has no inlet at all — passing 0 would
// store a zero that reads as a door whose row has gone missing.
func (s *Store) Accept(ctx context.Context, wsID int64, inletID *int64, address, taskName, agentName string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO inlet_runs (workspace_id, inlet_id, inlet_address, task_name, agent_name, state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		wsID, inletID, address, taskName, agentName, StateAccepted, now(), now())
	if err != nil {
		return 0, fmt.Errorf("record the inlet run: %w", err)
	}
	return res.LastInsertId()
}

// Queued records everything known before the work starts, and says the delivery
// is WAITING rather than running.
//
// The state matters more than it looks. A delivery that met a busy workspace
// used to be settled `failed` — the same terminal state a genuinely broken job
// gets — so a caller could only tell a queue from a fault by string-matching an
// error message. Waiting is not failing, and now it does not have to be spelled
// as one.
func (s *Store) Queued(ctx context.Context, id, agentID, payloadBytes int64, payloadPath string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE inlet_runs
		    SET state = ?, agent_id = ?, payload_bytes = ?, payload_path = ?, updated_at = ?
		  WHERE id = ? AND state = ?`,
		StateQueued, agentID, payloadBytes, payloadPath, now(), id, StateAccepted)
	if err != nil {
		return fmt.Errorf("queue inlet run %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("inlet run %d: %w", id, ErrNotFound)
	}
	return nil
}

// Start moves a waiting delivery to running, when a worker picks it up.
func (s *Store) Start(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE inlet_runs SET state = ?, updated_at = ? WHERE id = ? AND state = ?`,
		StateRunning, now(), id, StateQueued)
	if err != nil {
		return fmt.Errorf("start inlet run %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("inlet run %d: %w", id, ErrNotFound)
	}
	return nil
}

// Begin moves an accepted run straight to running, for a delivery that is not
// going through the queue. The guard is what makes it safe to call from a path
// that may also have settled the row already: a run that was refused stays
// refused.
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
//
// did is the record of what the run did, as the engine produced it. It is a
// parameter of settling rather than a later update because the outcome and the
// record are one fact: a row that said "completed" for even a moment without
// the record beside it is the state this whole ledger exists to abolish. A run
// that never reached an agent passes the empty record — nothing ran, and saying
// so is an answer. Passing nil leaves the column empty, which reads as "no
// record was kept" and is reserved for exactly that.
func (s *Store) Settle(ctx context.Context, id int64, state, result, errText string, did []byte) error {
	res, err := s.db.ExecContext(ctx,
		// Every LIVE state, which is the whole of the guard's meaning: a run
		// that is already terminal stays as it was settled.
		//
		// StateQueued was added to this table and missed here, and the failure
		// was silent by construction — Settle affects no rows, SettleOrLog logs
		// a warning, and the caller carries on. Cancelling a waiting delivery
		// marked its work unit dead and left the ledger row saying `queued`
		// forever, so whoever was polling it waited for an answer nobody would
		// ever write.
		`UPDATE inlet_runs
		    SET state = ?, result = ?, error = ?, did = ?, updated_at = ?
		  WHERE id = ? AND state IN (?, ?, ?)`,
		state, result, errText, string(did), now(), id,
		StateAccepted, StateQueued, StateRunning)
	if err != nil {
		return fmt.Errorf("settle inlet run %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("inlet run %d: %w", id, ErrNotFound)
	}
	return nil
}

// DropPayload forgets a delivered file that was never used, so the row does not
// go on pointing at bytes that are no longer on disk.
//
// It exists for one case: a file delivery whose run never started. The busy
// check runs before the body is read, but a workspace can become busy in the
// gap between that check and the lock inside the engine — and by then the bytes
// have landed. The caller is told to retry, they do, and a second copy lands
// under a new run number while the first sits on the volume forever with
// nothing that will ever come back for it.
//
// The file is removed by the caller; this is the row half. Both or neither: a
// row naming a file that is gone is worse than either, because the one thing a
// ledger is for is being believed.
func (s *Store) DropPayload(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE inlet_runs SET payload_path = '', updated_at = ? WHERE id = ?`, now(), id); err != nil {
		return fmt.Errorf("clear the payload path of inlet run %d: %w", id, err)
	}
	return nil
}

// SettleOrLog is Settle for the paths that have already decided what to tell
// the caller. The answer is in hand by then, so a ledger hiccup is logged and
// swallowed rather than turned into a failure the caller sees for a job that
// actually ran.
func (s *Store) SettleOrLog(ctx context.Context, id int64, state, result, errText string, did []byte) {
	if err := s.Settle(ctx, id, state, result, errText, did); err != nil {
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
       payload_bytes, payload_path, state, result, error, did, created_at, updated_at
  FROM inlet_runs`

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var inletID, agentID sql.NullInt64
	var did string
	if err := row.Scan(&r.ID, &r.WorkspaceID, &inletID, &r.InletAddress, &r.TaskName, &agentID, &r.AgentName,
		&r.PayloadBytes, &r.PayloadPath, &r.State, &r.Result, &r.Error, &did, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Run{}, err
	}
	if inletID.Valid {
		r.InletID = &inletID.Int64
	}
	if agentID.Valid {
		r.AgentID = &agentID.Int64
	}
	// The column is spliced verbatim into an API response, so anything in it
	// that is not JSON would come back out as a broken body rather than as a
	// row somebody can read. A record is a few hundred bytes and this check
	// costs nothing; the warning is how an operator finds out the column has
	// been written by something other than the engine.
	if did != "" {
		if json.Valid([]byte(did)) {
			r.Did = json.RawMessage(did)
		} else {
			slog.Warn("inlet run has an unreadable record; reporting it as absent", "run_id", r.ID)
		}
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
