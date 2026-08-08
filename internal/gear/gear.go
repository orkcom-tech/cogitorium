// Package gear owns the gear catalog: tools agents forge for themselves,
// which persist in the environment instead of evaporating at the end of a
// run. A gear is registered the moment it is forged, but stays inert until
// the operator approves that exact version.
package gear

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
)

var (
	ErrNotFound = catalog.ErrNotFound
	ErrConflict = catalog.ErrConflict
	// ErrNotApproved is returned when an agent calls a gear the operator
	// has not approved (or has disabled).
	ErrNotApproved = errors.New("gear is not approved for execution")
)

// nameRe keeps gear names usable as tool names across providers.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,48}$`)

const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDisabled = "disabled"
)

type Gear struct {
	ID                int64    `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Tags              []string `json:"tags"`
	OriginWorkspaceID *int64   `json:"origin_workspace_id"`
	OriginWorkspace   string   `json:"origin_workspace"`
	CreatedByAgentID  *int64   `json:"created_by_agent_id"`
	CreatedByAgent    string   `json:"created_by_agent"`
	Version           int      `json:"version"`
	Runtime           string   `json:"runtime"`
	Entrypoint        string   `json:"entrypoint"`
	ArgsSchema        string   `json:"args_schema"`
	Status            string   `json:"status"`
	UpdatedAt         string   `json:"updated_at"`
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Binding struct {
	ID          int64  `json:"id"`
	GearID      int64  `json:"gear_id"`
	GearName    string `json:"gear_name"`
	WorkspaceID int64  `json:"workspace_id"`
	AgentID     *int64 `json:"agent_id"` // nil = every agent in the workspace
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func asConflict(err error, what string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%s: %w", what, ErrConflict)
	}
	return err
}

func validRuntime(rt string) bool {
	return rt == "python" || rt == "node" || rt == "bash"
}

// Forge registers a new gear, or supersedes an existing one with a new
// version. A new version always returns to pending: approval covers exact
// content, never a moving target.
func (s *Store) Forge(ctx context.Context, name, description string, tags []string, runtime, entrypoint, argsSchema string, files []File, wsID, agentID int64) (Gear, error) {
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) {
		return Gear{}, fmt.Errorf("gear name %q is invalid: use lowercase letters, digits and underscores (2-49 chars), starting with a letter", name)
	}
	if !validRuntime(runtime) {
		return Gear{}, fmt.Errorf("runtime must be python, node or bash (got %q)", runtime)
	}
	if len(files) == 0 {
		return Gear{}, errors.New("a gear needs at least one file")
	}
	found := false
	for _, f := range files {
		if err := validFilePath(f.Path); err != nil {
			return Gear{}, err
		}
		if f.Path == entrypoint {
			found = true
		}
	}
	if !found {
		return Gear{}, fmt.Errorf("entrypoint %q is not among the provided files", entrypoint)
	}
	if argsSchema == "" {
		argsSchema = "{}"
	}
	// Must be a JSON *object*: the schema is sent to providers as the tool's
	// input_schema, and a non-object (array, string, number) makes them
	// reject every request of every agent the gear is bound to.
	var schemaObj map[string]any
	if err := json.Unmarshal([]byte(argsSchema), &schemaObj); err != nil {
		return Gear{}, fmt.Errorf("args_schema must be a JSON Schema object: %w", err)
	}
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return Gear{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Gear{}, fmt.Errorf("forge gear: begin: %w", err)
	}
	defer tx.Rollback()

	var id int64
	var version int
	err = tx.QueryRowContext(ctx, `SELECT id, version FROM gears WHERE name = ?`, name).Scan(&id, &version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		version = 1
		res, err := tx.ExecContext(ctx, `
			INSERT INTO gears (name, description, tags, origin_workspace_id, created_by_agent_id,
			                   version, runtime, entrypoint, args_schema, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, 'pending', ?, ?)`,
			name, description, string(tagsJSON), wsID, agentID, runtime, entrypoint, argsSchema, now(), now())
		if err := asConflict(err, fmt.Sprintf("gear %q", name)); err != nil {
			return Gear{}, fmt.Errorf("forge gear: %w", err)
		}
		id, _ = res.LastInsertId()
	case err != nil:
		return Gear{}, fmt.Errorf("forge gear: look up %q: %w", name, err)
	default:
		version++
		if _, err := tx.ExecContext(ctx, `
			UPDATE gears SET description = ?, tags = ?, version = ?, runtime = ?, entrypoint = ?,
			                 args_schema = ?, status = 'pending', updated_at = ?
			WHERE id = ?`,
			description, string(tagsJSON), version, runtime, entrypoint, argsSchema, now(), id); err != nil {
			return Gear{}, fmt.Errorf("forge gear: update %q: %w", name, err)
		}
	}

	for _, f := range files {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO gear_files (gear_id, version, path, content) VALUES (?, ?, ?, ?)`,
			id, version, f.Path, f.Content); err != nil {
			return Gear{}, fmt.Errorf("forge gear: store file %q: %w", f.Path, err)
		}
	}

	// The forging agent binds the gear to itself automatically.
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO gear_bindings (gear_id, workspace_id, agent_id, created_at) VALUES (?, ?, ?, ?)`,
		id, wsID, agentID, now()); err != nil {
		return Gear{}, fmt.Errorf("forge gear: self-bind: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Gear{}, fmt.Errorf("forge gear: commit: %w", err)
	}
	slog.Info("gear forged", "id", id, "name", name, "version", version, "runtime", runtime,
		"workspace_id", wsID, "agent_id", agentID, "status", "pending")
	return s.Get(ctx, id)
}

func validFilePath(p string) error {
	if p == "" {
		return errors.New("gear file path is required")
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.Contains(p, `\`) {
		return fmt.Errorf("gear file path %q must be relative and must not escape the gear directory", p)
	}
	return nil
}

const gearSelect = `
	SELECT g.id, g.name, g.description, g.tags, g.origin_workspace_id,
	       COALESCE(w.name, ''), g.created_by_agent_id, COALESCE(a.name, ''),
	       g.version, g.runtime, g.entrypoint, g.args_schema, g.status, g.updated_at
	FROM gears g
	LEFT JOIN workspaces w ON w.id = g.origin_workspace_id
	LEFT JOIN agents a ON a.id = g.created_by_agent_id`

func scanGear(row interface{ Scan(...any) error }) (Gear, error) {
	var g Gear
	var tags string
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &tags, &g.OriginWorkspaceID, &g.OriginWorkspace,
		&g.CreatedByAgentID, &g.CreatedByAgent, &g.Version, &g.Runtime, &g.Entrypoint,
		&g.ArgsSchema, &g.Status, &g.UpdatedAt); err != nil {
		return Gear{}, err
	}
	if err := json.Unmarshal([]byte(tags), &g.Tags); err != nil {
		slog.Warn("gear has unparseable tags", "gear", g.Name, "err", err)
		g.Tags = []string{}
	}
	return g, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Gear, error) {
	g, err := scanGear(s.db.QueryRowContext(ctx, gearSelect+` WHERE g.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Gear{}, fmt.Errorf("gear %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Gear{}, fmt.Errorf("get gear %d: %w", id, err)
	}
	return g, nil
}

