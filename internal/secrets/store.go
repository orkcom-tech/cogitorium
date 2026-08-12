package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
)

var (
	ErrNotFound = catalog.ErrNotFound
	// ErrNoKey is the refusal to write a secret with nowhere safe to put it.
	// Separate from any other failure because the fix is one specific act by
	// the operator: start the server with COGITORIUM_SECRET_KEY set.
	ErrNoKey = errors.New("this server has no COGITORIUM_SECRET_KEY, so it cannot store a secret")
)

// Store holds the install's own values: source one for a global name, source
// three for a workspace's override of it.
type Store struct {
	db  *sql.DB
	key *Key
}

// NewStore takes the key at construction rather than per call, so no code path
// can write a secret having simply forgotten to encrypt it. A nil key is
// allowed and means secrets cannot be written here; variables still can.
func NewStore(db *sql.DB, key *Key) *Store { return &Store{db: db, key: key} }

// HasKey reports whether this install can hold secrets of its own. The
// interface asks before offering to set one, so the operator learns what to do
// before typing a credential into a form that will refuse it.
func (s *Store) HasKey() bool { return s.key != nil }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// scope turns the caller's nil-or-workspace into what the database stores and
// into what an error message calls it.
func scope(wsID *int64) (any, string) {
	if wsID == nil {
		return nil, "this install"
	}
	return *wsID, fmt.Sprintf("workspace %d", *wsID)
}

// Set creates or replaces one name in one scope.
//
// An empty value is refused rather than stored. The plan's rule is that a name
// which does not resolve is a clear refusal naming it, never an empty string,
// because an empty credential fails somewhere far away and confusingly — and a
// stored empty value would be exactly that, laundered through a successful
// lookup.
func (s *Store) Set(ctx context.Context, wsID *int64, name, kind, value, description string) (Record, error) {
	name = strings.TrimSpace(name)
	if err := ValidName(name); err != nil {
		return Record{}, err
	}
	if err := ValidKind(kind); err != nil {
		return Record{}, err
	}
	if value == "" {
		return Record{}, fmt.Errorf("%s has no value: a gear given an empty credential fails somewhere far away and confusingly, "+
			"so delete the name instead of emptying it", name)
	}

	stored, keyID := value, ""
	if kind == KindSecret {
		if s.key == nil {
			return Record{}, fmt.Errorf("%w — set it in the environment and restart, or store %s as a variable if its value is not sensitive: %w",
				ErrNoKey, name, ErrNoKey)
		}
		sealed, err := s.key.Seal(value)
		if err != nil {
			return Record{}, err
		}
		stored, keyID = sealed, s.key.ID()
	}

	who, label := scope(wsID)
	// Upsert on the same pair the unique index covers, so setting a name twice
	// replaces it instead of failing — an operator rotating a credential should
	// not have to delete it first.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO env_values (workspace_id, name, kind, value, key_id, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (name, COALESCE(workspace_id, 0)) DO UPDATE SET
			kind = excluded.kind, value = excluded.value, key_id = excluded.key_id,
			description = excluded.description, updated_at = excluded.updated_at`,
		who, name, kind, stored, keyID, description, now(), now())
	if err != nil {
		return Record{}, fmt.Errorf("set %s for %s: %w", name, label, err)
	}
	// Name, kind and scope only. This log line is the audit trail for a
	// credential changing, and it must be safe to ship to wherever logs go.
	slog.Info("named value set", "name", name, "kind", kind, "scope", label)
	return s.get(ctx, wsID, name)
}

// Delete removes one name from one scope. Deleting a workspace override
// uncovers the global value again rather than leaving a hole, which is what
// "narrower scope wins" means when the narrower scope goes away.
func (s *Store) Delete(ctx context.Context, wsID *int64, name string) error {
	who, label := scope(wsID)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM env_values WHERE name = ? AND COALESCE(workspace_id, 0) = COALESCE(?, 0)`, name, who)
	if err != nil {
		return fmt.Errorf("delete %s from %s: %w", name, label, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s is not set for %s: %w", name, label, ErrNotFound)
	}
	slog.Info("named value deleted", "name", name, "scope", label)
	return nil
}

