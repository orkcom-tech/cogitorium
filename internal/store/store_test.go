package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The on-disk name is a compatibility contract: every existing install's data
// lives at this path inside the data dir. Renaming it silently starts everyone
// over on an empty database instead of failing loudly.
const dbFileName = "cogitorium.db"

func openTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	// A nested path that does not exist yet: Open promises to create the data
	// dir, parents included.
	dir := filepath.Join(t.TempDir(), "state", "cogitorium")
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dir
}

// rawOpen bypasses the package under test to look at the file the way another
// process would: no pragmas, no migrations, no pool limit.
func rawOpen(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFileName))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatalf("looking for table %s: %v", name, err)
	}
	return n > 0
}

func appliedVersions(t *testing.T, db *sql.DB) map[int]string {
	t.Helper()
	rows, err := db.Query(`SELECT version, applied_at FROM schema_migrations`)
	if err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	defer rows.Close()
	got := map[int]string{}
	for rows.Next() {
		var v int
		var at string
		if err := rows.Scan(&v, &at); err != nil {
			t.Fatalf("scan schema_migrations: %v", err)
		}
		got[v] = at
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	return got
}

// migrationFileNames lists the shipped .sql files. Read straight off the
// embedded FS rather than through any of the package's parsing, so the test
// compares "what shipped" against "what the code recorded".
func migrationFileNames(t *testing.T) []string {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatal("no migrations are embedded at all; the go:embed directive is broken")
	}
	return names
}

// Open rejects a duplicate version number and a name without an NNNN_ prefix,
// but it does so at startup on the operator's machine — the migration set is
// embedded, so no test can feed it a bad one. This is the same rule enforced
// one step earlier, against the files actually shipping: a duplicate version or
// a stray name means every install fails to start after the upgrade.
func TestShippedMigrationsAreWellFormedAndUniquelyNumbered(t *testing.T) {
	t.Parallel()
	wellFormed := regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

	byVersion := map[int]string{}
	for _, name := range migrationFileNames(t) {
		m := wellFormed.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("migration %q is not NNNN_lower_snake_description.sql; Open refuses to start on it", name)
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil || v < 1 {
			t.Errorf("migration %q has version %q, which is not a positive number", name, m[1])
			continue
		}
		if prev, dup := byVersion[v]; dup {
			t.Errorf("migrations %q and %q share version %d; Open treats that as a build error and refuses to start", prev, name, v)
			continue
		}
		byVersion[v] = name
	}
}

// A migration that ships but never runs is the worst failure mode here: the
// binary starts, then every query against the missing table fails at runtime,
// far from the cause.
func TestOpenFreshDataDirAppliesEveryShippedMigration(t *testing.T) {
	t.Parallel()
	db, dir := openTestDB(t)

	if _, err := os.Stat(filepath.Join(dir, dbFileName)); err != nil {
		t.Fatalf("no database at %s/%s after Open: %v", dir, dbFileName, err)
	}

	applied := appliedVersions(t, db)
	files := migrationFileNames(t)
	if len(applied) != len(files) {
		t.Errorf("%d migrations shipped but %d recorded as applied (%v vs %v)",
			len(files), len(applied), files, applied)
	}
	for _, name := range files {
		// 0007_context_branches.sql -> 7
		var v int
		if _, err := fmt.Sscanf(name, "%04d_", &v); err != nil {
			t.Fatalf("migration %s does not match NNNN_description.sql: %v", name, err)
		}
		if _, ok := applied[v]; !ok {
			t.Errorf("migration %s shipped but version %d was never recorded as applied", name, v)
		}
	}

	// A literal floor rather than "whatever is on disk": these fourteen exist
	// today, and a change that stops applying one of them is a data-loss bug,
	// not a refactor. New migrations extend this without touching the test.
	for v := 1; v <= 14; v++ {
		if _, ok := applied[v]; !ok {
			t.Errorf("migration version %d is not recorded as applied on a fresh database", v)
		}
	}

	for v, at := range applied {
		ts, err := time.Parse(time.RFC3339, at)
		if err != nil {
			t.Errorf("version %d recorded applied_at %q, which is not RFC3339: %v", v, at, err)
			continue
		}
		if !strings.HasSuffix(at, "Z") {
			// Database files get copied between machines and into containers.
			// A local-offset stamp sorts wrong against a UTC one and misdates
			// the upgrade history.
			t.Errorf("version %d recorded applied_at %q; migration stamps must be UTC", v, at)
		}
		if time.Since(ts) > time.Hour || time.Until(ts) > time.Minute {
			t.Errorf("version %d recorded applied_at %q, nowhere near now", v, at)
		}
	}
}

