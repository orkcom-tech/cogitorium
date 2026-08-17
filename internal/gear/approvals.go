package gear

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// The approval trail: who said yes, when, to which version, and with what.
//
// Approving a gear is the most consequential act in this product — a human
// saying that code an agent wrote may run on this machine, with these
// credentials, reaching these hosts. Until now it left exactly one trace: a
// status column reading "approved". That column answers "is it approved" and
// nothing else, and none of the questions actually asked afterwards:
//
//	Who approved it. On an install with accounts, "somebody with admin" is not
//	an answer.
//
//	WHICH VERSION they approved. A gear can be edited after approval and keeps
//	its status — the review screen already diffs against the approved version
//	for exactly this reason — so "approved" on a v7 gear may mean somebody read
//	v3. Without the version recorded, nobody can tell.
//
//	What was granted at the same moment. The credentials and the network reach
//	are decided in the same breath as the approval and are most of what makes
//	the decision dangerous; a trail that omits them describes a milder decision
//	than the one that was made.
//
// Append-only by construction: nothing in this file updates or deletes. The
// gear's status column stays the answer to "what is it now"; this is the answer
// to "how did it get that way", and the two are allowed to be read together.

// Approval is one decision about one gear.
type Approval struct {
	ID       int64  `json:"id"`
	GearID   int64  `json:"gear_id"`
	GearName string `json:"gear_name"`
	Version  int    `json:"version"`
	Status   string `json:"status"`
	// UserName is empty when the actor could not be attributed — a
	// single-operator install, or a user deleted since. The decision still
	// happened, and a row with no name is more honest than no row.
	UserID    *int64 `json:"user_id"`
	UserName  string `json:"user_name"`
	EnvNames  string `json:"env_names"`
	Network   string `json:"network"`
	CreatedAt string `json:"created_at"`
}

// Actor is who is making the decision, as far as the server can tell.
type Actor struct {
	ID   *int64
	Name string
}

// recordApproval writes the trail row for a decision that has already been
// made.
//
// It reads the gear back INSIDE the same call rather than taking the values
// from the caller, so the row states what was true at the moment of the change
// — the version that was live, the grants that were in force — rather than
// what a caller believed. A caller that passed stale grants would produce a
// trail that is worse than none, because it would be believed.
func (s *Store) recordApproval(ctx context.Context, id int64, status string, by Actor) {
	var (
		name    string
		version int
		env     string
		granted int
		hosts   string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT name, version, COALESCE(env_names, ''), network_granted, COALESCE(network_hosts, '')
		FROM gears WHERE id = ?`, id).Scan(&name, &version, &env, &granted, &hosts)
	if err != nil {
		// A trail row is not worth failing the operator's action for — the
		// decision has already been written. It IS worth a loud line, because
		// a silent gap in an audit trail is the worst of both.
		slog.Error("gear approval trail not written", "gear_id", id, "status", status, "err", err)
		return
	}

	network := "no network"
	if granted == 1 {
		network = "the network, anywhere"
		if strings.TrimSpace(hosts) != "" {
			network = hosts
		}
	}

	var userID any
	if by.ID != nil {
		userID = *by.ID
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO gear_approvals (gear_id, gear_name, version, status, user_id, user_name,
		                            env_names, network, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, version, status, userID, by.Name, env, network, now()); err != nil {
		slog.Error("gear approval trail not written", "gear_id", id, "status", status, "err", err)
		return
	}
	slog.Info("gear decision recorded", "gear_id", id, "gear", name, "version", version,
		"status", status, "by", by.Name, "env", env, "network", network)
}

// Approvals returns a gear's decision history, newest first.
func (s *Store) Approvals(ctx context.Context, gearID int64) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(gear_id, 0), gear_name, version, status, user_id, user_name,
		       env_names, network, created_at
		FROM gear_approvals WHERE gear_id = ? ORDER BY id DESC`, gearID)
	if err != nil {
		return nil, fmt.Errorf("list gear approvals: %w", err)
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		var a Approval
		var uid sql.NullInt64
		if err := rows.Scan(&a.ID, &a.GearID, &a.GearName, &a.Version, &a.Status, &uid,
			&a.UserName, &a.EnvNames, &a.Network, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan gear approval: %w", err)
		}
		if uid.Valid {
			v := uid.Int64
			a.UserID = &v
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ApprovedVersion is the version the most recent approval covered, and whether
// there has ever been one.
//
// It is what tells "approved" apart from "approved, then edited": a gear whose
// status is approved at v7 while the last approval names v3 is running code
// nobody has read. The review screen already diffs those versions; this is the
// same fact available to anything that cannot open a screen.
func (s *Store) ApprovedVersion(ctx context.Context, gearID int64) (int, bool, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `
		SELECT version FROM gear_approvals
		WHERE gear_id = ? AND status = 'approved' ORDER BY id DESC LIMIT 1`, gearID).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("approved version of gear %d: %w", gearID, err)
	}
	return v, true, nil
}
