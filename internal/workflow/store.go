package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrNoVersion is a version that is not there.
var ErrNoVersion = errors.New("no such version")

// Version is one saved state of a workflow.
type Version struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	Number      int    `json:"number"`
	Message     string `json:"message"`
	Author      string `json:"author"`
	// RestoredFrom is the version this one was rolled back to, or zero.
	RestoredFrom int      `json:"restored_from,omitempty"`
	CreatedAt    string   `json:"created_at"`
	Snapshot     Snapshot `json:"snapshot"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Save records a workflow as it stands.
//
// Numbers are per workspace and taken inside the write, so two saves at once
// cannot both become v4. Never reused, including after a rollback: a history
// where a number can mean two things is a history nobody can cite.
func (s *Store) Save(ctx context.Context, wsID int64, snap Snapshot, message, author string, restoredFrom int) (Version, error) {
	body, err := json.Marshal(snap)
	if err != nil {
		return Version{}, fmt.Errorf("record the workflow: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var next int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(number), 0) + 1 FROM workflow_versions WHERE workspace_id = ?`,
		wsID).Scan(&next); err != nil {
		return Version{}, fmt.Errorf("find the next version number: %w", err)
	}

	at := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(ctx, `
		INSERT INTO workflow_versions (workspace_id, number, message, author, restored_from, snapshot, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		wsID, next, message, author, nullIfZero(restoredFrom), string(body), at)
	if err != nil {
		return Version{}, fmt.Errorf("save the version: %w", err)
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return Version{}, err
	}
	return Version{
		ID: id, WorkspaceID: wsID, Number: next, Message: message, Author: author,
		RestoredFrom: restoredFrom, CreatedAt: at, Snapshot: snap,
	}, nil
}

// List returns a workspace's versions, newest first.
//
// Without the snapshots. A list is scanned, and carrying every state of every
// version to draw a list of dates is the same mistake the instruction library
// avoids by not carrying every body.
func (s *Store) List(ctx context.Context, wsID int64) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workspace_id, number, message, author, COALESCE(restored_from, 0), created_at
		  FROM workflow_versions WHERE workspace_id = ? ORDER BY number DESC`, wsID)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()

	out := []Version{}
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.ID, &v.WorkspaceID, &v.Number, &v.Message,
			&v.Author, &v.RestoredFrom, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("read a version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Get returns one version with its snapshot.
func (s *Store) Get(ctx context.Context, wsID int64, number int) (Version, error) {
	var v Version
	var body string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, number, message, author, COALESCE(restored_from, 0), created_at, snapshot
		  FROM workflow_versions WHERE workspace_id = ? AND number = ?`, wsID, number).
		Scan(&v.ID, &v.WorkspaceID, &v.Number, &v.Message, &v.Author, &v.RestoredFrom, &v.CreatedAt, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNoVersion
	}
	if err != nil {
		return Version{}, fmt.Errorf("read version %d: %w", number, err)
	}
	if err := json.Unmarshal([]byte(body), &v.Snapshot); err != nil {
		return Version{}, fmt.Errorf("version %d is not readable: %w", number, err)
	}
	return v, nil
}

// Latest is the most recent version, or ErrNoVersion when there is none.
//
// Used to refuse a save that would record nothing new — a list where half the
// entries are identical is a list nobody reads.
func (s *Store) Latest(ctx context.Context, wsID int64) (Version, error) {
	var number int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(number), 0) FROM workflow_versions WHERE workspace_id = ?`, wsID).Scan(&number)
	if err != nil {
		return Version{}, err
	}
	if number == 0 {
		return Version{}, ErrNoVersion
	}
	return s.Get(ctx, wsID, number)
}

func nullIfZero(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