func (s *Store) GetByName(ctx context.Context, name string) (Gear, error) {
	g, err := scanGear(s.db.QueryRowContext(ctx, gearSelect+` WHERE g.name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Gear{}, fmt.Errorf("gear %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Gear{}, fmt.Errorf("get gear %q: %w", name, err)
	}
	return g, nil
}

// List returns the whole catalog, optionally filtered by a tag or a
// free-text query over name and description.
func (s *Store) List(ctx context.Context, tag, query string) ([]Gear, error) {
	rows, err := s.db.QueryContext(ctx, gearSelect+` ORDER BY g.name`)
	if err != nil {
		return nil, fmt.Errorf("list gears: %w", err)
	}
	defer rows.Close()

	out := []Gear{}
	for rows.Next() {
		g, err := scanGear(rows)
		if err != nil {
			return nil, fmt.Errorf("scan gear: %w", err)
		}
		if tag != "" && !slicesContainsFold(g.Tags, tag) {
			continue
		}
		if query != "" {
			q := strings.ToLower(query)
			if !strings.Contains(strings.ToLower(g.Name), q) && !strings.Contains(strings.ToLower(g.Description), q) {
				continue
			}
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func slicesContainsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

func (s *Store) Files(ctx context.Context, gearID int64, version int) ([]File, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, content FROM gear_files WHERE gear_id = ? AND version = ? ORDER BY path`, gearID, version)
	if err != nil {
		return nil, fmt.Errorf("gear %d files: %w", gearID, err)
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Path, &f.Content); err != nil {
			return nil, fmt.Errorf("scan gear file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetStatus approves or disables a gear. Approval is the operator's act —
// nothing else may call it.
func (s *Store) SetStatus(ctx context.Context, id int64, status string) (Gear, error) {
	if status != StatusApproved && status != StatusDisabled && status != StatusPending {
		return Gear{}, fmt.Errorf("unknown gear status %q", status)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE gears SET status = ?, updated_at = ? WHERE id = ?`, status, now(), id)
	if err != nil {
		return Gear{}, fmt.Errorf("set gear %d status: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Gear{}, fmt.Errorf("gear %d: %w", id, ErrNotFound)
	}
	slog.Info("gear status changed by operator", "gear_id", id, "status", status)
	return s.Get(ctx, id)
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM gears WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete gear %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("gear %d: %w", id, ErrNotFound)
	}
	slog.Info("gear deleted", "gear_id", id)
	return nil
}

func (s *Store) Bind(ctx context.Context, gearID, wsID int64, agentID *int64) (Binding, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO gear_bindings (gear_id, workspace_id, agent_id, created_at) VALUES (?, ?, ?, ?)`,
		gearID, wsID, agentID, now())
	if err := asConflict(err, "gear binding"); err != nil {
		return Binding{}, fmt.Errorf("bind gear: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("gear bound", "binding_id", id, "gear_id", gearID, "workspace_id", wsID, "agent_id", agentID)
	return Binding{ID: id, GearID: gearID, WorkspaceID: wsID, AgentID: agentID}, nil
}

func (s *Store) Unbind(ctx context.Context, bindingID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM gear_bindings WHERE id = ?`, bindingID)
	if err != nil {
		return fmt.Errorf("unbind gear %d: %w", bindingID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("gear binding %d: %w", bindingID, ErrNotFound)
	}
	slog.Info("gear unbound", "binding_id", bindingID)
	return nil
}

func (s *Store) ListBindings(ctx context.Context, wsID int64) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id, b.gear_id, g.name, b.workspace_id, b.agent_id
		FROM gear_bindings b JOIN gears g ON g.id = b.gear_id
		WHERE b.workspace_id = ? ORDER BY g.name`, wsID)
	if err != nil {
		return nil, fmt.Errorf("list gear bindings: %w", err)
	}
	defer rows.Close()
	out := []Binding{}
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.ID, &b.GearID, &b.GearName, &b.WorkspaceID, &b.AgentID); err != nil {
			return nil, fmt.Errorf("scan gear binding: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ForAgent returns the approved gears an agent may call: those bound to the
// whole workspace plus those bound to it specifically. Pending and disabled
// gears are never offered.
func (s *Store) ForAgent(ctx context.Context, wsID, agentID int64) ([]Gear, error) {
	rows, err := s.db.QueryContext(ctx, gearSelect+`
		JOIN gear_bindings b ON b.gear_id = g.id
		WHERE b.workspace_id = ? AND (b.agent_id IS NULL OR b.agent_id = ?) AND g.status = 'approved'
		GROUP BY g.id ORDER BY g.name`, wsID, agentID)
	if err != nil {
		return nil, fmt.Errorf("gears for agent %d: %w", agentID, err)
	}
	defer rows.Close()
	out := []Gear{}
	for rows.Next() {
		g, err := scanGear(rows)
		if err != nil {
			return nil, fmt.Errorf("scan gear: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
