// Package settings remembers answers an operator gave in the interface.
//
// It is deliberately tiny and deliberately not the config file. config.yaml is
// only ever read — on Kubernetes it is a ConfigMap, and a product that rewrote
// its own ConfigMap would be fighting whatever applies the chart — so a setting
// somebody changes from a browser has nowhere else to live.
//
// The relationship between the two is a CEILING and an ANSWER. The file says
// what an install is permitted to do; a row here says what an operator chose
// within that. A row can never lift a restriction the file imposed, and that
// rule is enforced by whoever owns the setting rather than here: see
// update.Checker.SetMode, which refuses to leave "off" no matter what is
// stored.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Keys. Declared here rather than at each call site so a typo is a compile
// error instead of a setting that silently never loads.
const (
	// KeyUpdateCheck is "ask" | "on" | "off" — see internal/update.
	KeyUpdateCheck = "update_check"
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Get returns the stored value, or "" when nothing has been stored.
//
// A missing row is NOT an error, and that is the whole point of this signature:
// "nobody has answered" is the ordinary state of a fresh install, and a caller
// forced to distinguish sql.ErrNoRows from a real failure would eventually get
// it wrong in the direction of treating a broken database as an unanswered
// question.
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read setting %q: %w", key, err)
	}
	return v, nil
}

// Set stores an answer, replacing whatever was there.
func (s *Store) Set(ctx context.Context, key, value string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("store setting %q: %w", key, err)
	}
	slog.Info("setting stored", "key", key, "value", value)
	return nil
}

// OrchestratorSecrets is whether the orchestrator may read and write the
// operator's named values.
//
// Absent means on, which is the one default in this file that grants rather
// than withholds — an orchestrator that can build an agent and forge a gear
// but cannot give either the credential they need is an orchestrator that
// hands the job back at the last step.
//
// What it costs is stated rather than hidden: reading a secret puts its
// plaintext into a model's context, and no design removes that. Setting this
// to "off" is how an operator who would rather type credentials themselves
// says so.
const OrchestratorSecrets = "orchestrator_secrets"

// OrchestratorModel is the model a new workspace's orchestrator thinks with.
//
// Every workspace is created with an orchestrator — that is not optional — but
// the only place that was visible was a picker on the new-workspace form, and
// somebody looking for where an orchestrator comes from could not find one.
// Setting this makes the answer a thing on a screen: the role the product
// already wrote, with a model in it, and every workspace made afterwards
// starting from that rather than from a question.
//
// Stored as the model's id in decimal. Absent means nobody has chosen, and the
// new-workspace form asks as it always did.
const OrchestratorModel = "orchestrator_model"