// The schema itself, asserted by name. If a migration is dropped, reordered or
// quietly emptied, the migration bookkeeping can still look right while the
// tables the product queries are gone.
func TestFreshSchemaContainsTheTablesTheProductQueries(t *testing.T) {
	t.Parallel()
	db, _ := openTestDB(t)

	for _, table := range []string{
		"providers", "models", // 0001
		"workspaces", "agents", "wires", "ws_messages", // 0002
		"context_bindings",                     // 0003
		"gears", "gear_files", "gear_bindings", // 0005
		"gear_runs",                                // 0006
		"users", "teams", "team_members", "tokens", // 0008
		"instructions",                   // 0011
		"agent_usage",                    // 0012
		"agent_egress", "search_queries", // 0013
		"workspace_teams", // 0014
	} {
		if !tableExists(t, db, table) {
			t.Errorf("table %q is missing from a freshly migrated database", table)
		}
	}
}

// ALTER-only migrations add nothing that a table-name check would notice: drop
// 0009 and login still "works" until someone tries to set a password.
func TestFreshSchemaContainsTheColumnsLaterMigrationsAdd(t *testing.T) {
	t.Parallel()
	db, _ := openTestDB(t)

	for _, tc := range []struct {
		table, column, addedBy string
	}{
		{"agents", "pos_x", "0004"},
		{"agents", "pos_y", "0004"},
		{"gears", "timeout_seconds", "0006"},
		{"workspaces", "branch", "0007"},
		{"agents", "branch", "0007"},
		{"workspaces", "owner_id", "0008"},
		{"workspaces", "team_id", "0008"},
		{"users", "password_hash", "0009"},
		{"gear_files", "encoding", "0010"},
	} {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, tc.table)
		if err != nil {
			t.Fatalf("table_info(%s): %v", tc.table, err)
		}
		found := false
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan table_info(%s): %v", tc.table, err)
			}
			if name == tc.column {
				found = true
			}
		}
		rows.Close()
		if !found {
			t.Errorf("%s.%s is missing; migration %s did not take effect", tc.table, tc.column, tc.addedBy)
		}
	}
}