const recordSelect = `SELECT id, workspace_id, name, kind, value, key_id, description, created_at, updated_at FROM env_values`

// List returns one scope's names, with secret values withheld. It is what the
// interface reads, so withholding happens here rather than being left to every
// handler to remember.
func (s *Store) List(ctx context.Context, wsID *int64) ([]Record, error) {
	who, label := scope(wsID)
	rows, err := s.db.QueryContext(ctx,
		recordSelect+` WHERE COALESCE(workspace_id, 0) = COALESCE(?, 0) ORDER BY name`, who)
	if err != nil {
		return nil, fmt.Errorf("list the names set for %s: %w", label, err)
	}
	defer rows.Close()

	out := []Record{}
	for rows.Next() {
		r, _, _, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("read a named value of %s: %w", label, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) get(ctx context.Context, wsID *int64, name string) (Record, error) {
	who, label := scope(wsID)
	r, _, _, err := scanRow(s.db.QueryRowContext(ctx,
		recordSelect+` WHERE name = ? AND COALESCE(workspace_id, 0) = COALESCE(?, 0)`, name, who))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%s is not set for %s: %w", name, label, ErrNotFound)
	}
	if err != nil {
		return Record{}, fmt.Errorf("read %s for %s: %w", name, label, err)
	}
	return r, nil
}

// scanRow reads one row into the shape safe to hand out, and returns the raw
// stored value and key id alongside it for the resolver, which is the only
// caller entitled to open a secret.
func scanRow(row interface{ Scan(...any) error }) (Record, string, string, error) {
	var r Record
	var stored, keyID string
	if err := row.Scan(&r.ID, &r.WorkspaceID, &r.Name, &r.Kind, &stored, &keyID,
		&r.Description, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Record{}, "", "", err
	}
	if r.Kind == KindVariable {
		r.Value = stored
	}
	return r, stored, keyID, nil
}

// sealed is one row as the resolver needs it: still encrypted, so that a name
// nobody asked for is never decrypted, and a row sealed under a key this server
// no longer has cannot fail a run that did not want it.
type sealed struct {
	kind   string
	stored string
	keyID  string
}

// load reads one scope's rows into a map, without opening anything.
func (s *Store) load(ctx context.Context, wsID *int64) (map[string]sealed, error) {
	who, label := scope(wsID)
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, kind, value, key_id FROM env_values WHERE COALESCE(workspace_id, 0) = COALESCE(?, 0)`, who)
	if err != nil {
		return nil, fmt.Errorf("read the names set for %s: %w", label, err)
	}
	defer rows.Close()

	out := map[string]sealed{}
	for rows.Next() {
		var name string
		var v sealed
		if err := rows.Scan(&name, &v.kind, &v.stored, &v.keyID); err != nil {
			return nil, fmt.Errorf("read a named value of %s: %w", label, err)
		}
		out[name] = v
	}
	return out, rows.Err()
}

// open turns one stored row into a usable value.
func (s *Store) open(name string, v sealed) (string, error) {
	if v.kind != KindSecret {
		return v.stored, nil
	}
	if s.key == nil {
		return "", fmt.Errorf("%s is a secret held in this install's store, and this server was started without COGITORIUM_SECRET_KEY, so it cannot be read: "+
			"set that environment variable to the key this install uses and restart", name)
	}
	plain, err := s.key.Open(v.stored, v.keyID)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return plain, nil
}

// CountSealed reports how many secrets are held in the database. Startup asks,
// so an install whose key went missing says so at once instead of at the first
// gear run — by which time whoever changed the environment has gone home.
func (s *Store) CountSealed(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM env_values WHERE kind = ?`, KindSecret).Scan(&n); err != nil {
		return 0, fmt.Errorf("count the stored secrets: %w", err)
	}
	return n, nil
}
