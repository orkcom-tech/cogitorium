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
	"github.com/orkcom-tech/cogitorium/internal/gearnet"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
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
	// EnvNames are the named values this gear asks to be given at run time.
	// Names only — a value never reaches this struct, which is what makes a
	// Gear safe to marshal into an API response, a log line and a bundle.
	EnvNames []string `json:"env_names"`
	// NetworkGranted and NetworkHosts are the other half of what the operator
	// decides at approval: whether this code may reach out, and where. They are
	// not declared by the gear and cannot be — an agent asking for the network
	// on its own behalf is the one request the forging side has no standing to
	// make. Empty NetworkHosts with the grant on means anywhere, which is a
	// choice the operator is allowed to make.
	NetworkGranted bool     `json:"network_granted"`
	NetworkHosts   []string `json:"network_hosts"`
	// Environment names the kind of container this gear runs in, and is the
	// operator's decision at approval for the same reason the network is: an
	// agent asking for a browser is asking for a machine that renders untrusted
	// pages. Empty is the ordinary sandbox image; "browser" resolves to the
	// install's browser_image.
	Environment    string `json:"environment"`
	Status         string `json:"status"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	UpdatedAt      string `json:"updated_at"`
}

// Run is one recorded execution of a gear.
type Run struct {
	ID int64 `json:"id"`
	// GearID is 0 once the gear has been deleted. GearName is what is left,
	// and it is why the run is still readable: see migration 0029.
	GearID   int64  `json:"gear_id"`
	GearName string `json:"gear_name"`
	Version     int    `json:"version"`
	AgentID     *int64 `json:"agent_id"` // nil = the operator (dry run)
	AgentName   string `json:"agent_name"`
	WorkspaceID *int64 `json:"workspace_id"`
	Args        string `json:"args"`
	ExitCode    int    `json:"exit_code"`
	TimedOut    bool   `json:"timed_out"`
	DurationMs  int64  `json:"duration_ms"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	CreatedAt   string `json:"created_at"`
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Encoding is "utf8" for source and "base64" for uploaded binaries.
	// Review shows source as source and refuses to render a blob as code.
	Encoding string `json:"encoding"`
}

const (
	EncodingUTF8   = "utf8"
	EncodingBase64 = "base64"
)

// IsBinary reports whether the file's content is base64-encoded bytes
// rather than readable source.
func (f File) IsBinary() bool { return f.Encoding == EncodingBase64 }

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
	return rt == "python" || rt == "node" || rt == "bash" || rt == RuntimeBinary
}

// RuntimeBinary executes the entrypoint directly instead of handing it to
// an interpreter. The binary must be built for the sandbox container's OS
// and architecture — a macOS build will not run in a Linux container, and
// that is worth saying before someone waits for a confusing exec error.
const RuntimeBinary = "binary"

// DefaultEntrypoint names the single file of a one-file gear, so the forging
// agent does not have to supply a filename it has no opinion about.
func DefaultEntrypoint(runtime string) string {
	switch runtime {
	case "python":
		return "main.py"
	case "node":
		return "main.js"
	default:
		return "main.sh"
	}
}