// 0010 rebuilds the gears table to widen a CHECK constraint. A rebuild that
// half-happens is invisible to a table-name check, so assert the constraint the
// rebuild exists to change: binary gears must be insertable, and the constraint
// must still reject a runtime nobody can execute.
func TestGearRuntimeCheckWasWidenedToBinaryAndStillRejectsUnknownRuntimes(t *testing.T) {
	t.Parallel()
	db, _ := openTestDB(t)

	if _, err := db.Exec(
		`INSERT INTO gears (name, runtime, entrypoint, created_at, updated_at)
		 VALUES ('compiled', 'binary', './tool', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Errorf("a 'binary' gear was rejected, so migration 0010 did not rebuild gears: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO gears (name, runtime, entrypoint, created_at, updated_at)
		 VALUES ('bogus', 'ruby', './tool', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err == nil {
		t.Error("a gear with runtime 'ruby' was accepted; the executor cannot run it and the CHECK is the only thing stopping it")
	}
}

// Startup runs Open every time. If the second run reapplies anything, either
// startup breaks or a rebuild migration like 0010 destroys live data.
func TestReopeningTheSameDataDirAppliesNothing(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO workspaces (name, created_at, updated_at)
		 VALUES ('operator data', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seeding a workspace: %v", err)
	}
	// Stamp every row with a value the production code would never write, so a
	// re-applied migration is visible even if reapplying happened to succeed.
	if _, err := db.Exec(`UPDATE schema_migrations SET applied_at = 'FIRST-RUN'`); err != nil {
		t.Fatalf("stamping schema_migrations: %v", err)
	}
	first := appliedVersions(t, db)
	if err := db.Close(); err != nil {
		t.Fatalf("closing first handle: %v", err)
	}

	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open on an already-migrated data dir: %v", err)
	}
	defer db2.Close()

	second := appliedVersions(t, db2)
	if len(second) != len(first) {
		t.Errorf("reopening changed the applied set: %d rows before, %d after", len(first), len(second))
	}
	for v := range first {
		at, ok := second[v]
		if !ok {
			t.Errorf("version %d disappeared from schema_migrations on reopen", v)
			continue
		}
		if at != "FIRST-RUN" {
			t.Errorf("version %d was re-recorded on reopen (applied_at is now %q); the migration ran twice", v, at)
		}
	}

	var name string
	if err := db2.QueryRow(`SELECT name FROM workspaces`).Scan(&name); err != nil {
		t.Fatalf("operator data did not survive reopening: %v", err)
	}
	if name != "operator data" {
		t.Errorf("workspace name is %q after reopen; the row was rewritten", name)
	}
}

// The package documents that it tracks the applied SET, not MAX(version),
// because branches merge out of order: a teammate's later migration can land
// first, and the earlier one must still run rather than being skipped forever.
func TestAMigrationBelowTheHighestRecordedVersionStillRuns(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	seed := rawOpen(t, dir)
	if _, err := seed.Exec(
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("seeding schema_migrations: %v", err)
	}
	// A version above every shipped migration: under MAX(version) logic every
	// real migration looks already-applied and the database stays empty.
	if _, err := seed.Exec(
		`INSERT INTO schema_migrations (version, applied_at) VALUES (9999, 'from a newer branch')`); err != nil {
		t.Fatalf("seeding a future version: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("closing seed handle: %v", err)
	}

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open with a future version recorded: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"providers", "workspaces", "users", "workspace_teams"} {
		if !tableExists(t, db, table) {
			t.Errorf("table %q was never created: a migration below the highest recorded version was skipped", table)
		}
	}
	applied := appliedVersions(t, db)
	if _, ok := applied[9999]; !ok {
		t.Error("the pre-seeded version 9999 vanished, so this test never exercised what it claims to")
	}
	if _, ok := applied[1]; !ok {
		t.Error("version 1 was not recorded even though its tables were expected")
	}
}

// modernc/sqlite runs in-process and the whole codebase assumes exactly one
// connection. Raise the cap and the symptom is not a test failure, it is
// intermittent "database is locked" in production.
func TestThePoolIsCappedAtASingleConnection(t *testing.T) {
	t.Parallel()
	db, _ := openTestDB(t)

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections is %d; this package must hand out exactly 1 connection", got)
	}

	// Behavioural half: hold the only connection in a transaction, then ask the
	// pool for another. With a cap of one the caller must wait (and here, time
	// out). Without the cap, SQLite happily opens a second reader in WAL mode
	// and this returns instantly.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var n int
	err = db.QueryRowContext(ctx, `SELECT 1`).Scan(&n)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a query issued while a transaction held the connection returned %v (n=%d); "+
			"a second connection was available, so the single-writer guarantee is gone", err, n)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// And the pool recovers once the transaction lets go, so the assertion
	// above is about contention and not about a wedged handle.
	if err := db.QueryRow(`SELECT 1`).Scan(&n); err != nil {
		t.Fatalf("query after rollback: %v", err)
	}
}

func TestConnectionPragmasAreActuallyInEffect(t *testing.T) {
	t.Parallel()
	db, dir := openTestDB(t)

	var journal string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Errorf("journal_mode is %q, not WAL; readers and the writer will now block each other", journal)
	}
	// WAL is also a fact about the files on disk, which is what a backup script
	// or a second process sees.
	if _, err := os.Stat(filepath.Join(dir, dbFileName+"-wal")); err != nil {
		t.Errorf("no %s-wal alongside the database, so WAL is not in use: %v", dbFileName, err)
	}

	var busy int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busy != 5000 {
		t.Errorf("busy_timeout is %dms, not 5000ms; a contended write fails instead of waiting", busy)
	}

	var fks int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fks); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fks != 1 {
		t.Errorf("foreign_keys is %d; SQLite ignores every REFERENCES clause in the migrations unless it is 1", fks)
	}
}

// Foreign keys are off by default in SQLite and every ON DELETE CASCADE in the
// migrations is decoration without the pragma. Assert the effect, not the
// setting: an agent pointing at a workspace that does not exist must be refused.
func TestForeignKeysAreEnforcedNotJustRequested(t *testing.T) {
	t.Parallel()
	db, _ := openTestDB(t)

	if _, err := db.Exec(
		`INSERT INTO agents (workspace_id, name, created_at, updated_at)
		 VALUES (424242, 'orphan', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err == nil {
		t.Error("an agent was inserted into a workspace that does not exist; foreign keys are not being enforced")
	}

	// And the cascade the schema promises actually fires.
	res, err := db.Exec(
		`INSERT INTO workspaces (name, created_at, updated_at)
		 VALUES ('doomed', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	wsID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agents (workspace_id, name, created_at, updated_at)
		 VALUES (?, 'child', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM workspaces WHERE id = ?`, wsID); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	var orphans int
	if err := db.QueryRow(`SELECT count(*) FROM agents WHERE workspace_id = ?`, wsID).Scan(&orphans); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d agents outlived their deleted workspace; ON DELETE CASCADE never ran", orphans)
	}
}

// The database holds provider API keys in cleartext (providers.api_key) and
// token hashes. Any other local account being able to read it is the whole
// problem.
func TestDataDirAndDatabaseAreReadableOnlyByTheOwner(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	_, dir := openTestDB(t)

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if got := di.Mode().Perm(); got != fs.FileMode(0o700) {
		t.Errorf("data dir mode is %04o, want 0700: group and other must not be able to list or enter it", got)
	}

	fi, err := os.Stat(filepath.Join(dir, dbFileName))
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if got := fi.Mode().Perm(); got != fs.FileMode(0o600) {
		t.Errorf("database mode is %04o, want 0600: it stores provider API keys in cleartext", got)
	}
}

// A database that is missing tables must stop the process at startup. Starting
// anyway means the operator finds out from a 500 in the middle of a turn.
func TestOpenRefusesToReturnAHandleWhenAMigrationFails(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Occupy the name of the SECOND table 0001 creates, so its first statement
	// succeeds and its second fails partway through the migration.
	seed := rawOpen(t, dir)
	if _, err := seed.Exec(`CREATE TABLE models (squatter TEXT)`); err != nil {
		t.Fatalf("seeding a conflicting table: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("closing seed handle: %v", err)
	}

	db, err := Open(dir)
	if err == nil {
		db.Close()
		t.Fatal("Open succeeded on a database where migration 0001 cannot apply")
	}
	if db != nil {
		db.Close()
		t.Error("Open returned both an error and a usable handle; the caller will use it")
	}

	// A migration runs in a transaction, so its earlier statements must not
	// survive its failure. A half-applied migration can never be re-applied and
	// leaves the install permanently unstartable.
	after := rawOpen(t, dir)
	if tableExists(t, after, "providers") {
		t.Error("the 'providers' table from the first half of migration 0001 outlived the failure; " +
			"migrations are not running in a transaction")
	}
	var recorded int
	if err := after.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&recorded); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if recorded != 0 {
		t.Errorf("%d migrations were recorded as applied even though migration 0001 failed", recorded)
	}
}

func TestOpenReportsAnErrorWhenTheDataDirCannotBeCreated(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := Open(filepath.Join(blocker, "state"))
	if err == nil {
		db.Close()
		t.Fatal("Open succeeded with a regular file in place of the data dir's parent")
	}
	if db != nil {
		db.Close()
		t.Error("Open returned a handle alongside its error")
	}
	if !strings.Contains(err.Error(), blocker) {
		t.Errorf("error %q does not name the path that could not be created; the operator cannot act on it", err)
	}
}
