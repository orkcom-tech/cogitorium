package gearnet

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// The log of what gears actually reached. Every method here does one statement
// and returns the connection: SQLite runs on a single connection here, and a
// proxied request that held it while copying a megabyte would stop the server
// answering anything else.

// Connection is one attempt, whatever became of it. It carries no path, no
// query string and no header — see the migration for why.
type Connection struct {
	ID       int64  `json:"id"`
	GearID   *int64 `json:"gear_id"`
	GearName string `json:"gear_name"`
	// Source is "gear" or "plugin". A plugin's request goes out through the
	// same gate under the same rules, and filing it under a gear name would
	// answer "which gear reached this host" with something that was never one.
	Source      string   `json:"source"`
	Version     int      `json:"version"`
	WorkspaceID *int64   `json:"workspace_id"`
	AgentID     *int64   `json:"agent_id"`
	AgentName   string   `json:"agent_name"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Method      string   `json:"method"`
	Allowed     []string `json:"allowed"`
	State       string   `json:"state"`
	BytesSent   int64    `json:"bytes_sent"`
	BytesRecv   int64    `json:"bytes_received"`
	Error       string   `json:"error"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// source defaults an empty value to gear, which is what every caller meant
// before plugins could reach the network at all.
func source(s string) string {
	if s == "" {
		return SourceGear
	}
	return s
}

// Record writes the attempt before the socket is opened. There is no path in
// this package that connects without a row first — the same rule the search
// log keeps, and for the same reason: a crash or a refusal must still leave
// the attempt on record.
func (s *Store) Record(ctx context.Context, c Connection) (int64, error) {
	allowed, err := json.Marshal(c.Allowed)
	if err != nil {
		allowed = []byte("[]")
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO gear_connections (gear_id, gear_name, source, version, workspace_id, agent_id,
		                              agent_name, host, port, method, allowed, state, error,
		                              created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.GearID, c.GearName, source(c.Source), c.Version, c.WorkspaceID, c.AgentID, c.AgentName,
		c.Host, c.Port, c.Method, string(allowed), c.State, c.Error, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("record gear connection: %w", err)
	}
	return res.LastInsertId()
}

// Settle writes the outcome of a row that was opened. It is a no-op on a row
// that is already terminal, so a connection torn down twice — by its own end
// and by the run finishing under it — cannot rewrite its own history.
func (s *Store) Settle(ctx context.Context, id int64, state string, sent, received int64, errText string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE gear_connections
		   SET state = ?, bytes_sent = ?, bytes_received = ?, error = ?, updated_at = ?
		 WHERE id = ? AND state = 'open'`,
		state, sent, received, errText, now(), id)
	if err != nil {
		return fmt.Errorf("settle gear connection %d: %w", id, err)
	}
	return nil
}

// ForGear is the operator's log for one gear, newest first. It sits beside the
// execution history on the same screen: "what did this code do" is one
// question, and the answer is the runs and the connections together.
func (s *Store) ForGear(ctx context.Context, gearID int64, limit int) ([]Connection, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, gear_id, gear_name, version, workspace_id, agent_id, agent_name,
		       host, port, method, allowed, state, bytes_sent, bytes_received, error, created_at, updated_at
		  FROM gear_connections WHERE gear_id = ? ORDER BY id DESC LIMIT ?`, gearID, limit)
	if err != nil {
		return nil, fmt.Errorf("list gear connections: %w", err)
	}
	defer rows.Close()

	out := []Connection{}
	for rows.Next() {
		var c Connection
		var allowed string
		if err := rows.Scan(&c.ID, &c.GearID, &c.GearName, &c.Version, &c.WorkspaceID, &c.AgentID,
			&c.AgentName, &c.Host, &c.Port, &c.Method, &allowed, &c.State,
			&c.BytesSent, &c.BytesRecv, &c.Error, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan gear connection: %w", err)
		}
		if err := json.Unmarshal([]byte(allowed), &c.Allowed); err != nil {
			c.Allowed = []string{}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Reconcile runs at startup. A connection lives in one process, so a row still
// saying 'open' is a socket that died when the process did; saying so is what
// keeps the log from implying traffic is still flowing.
func (s *Store) Reconcile(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE gear_connections
		   SET state = 'interrupted', updated_at = ?,
		       error = 'the server stopped while this connection was open'
		 WHERE state = 'open'`, now())
	if err != nil {
		return 0, fmt.Errorf("reconcile gear connections: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
