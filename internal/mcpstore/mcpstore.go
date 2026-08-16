// Package mcpstore holds the external MCP servers an operator installed, the
// tools they reported, and which agents may reach them.
//
// # What an operator is agreeing to
//
// A gear is source this install holds: versioned, approved line by line, run in
// a container that cannot see the server's files. An external MCP server is a
// command. Cogitorium never sees its source, and the tool list is the server's
// own account of itself. The child runs on the host as this server's user, so
// an approved MCP server can read the database this table lives in and every
// provider key in it.
//
// Nothing in this package changes that. What it does is make each step
// something an operator did on purpose, and make a change afterwards visible:
//
//   - installing, approving and granting are three separate admin-only acts;
//   - each TOOL is approved individually, so a server that grows a new one
//     after approval does not thereby acquire it;
//   - the command is fingerprinted at approval and re-checked at every spawn,
//     and a mismatch puts the server back to pending.
//
// The honest limit of that fingerprint: it covers the command line, not the
// bytes at the end of it. `npx some-server@latest` refetches on every spawn and
// the fingerprint never moves.
package mcpstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/mcp/mcpwire"
)

// The statuses a server can be in — the same three a gear has, so the approval
// screen and the operator's understanding carry over unchanged.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDisabled = "disabled"
)

// ToolPrefix is what an MCP tool is called when a model is offered it.
//
// Disjoint from internal/engine's "gear_" by construction, so a gear and an MCP
// server may share a name without either shadowing the other. The double
// underscore separates the server from the tool, and both halves are sanitised,
// so `server` + `tool` can never collide with a different pair.
const ToolPrefix = "mcp_"

// maxToolName is what model providers accept. Longer names are truncated with a
// hash of the original, which keeps them distinct — truncation alone turns two
// long names from the same server into one, and the second silently shadows the
// first.
const maxToolName = 64

// MaxToolsPerServer bounds what one server can put in every request.
//
// A server offering three hundred tools is offering three hundred tool
// definitions in every model call, which is a bill rather than a capability.
// The remainder is refused and the operator is told, rather than dropped.
const MaxToolsPerServer = 100

type Server struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	Dir         string   `json:"cwd"`
	EnvNames    []string `json:"env_names"`
	Transport   string   `json:"transport"`
	Status      string   `json:"status"`
	// Fingerprint is what was approved. Never the thing that is checked — that
	// is recomputed from the row at every spawn — but shown so an operator can
	// see that one exists.
	Fingerprint    string `json:"approved_fingerprint"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// Tool is one thing a server reported, and whether the operator agreed to it.
type Tool struct {
	ID          int64  `json:"id"`
	ServerID    int64  `json:"server_id"`
	ServerName  string `json:"server_name"`
	RemoteName  string `json:"remote_name"`
	OfferedName string `json:"offered_name"`
	Description string `json:"description"`
	InputSchema string `json:"input_schema"`
	Approved    bool   `json:"approved"`
	FirstSeenAt string `json:"first_seen_at"`
	ListedAt    string `json:"listed_at"`
}

type Binding struct {
	ID          int64  `json:"id"`
	ServerID    int64  `json:"server_id"`
	ServerName  string `json:"server_name"`
	WorkspaceID int64  `json:"workspace_id"`
	AgentID     *int64 `json:"agent_id"`
	CreatedAt   string `json:"created_at"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