// Forge registers a new gear, or supersedes an existing one with a new
// version. A new version always returns to pending: approval covers exact
// content, never a moving target. wsID/agentID of 0 mean the operator
// authored it directly rather than an agent forging it.
//
// envNames are the named values the gear asks for — part of forging, like its
// args schema, and part of what returns to pending when the gear changes. A
// gear that asks for a credential it did not ask for last time is a gear the
// operator has not approved.
//
// The network grant returns with it, and for the same reason: approval covers
// exact content, and a grant that survived a new version would be a permission
// given to code nobody has read.
func (s *Store) Forge(ctx context.Context, name, description string, tags []string, runtime, entrypoint, argsSchema string, envNames []string, files []File, wsID, agentID int64) (Gear, error) {
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) {
		return Gear{}, fmt.Errorf("gear name %q is invalid: use lowercase letters, digits and underscores (2-49 chars), starting with a letter", name)
	}
	if !validRuntime(runtime) {
		return Gear{}, fmt.Errorf("runtime must be python, node, bash or %s (got %q)", RuntimeBinary, runtime)
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
	// Judged here, at the doorway, rather than at run time: a gear declaring
	// "api key" would forge cleanly and then fail on every call, and the
	// operator would be reading the wrong file to find out why.
	envNames, err := secrets.NormalizeNames(envNames)
	if err != nil {
		return Gear{}, fmt.Errorf("gear %q asks for a name it cannot have: %w", name, err)
	}
	envJSON, err := json.Marshal(envNames)
	if err != nil {
		return Gear{}, err
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

	// Operator-authored gears (wsID/agentID 0) have no provenance agent.
	var originWS, originAgent any
	if wsID != 0 {
		originWS = wsID
	}
	if agentID != 0 {
		originAgent = agentID
	}

	var id int64
	var version int
	err = tx.QueryRowContext(ctx, `SELECT id, version FROM gears WHERE name = ?`, name).Scan(&id, &version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		version = 1
		res, err := tx.ExecContext(ctx, `
			INSERT INTO gears (name, description, tags, origin_workspace_id, created_by_agent_id,
			                   version, runtime, entrypoint, args_schema, env_names, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, 'pending', ?, ?)`,
			name, description, string(tagsJSON), originWS, originAgent, runtime, entrypoint, argsSchema, string(envJSON), now(), now())
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
			                 args_schema = ?, env_names = ?, network_granted = 0, network_hosts = '[]', environment = '',
			                 status = 'pending', updated_at = ?
			WHERE id = ?`,
			description, string(tagsJSON), version, runtime, entrypoint, argsSchema, string(envJSON), now(), id); err != nil {
			return Gear{}, fmt.Errorf("forge gear: update %q: %w", name, err)
		}
	}

	for _, f := range files {
		encoding := f.Encoding
		if encoding == "" {
			encoding = EncodingUTF8
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO gear_files (gear_id, version, path, content, encoding) VALUES (?, ?, ?, ?, ?)`,
			id, version, f.Path, f.Content, encoding); err != nil {
			return Gear{}, fmt.Errorf("forge gear: store file %q: %w", f.Path, err)
		}
	}

	// The forging agent binds the gear to itself automatically. An
	// operator-authored gear has no agent to bind to — it is granted from
	// the catalog instead.
	if agentID != 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO gear_bindings (gear_id, workspace_id, agent_id, created_at) VALUES (?, ?, ?, ?)`,
			id, wsID, agentID, now()); err != nil {
			return Gear{}, fmt.Errorf("forge gear: self-bind: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Gear{}, fmt.Errorf("forge gear: commit: %w", err)
	}
	// The names are logged and the values are not, which is the rule this whole
	// mechanism turns on: a name is what the operator has to see, and a value is
	// what nobody but the gear's own process ever does.
	slog.Info("gear forged", "id", id, "name", name, "version", version, "runtime", runtime,
		"workspace_id", wsID, "agent_id", agentID, "status", "pending", "env_names", envNames,
		"network_granted", false)
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
	       g.version, g.runtime, g.entrypoint, g.args_schema, g.env_names,
	       g.network_granted, g.network_hosts, g.environment, g.status,
	       g.timeout_seconds, g.updated_at
	FROM gears g
	LEFT JOIN workspaces w ON w.id = g.origin_workspace_id
	LEFT JOIN agents a ON a.id = g.created_by_agent_id`

func scanGear(row interface{ Scan(...any) error }) (Gear, error) {
	var g Gear
	var tags, envNames, netHosts string
	var netGranted int
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &tags, &g.OriginWorkspaceID, &g.OriginWorkspace,
		&g.CreatedByAgentID, &g.CreatedByAgent, &g.Version, &g.Runtime, &g.Entrypoint,
		&g.ArgsSchema, &envNames, &netGranted, &netHosts, &g.Environment, &g.Status, &g.TimeoutSeconds, &g.UpdatedAt); err != nil {
		return Gear{}, err
	}
	g.NetworkGranted = netGranted == 1
	if err := json.Unmarshal([]byte(tags), &g.Tags); err != nil {
		slog.Warn("gear has unparseable tags", "gear", g.Name, "err", err)
		g.Tags = []string{}
	}
	// An unreadable list becomes the empty list, not the previous value and not
	// a guess: a gear whose declaration cannot be read must be given nothing,
	// and it will then refuse loudly on its first call rather than run with
	// whatever happened to parse.
	if err := json.Unmarshal([]byte(envNames), &g.EnvNames); err != nil {
		slog.Warn("gear has an unparseable list of named values; it will be given none",
			"gear", g.Name, "err", err)
		g.EnvNames = []string{}
	}
	// An unreadable destination list is NOT the empty list here, and the
	// asymmetry with env_names above is deliberate. Empty means "anywhere", so
	// falling back to it would widen a grant on the strength of a parse error.
	// A list nobody can read is a grant nobody can honour, so the grant goes.
	if err := json.Unmarshal([]byte(netHosts), &g.NetworkHosts); err != nil {
		slog.Warn("gear has an unparseable list of network destinations; its network grant is withdrawn until an operator grants it again",
			"gear", g.Name, "err", err)
		g.NetworkHosts, g.NetworkGranted = []string{}, false
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
		`SELECT path, content, encoding FROM gear_files WHERE gear_id = ? AND version = ? ORDER BY path`,
		gearID, version)
	if err != nil {
		return nil, fmt.Errorf("gear %d files: %w", gearID, err)
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Path, &f.Content, &f.Encoding); err != nil {
			return nil, fmt.Errorf("scan gear file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetStatus approves or disables a gear. Approval is the operator's act —
// nothing else may call it.
// SetStatus changes a gear's status AND writes the trail row for it.
//
// The two are one function on purpose. A separate "record the approval" call
// beside this one is a call somebody forgets at the next call site, and the
// gap it leaves is invisible — the gear is approved, the trail simply has no
// row for it, and nobody notices until the day the trail is what matters. See
// approvals.go for what the trail answers and why a status column cannot.
func (s *Store) SetStatus(ctx context.Context, id int64, status string, by Actor) (Gear, error) {
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
	// After the change, so the row states the grants that are in force under
	// the new status rather than the ones that were in force before it.
	s.recordApproval(ctx, id, status, by)
	return s.Get(ctx, id)
}

// SetNetwork records the other half of the approval decision: whether this
// gear may reach out, and where.
//
// It is the operator's act, like SetStatus, and it is made on the same screen
// in the same breath — an operator reading the code is the only person who can
// judge "should THIS be able to reach api.example.com". Nothing else may call
// it, and nothing an agent can reach leads here.
//
// An empty hosts list with granted true means anywhere. That is deliberately
// allowed and deliberately not the default the interface offers: the plan is
// explicit that a destination list is what makes the connection log auditable
// afterwards, not a gate on the operator.
// EnvironmentDefault and EnvironmentBrowser are the two this software knows.
//
// Names rather than images, because a gear that pinned an image is a gear that
// stops working when the operator moves to another one, and a gear that could
// NAME an image would be agent-authored code choosing what it runs inside.
const (
	EnvironmentDefault = ""
	EnvironmentBrowser = "browser"
)

// SetEnvironment records which container this gear runs in.
//
// Refused rather than coerced for an unknown name: an operator who typed
// "chrome" and got the ordinary image would have a gear that silently cannot
// find a browser, and would go looking at the gear's code for the reason.
func (s *Store) SetEnvironment(ctx context.Context, id int64, env string) (Gear, error) {
	if env != EnvironmentDefault && env != EnvironmentBrowser {
		return Gear{}, fmt.Errorf("%q is not an environment this install has: use \"browser\", or leave it "+
			"empty for the ordinary sandbox image", env)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE gears SET environment = ?, updated_at = ? WHERE id = ?`, env, now(), id)
	if err != nil {
		return Gear{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Gear{}, fmt.Errorf("gear %d: %w", id, ErrNotFound)
	}
	slog.Info("gear environment set", "gear_id", id, "environment", env)
	return s.Get(ctx, id)
}

func (s *Store) SetNetwork(ctx context.Context, id int64, granted bool, hosts []string) (Gear, error) {
	clean, err := gearnet.NormalizeHosts(hosts)
	if err != nil {
		return Gear{}, err
	}
	if !granted {
		// The list goes with the grant rather than lingering: a gear that shows
		// three allowed hosts and cannot reach any of them is a screen that
		// reads as permission when it is the opposite.
		clean = []string{}
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return Gear{}, err
	}
	g := 0
	if granted {
		g = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE gears SET network_granted = ?, network_hosts = ?, updated_at = ? WHERE id = ?`,
		g, string(raw), now(), id)
	if err != nil {
		return Gear{}, fmt.Errorf("set gear %d network: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Gear{}, fmt.Errorf("gear %d: %w", id, ErrNotFound)
	}
	// Logged as its own line rather than folded into the status change: this is
	// a capability grant, and an operator reading the log later is looking for
	// the moment code was allowed to reach out.
	slog.Info("gear network grant changed by operator", "gear_id", id, "granted", granted,
		"destinations", clean, "anywhere", granted && len(clean) == 0)
	return s.Get(ctx, id)
}

// SetTimeout changes how long a gear may run before it is killed.
func (s *Store) SetTimeout(ctx context.Context, id int64, seconds int) (Gear, error) {
	if seconds < 1 || seconds > 3600 {
		return Gear{}, fmt.Errorf("timeout must be between 1 and 3600 seconds (got %d)", seconds)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE gears SET timeout_seconds = ?, updated_at = ? WHERE id = ?`, seconds, now(), id)
	if err != nil {
		return Gear{}, fmt.Errorf("set gear %d timeout: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Gear{}, fmt.Errorf("gear %d: %w", id, ErrNotFound)
	}
	slog.Info("gear timeout changed", "gear_id", id, "timeout_seconds", seconds)
	return s.Get(ctx, id)
}

// RecordRun persists one execution. Every gear run is recorded — this is
// the operator's audit trail for code running on their machine.
func (s *Store) RecordRun(ctx context.Context, r Run) error {
	var agentID, wsID any
	if r.AgentID != nil {
		agentID = *r.AgentID
	}
	if r.WorkspaceID != nil {
		wsID = *r.WorkspaceID
	}
	timedOut := 0
	if r.TimedOut {
		timedOut = 1
	}
	// The name is copied onto the row rather than joined to later: after the
	// gear is deleted there is nothing to join to, and a run that cannot say
	// which gear it was is not a record of anything.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO gear_runs (gear_id, gear_name, version, agent_id, workspace_id, args, exit_code,
		                       timed_out, duration_ms, stdout, stderr, created_at)
		VALUES (?, COALESCE((SELECT name FROM gears WHERE id = ?), ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.GearID, r.GearID, r.Version, agentID, wsID, r.Args, r.ExitCode, timedOut, r.DurationMs,
		truncateForLog(r.Stdout), truncateForLog(r.Stderr), now())
	if err != nil {
		return fmt.Errorf("record gear run: %w", err)
	}
	return nil
}

// truncateForLog bounds what a single run can add to the database.
func truncateForLog(s string) string {
	const max = 8 << 10
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n…[truncated]"
}

func (s *Store) ListRuns(ctx context.Context, gearID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, COALESCE(r.gear_id, 0), r.gear_name, r.version, r.agent_id, COALESCE(a.name, ''),
		       r.workspace_id, r.args, r.exit_code, r.timed_out, r.duration_ms, r.stdout, r.stderr, r.created_at
		FROM gear_runs r LEFT JOIN agents a ON a.id = r.agent_id
		WHERE r.gear_id = ? ORDER BY r.id DESC LIMIT ?`, gearID, limit)
	if err != nil {
		return nil, fmt.Errorf("list gear runs: %w", err)
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var timedOut int
		if err := rows.Scan(&r.ID, &r.GearID, &r.GearName, &r.Version, &r.AgentID, &r.AgentName, &r.WorkspaceID,
			&r.Args, &r.ExitCode, &timedOut, &r.DurationMs, &r.Stdout, &r.Stderr, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan gear run: %w", err)
		}
		r.TimedOut = timedOut == 1
		out = append(out, r)
	}
	return out, rows.Err()
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
