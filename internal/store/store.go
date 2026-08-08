// Package store owns the SQLite database: opening, pragmas, and schema
// migrations embedded from migrations/*.sql.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed all:migrations
var migrationsFS embed.FS

// Open creates dataDir if needed, opens the database and applies pending
// migrations.
func Open(dataDir string) (*sql.DB, error) {
	// 0700: the database will hold provider API keys — no other local
	// account has business reading it.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	dbPath := filepath.Join(dataDir, "cogitorium.db")
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	// modernc/sqlite is in-process; a single writer avoids SQLITE_BUSY churn.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", dbPath, err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		slog.Warn("could not tighten database file permissions", "path", dbPath, "err", err)
	}
	slog.Info("database opened", "path", dbPath)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// Track the applied set, not MAX(version): a backfilled lower-numbered
	// migration must still run, and duplicate version numbers are a build
	// error, not something to silently pick between.
	applied := map[int]bool{}
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("scan applied migration: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}

	type mig struct {
		version int
		name    string
	}
	seen := map[int]string{}
	var pending []mig
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		numStr, _, found := strings.Cut(name, "_")
		if !found {
			return fmt.Errorf("migration %s: name must be NNNN_description.sql", name)
		}
		v, err := strconv.Atoi(numStr)
		if err != nil {
			return fmt.Errorf("migration %s: bad version prefix: %w", name, err)
		}
		if prev, dup := seen[v]; dup {
			return fmt.Errorf("duplicate migration version %d: %s and %s", v, prev, name)
		}
		seen[v] = name
		if !applied[v] {
			pending = append(pending, mig{version: v, name: name})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].version < pending[j].version })

	if len(pending) == 0 {
		slog.Info("schema up to date", "applied", len(applied))
		return nil
	}

	for _, m := range pending {
		raw, err := migrationsFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.name, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(string(raw)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
		slog.Info("migration applied", "version", m.version, "name", m.name)
	}
	return nil
}