var nameOK = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// Install records a server. It is pending: installing is not approving, and the
// two are separate acts because the second one is the one that matters.
func (s *Store) Install(ctx context.Context, srv Server, byUser *int64) (Server, error) {
	srv.Name = strings.TrimSpace(srv.Name)
	if !nameOK.MatchString(srv.Name) {
		return Server{}, fmt.Errorf("an MCP server's name must be lower-case letters, digits, "+
			"underscore or hyphen, starting with a letter or digit (got %q)", srv.Name)
	}
	if strings.TrimSpace(srv.Command) == "" {
		return Server{}, errors.New("an MCP server needs a command to run")
	}
	if srv.Transport == "" {
		srv.Transport = "stdio"
	}
	if srv.Transport != "stdio" {
		return Server{}, fmt.Errorf("this install speaks MCP over stdio only, not %q", srv.Transport)
	}
	if srv.TimeoutSeconds <= 0 {
		srv.TimeoutSeconds = 60
	}
	args, err := json.Marshal(nonNil(srv.Args))
	if err != nil {
		return Server{}, err
	}
	envNames, err := json.Marshal(nonNil(srv.EnvNames))
	if err != nil {
		return Server{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_servers (name, description, command, args, cwd, env_names, transport,
		                          status, timeout_seconds, created_by_user_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.Name, srv.Description, srv.Command, string(args), srv.Dir, string(envNames),
		srv.Transport, StatusPending, srv.TimeoutSeconds, byUser, now(), now())
	if err != nil {
		return Server{}, asConflict(err, "mcp server "+srv.Name)
	}
	id, _ := res.LastInsertId()
	slog.Warn("an external MCP server was installed; it is PENDING and runs on this host outside the sandbox",
		"server", srv.Name, "command", srv.Command)
	return s.Get(ctx, id)
}

const serverSelect = `
	SELECT id, name, description, command, args, cwd, env_names, transport, status,
	       approved_fingerprint, timeout_seconds, created_at, updated_at
	FROM mcp_servers`

func scanServer(row interface{ Scan(...any) error }) (Server, error) {
	var srv Server
	var args, envNames string
	if err := row.Scan(&srv.ID, &srv.Name, &srv.Description, &srv.Command, &args, &srv.Dir,
		&envNames, &srv.Transport, &srv.Status, &srv.Fingerprint, &srv.TimeoutSeconds,
		&srv.CreatedAt, &srv.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Server{}, catalog.ErrNotFound
		}
		return Server{}, err
	}
	_ = json.Unmarshal([]byte(args), &srv.Args)
	_ = json.Unmarshal([]byte(envNames), &srv.EnvNames)
	return srv, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Server, error) {
	srv, err := scanServer(s.db.QueryRowContext(ctx, serverSelect+` WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			return Server{}, fmt.Errorf("mcp server %d: %w", id, catalog.ErrNotFound)
		}
		return Server{}, err
	}
	return srv, nil
}

func (s *Store) List(ctx context.Context) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, serverSelect+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Server{}
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// Fingerprint is what approval covers: the command line and what it will be
// given, and nothing about the bytes it points at.
func Fingerprint(srv Server) string {
	h := sha256.New()
	for _, part := range append([]string{srv.Command, srv.Dir, srv.Transport}, srv.Args...) {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	h.Write([]byte("env"))
	for _, n := range srv.EnvNames {
		h.Write([]byte(n))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SetStatus approves, disables or resets a server, stamping the fingerprint on
// approval so a later edit can be noticed.
func (s *Store) SetStatus(ctx context.Context, id int64, status string) (Server, error) {
	switch status {
	case StatusPending, StatusApproved, StatusDisabled:
	default:
		return Server{}, fmt.Errorf("status must be pending, approved or disabled (got %q)", status)
	}
	srv, err := s.Get(ctx, id)
	if err != nil {
		return Server{}, err
	}
	fingerprint := ""
	if status == StatusApproved {
		fingerprint = Fingerprint(srv)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE mcp_servers SET status = ?, approved_fingerprint = ?, updated_at = ? WHERE id = ?`,
		status, fingerprint, now(), id); err != nil {
		return Server{}, err
	}
	slog.Warn("an external MCP server's status changed", "server", srv.Name, "status", status,
		"command", srv.Command)
	return s.Get(ctx, id)
}

// Update edits a server and returns it to pending, because everything editable
// here is inside the fingerprint. Approval covers exact content, and this is
// the content.
func (s *Store) Update(ctx context.Context, id int64, srv Server) (Server, error) {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return Server{}, err
	}
	srv.Name = existing.Name
	args, err := json.Marshal(nonNil(srv.Args))
	if err != nil {
		return Server{}, err
	}
	envNames, err := json.Marshal(nonNil(srv.EnvNames))
	if err != nil {
		return Server{}, err
	}
	if strings.TrimSpace(srv.Command) == "" {
		return Server{}, errors.New("an MCP server needs a command to run")
	}
	if srv.TimeoutSeconds <= 0 {
		srv.TimeoutSeconds = existing.TimeoutSeconds
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE mcp_servers SET description = ?, command = ?, args = ?, cwd = ?, env_names = ?,
		        timeout_seconds = ?, status = ?, approved_fingerprint = '', updated_at = ?
		 WHERE id = ?`,
		srv.Description, srv.Command, string(args), srv.Dir, string(envNames),
		srv.TimeoutSeconds, StatusPending, now(), id); err != nil {
		return Server{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("mcp server %d: %w", id, catalog.ErrNotFound)
	}
	return nil
}

// ErrChanged is a server whose command no longer matches what was approved.
var ErrChanged = errors.New("this MCP server's command has changed since it was approved")

// Spawnable returns a server only if it may actually be run, and puts it back
// to pending if its command changed since approval.
//
// Checked here rather than at approval alone, because the row is editable and
// the whole value of the fingerprint is that it is compared at the moment it
// matters — which is the moment somebody else's binary is about to start.
func (s *Store) Spawnable(ctx context.Context, id int64) (Server, error) {
	srv, err := s.Get(ctx, id)
	if err != nil {
		return Server{}, err
	}
	if srv.Status != StatusApproved {
		return Server{}, fmt.Errorf("the MCP server %q is %s, not approved, so it was not started",
			srv.Name, srv.Status)
	}
	if got := Fingerprint(srv); got != srv.Fingerprint {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE mcp_servers SET status = ?, approved_fingerprint = '', updated_at = ? WHERE id = ?`,
			StatusPending, now(), id); err != nil {
			slog.Error("could not return a changed MCP server to pending", "server", srv.Name, "err", err)
		}
		slog.Warn("an MCP server's command changed after it was approved; it will not be started",
			"server", srv.Name, "command", srv.Command)
		return Server{}, fmt.Errorf("%w, so it was not started and is pending again: %s",
			ErrChanged, srv.Name)
	}
	return srv, nil
}

