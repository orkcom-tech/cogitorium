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