// RecordTools stores what a server reported.
//
// Tools already known keep their approval and their first_seen_at; new ones
// arrive unapproved. That is the whole mechanism against a server that grows a
// tool after an operator looked at it.
func (s *Store) RecordTools(ctx context.Context, serverID int64, tools []mcpwire.Tool) error {
	srv, err := s.Get(ctx, serverID)
	if err != nil {
		return err
	}
	stamp := now()
	for _, t := range tools {
		schema := "{}"
		if len(t.InputSchema) > 0 {
			schema = string(t.InputSchema)
		}
		offered := OfferedName(srv.Name, t.Name)
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO mcp_tools (server_id, remote_name, offered_name, description, input_schema,
			                        approved, first_seen_at, listed_at)
			 VALUES (?, ?, ?, ?, ?, 0, ?, ?)
			 ON CONFLICT (server_id, remote_name) DO UPDATE SET
			     description = excluded.description,
			     input_schema = excluded.input_schema,
			     offered_name = excluded.offered_name,
			     listed_at = excluded.listed_at`,
			serverID, t.Name, offered, t.Description, schema, stamp, stamp); err != nil {
			return asConflict(err, "mcp tool "+offered)
		}
	}
	return nil
}

// OfferedName is what a model is told a tool is called.
//
// Both halves are sanitised and then joined with a separator neither can
// contain, so two different (server, tool) pairs cannot produce one name. A
// name that would exceed what providers accept is truncated with a hash of the
// original — truncation alone would turn two long names into one, and the
// second would silently shadow the first.
func OfferedName(server, tool string) string {
	name := ToolPrefix + sanitise(server) + "__" + sanitise(tool)
	if len(name) <= maxToolName {
		return name
	}
	sum := sha256.Sum256([]byte(server + "\x00" + tool))
	suffix := "_" + hex.EncodeToString(sum[:4])
	return name[:maxToolName-len(suffix)] + suffix
}

var unsafeChar = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func sanitise(s string) string {
	return strings.Trim(unsafeChar.ReplaceAllString(s, "_"), "_")
}

const toolSelect = `
	SELECT t.id, t.server_id, s.name, t.remote_name, t.offered_name, t.description,
	       t.input_schema, t.approved, t.first_seen_at, t.listed_at
	FROM mcp_tools t JOIN mcp_servers s ON s.id = t.server_id`

func scanTools(rows *sql.Rows) ([]Tool, error) {
	defer rows.Close()
	out := []Tool{}
	for rows.Next() {
		var t Tool
		var approved int
		if err := rows.Scan(&t.ID, &t.ServerID, &t.ServerName, &t.RemoteName, &t.OfferedName,
			&t.Description, &t.InputSchema, &approved, &t.FirstSeenAt, &t.ListedAt); err != nil {
			return nil, err
		}
		t.Approved = approved == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Tools(ctx context.Context, serverID int64) ([]Tool, error) {
	rows, err := s.db.QueryContext(ctx, toolSelect+` WHERE t.server_id = ? ORDER BY t.remote_name`, serverID)
	if err != nil {
		return nil, err
	}
	return scanTools(rows)
}

// ApproveTool is per tool, and per tool is the point.
func (s *Store) ApproveTool(ctx context.Context, toolID int64, approved bool) error {
	v := 0
	if approved {
		v = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE mcp_tools SET approved = ? WHERE id = ?`, v, toolID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("mcp tool %d: %w", toolID, catalog.ErrNotFound)
	}
	return nil
}

// Bind grants a server to a whole workspace (agentID nil) or to one agent.
func (s *Store) Bind(ctx context.Context, serverID, workspaceID int64, agentID *int64) (Binding, error) {
	if _, err := s.Get(ctx, serverID); err != nil {
		return Binding{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_bindings (server_id, workspace_id, agent_id, created_at) VALUES (?, ?, ?, ?)`,
		serverID, workspaceID, agentID, now())
	if err != nil {
		return Binding{}, asConflict(err, "mcp binding")
	}
	id, _ := res.LastInsertId()
	return Binding{ID: id, ServerID: serverID, WorkspaceID: workspaceID, AgentID: agentID}, nil
}

func (s *Store) Unbind(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mcp_bindings WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("mcp binding %d: %w", id, catalog.ErrNotFound)
	}
	return nil
}

// WorkspaceOfBinding is what the HTTP layer scopes a delete by, so one
// workspace's member cannot remove another's grant.
func (s *Store) WorkspaceOfBinding(ctx context.Context, id int64) (int64, error) {
	var ws int64
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM mcp_bindings WHERE id = ?`, id).Scan(&ws)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("mcp binding %d: %w", id, catalog.ErrNotFound)
	}
	return ws, err
}

func (s *Store) Bindings(ctx context.Context, workspaceID int64) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT b.id, b.server_id, s.name, b.workspace_id, b.agent_id, b.created_at
		 FROM mcp_bindings b JOIN mcp_servers s ON s.id = b.server_id
		 WHERE b.workspace_id = ? ORDER BY s.name`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Binding{}
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.ID, &b.ServerID, &b.ServerName, &b.WorkspaceID, &b.AgentID, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ToolsForAgent is every MCP tool this agent may actually call.
//
// Four conditions, and each one is a gate an operator closed on purpose: the
// server is bound to this agent or to its whole workspace, the server is
// approved, the tool is approved, and — implicitly — the server still exists.
// A query that dropped any of them would offer a model something nobody agreed
// to, and the tests break each in turn.
func (s *Store) ToolsForAgent(ctx context.Context, workspaceID, agentID int64) ([]Tool, error) {
	rows, err := s.db.QueryContext(ctx, toolSelect+`
		 JOIN mcp_bindings b ON b.server_id = s.id
		 WHERE b.workspace_id = ?
		   AND (b.agent_id IS NULL OR b.agent_id = ?)
		   AND s.status = 'approved'
		   AND t.approved = 1
		 GROUP BY t.id
		 ORDER BY t.offered_name`, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	return scanTools(rows)
}

// ByOfferedName finds the tool a model asked for, and the server behind it.
func (s *Store) ByOfferedName(ctx context.Context, name string) (Tool, Server, error) {
	rows, err := s.db.QueryContext(ctx, toolSelect+` WHERE t.offered_name = ?`, name)
	if err != nil {
		return Tool{}, Server{}, err
	}
	tools, err := scanTools(rows)
	if err != nil {
		return Tool{}, Server{}, err
	}
	if len(tools) == 0 {
		return Tool{}, Server{}, fmt.Errorf("mcp tool %q: %w", name, catalog.ErrNotFound)
	}
	srv, err := s.Get(ctx, tools[0].ServerID)
	return tools[0], srv, err
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// asConflict turns a unique-constraint violation into the shared sentinel, the
// way internal/catalog does, so the HTTP layer's existing mapping answers 409.
func asConflict(err error, what string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%s: %w", what, catalog.ErrConflict)
	}
	return err
}
