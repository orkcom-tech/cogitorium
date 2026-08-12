// Package workspace owns workspaces, their agents, and the wires between
// agents. Every workspace is created with its orchestrator agent — the
// mandatory chat entry point.
package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/identity"
)

var (
	ErrNotFound = catalog.ErrNotFound
	ErrConflict = catalog.ErrConflict
)

const OrchestratorName = "orchestrator"

// ContextRoot is the only part of the Contextverse space Cogitorium writes
// to. Everything outside it belongs to the operator and to Contextverse's
// other clients.
const ContextRoot = "workspaces"

// slugRe strips anything that would make a path awkward to read or unsafe.
var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "unnamed"
	}
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// newWorkspaceBranch names a workspace's subtree in the Contextverse space.
// The id keeps it unique and the slug keeps the tree readable as a mind
// map. It is computed once, at creation, and stored: recomputing it from
// the current name would move an agent away from its own context the
// moment anyone renames it.
func newWorkspaceBranch(name string, id int64) string {
	return fmt.Sprintf("%s/%s-%d", ContextRoot, slug(name), id)
}

func newAgentBranch(workspaceBranch, name string, id int64) string {
	return fmt.Sprintf("%s/agents/%s-%d", workspaceBranch, slug(name), id)
}

// SharedBranch holds context every agent in the workspace sees.
func (w Workspace) SharedBranch() string {
	if w.Branch == "" {
		return ""
	}
	return w.Branch + "/shared"
}

// DefaultOrchestratorRole seeds the orchestrator's editable system prompt.
const DefaultOrchestratorRole = `You are the orchestrator of this workspace. You are the operator's single point of contact: everything they want done in this workspace goes through you.

You can inspect the model catalog, create and configure worker agents (each with its own role and model), and delegate tasks to them. Prefer delegating specialized work to a fitting agent — create one if none fits. Report results back plainly and honestly; if an agent fails, say so and show why.`

type Workspace struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Branch is this workspace's subtree in the Contextverse space, frozen
	// at creation so a rename cannot orphan it.
	Branch string `json:"branch"`
	// SharedBranchPath is where the workspace's shared context lives; every
	// agent reads it without an explicit binding.
	SharedBranchPath string `json:"shared_branch"`
	// OwnerID is the user who created it; TeamID, when set, shares it with
	// every member of that team.
	OwnerID *int64 `json:"owner_id"`
	// TeamIDs is every team the workspace is shared with. It replaced a single
	// team_id column, which made "share it with the platform team as well"
	// impossible without taking it away from research first.
	TeamIDs []int64 `json:"team_ids"`
}

type Agent struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Role        string `json:"role"`
	// Avoid is the operator's standing prohibitions for this agent, one rule
	// per line. It reaches the prompt last, after everything the agent is
	// asked to do, because a constraint stated last is the one still in view
	// when the model answers.
	Avoid          string   `json:"avoid"`
	ModelID        *int64   `json:"model_id"`
	ModelLabel     string   `json:"model_label"` // "provider / model" for display; empty if unbound
	IsOrchestrator bool     `json:"is_orchestrator"`
	PosX           *float64 `json:"pos_x"` // blueprint canvas position; nil = unplaced
	PosY           *float64 `json:"pos_y"`
	// Branch is this agent's private context subtree, read without an
	// explicit binding and frozen at creation.
	Branch string `json:"branch"`
}

// AgentSpec is an agent described in full, for the callers that have every
// field at once: a clone, or an import rebuilding a workspace from a bundle.
// The narrow CreateAgent stays for the UI and the orchestrator's tools, which
// only ever have a name, a role and a model — adding the rest as more
// trailing arguments is how a caller ends up silently swapping two of them.
type AgentSpec struct {
	Name  string
	Role  string
	Avoid string
	// ModelID is nil when no model is bound. That is a real state, not a
	// missing value: a bundle can name a model this install does not have,
	// and it has to be stored as NULL rather than as the zero id, which
	// belongs to no model and which the foreign key would reject outright.
	ModelID *int64
	PosX    *float64
	PosY    *float64
}

type Wire struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	FromAgentID int64  `json:"from_agent_id"`
	ToAgentID   int64  `json:"to_agent_id"`
	Label       string `json:"label"`
}

type Message struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	AgentID     *int64 `json:"agent_id"` // nil = the operator
	Kind        string `json:"kind"`
	Content     string `json:"content"`
	Meta        string `json:"meta"`
	CreatedAt   string `json:"created_at"`
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

// CreateWorkspace creates the workspace and its orchestrator agent bound to
// orchestratorModelID in one transaction, owned by ownerID.
func (s *Store) CreateWorkspace(ctx context.Context, name, description string, orchestratorModelID, ownerID int64) (Workspace, error) {
	return s.CreateWorkspaceSpec(ctx, name, description,
		AgentSpec{Name: OrchestratorName, Role: DefaultOrchestratorRole, ModelID: &orchestratorModelID}, ownerID)
}

// CreateWorkspaceSpec is CreateWorkspace for a caller that describes the
// orchestrator in full — a clone or an import, which carry its own role, its
// prohibitions, and sometimes no model at all because the bundle named one
// this install does not have.
func (s *Store) CreateWorkspaceSpec(ctx context.Context, name, description string, orchestrator AgentSpec, ownerID int64) (Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Workspace{}, errors.New("name is required")
	}
	// The orchestrator's name is fixed by the product, so a caller that does
	// not care about it does not have to repeat it.
	if strings.TrimSpace(orchestrator.Name) == "" {
		orchestrator.Name = OrchestratorName
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Workspace{}, fmt.Errorf("create workspace: begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO workspaces (name, description, owner_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		name, description, ownerID, now(), now())
	if err := asConflict(err, fmt.Sprintf("workspace %q", name)); err != nil {
		return Workspace{}, fmt.Errorf("create workspace: %w", err)
	}
	wsID, _ := res.LastInsertId()

	// Branches are frozen here, with the ids the rows just received.
	wsBranch := newWorkspaceBranch(name, wsID)
	if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET branch = ? WHERE id = ?`, wsBranch, wsID); err != nil {
		return Workspace{}, fmt.Errorf("create workspace: set branch: %w", err)
	}

	if _, err := insertAgent(ctx, tx, wsID, wsBranch, orchestrator, true); err != nil {
		return Workspace{}, fmt.Errorf("create workspace: seed orchestrator: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Workspace{}, fmt.Errorf("create workspace: commit: %w", err)
	}
	slog.Info("workspace created with orchestrator", "id", wsID, "name", name,
		"orchestrator_model_id", modelForLog(orchestrator.ModelID))
	return s.GetWorkspace(ctx, wsID)
}

// execer is satisfied by both *sql.DB and *sql.Tx, so an agent can be written
// inside the transaction that creates its workspace or on its own afterwards
// without two copies of the insert.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// insertAgent writes one agent row and freezes its branch. Every path that
// creates an agent goes through it, so a column added to agents cannot be
// filled in on one path and quietly left at its default on the other.
func insertAgent(ctx context.Context, ex execer, wsID int64, wsBranch string, spec AgentSpec, isOrchestrator bool) (int64, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return 0, errors.New("name is required")
	}
	orch := 0
	if isOrchestrator {
		orch = 1
	}
	res, err := ex.ExecContext(ctx,
		`INSERT INTO agents (workspace_id, name, kind, role, avoid, model_id, is_orchestrator, pos_x, pos_y, created_at, updated_at)
		 VALUES (?, ?, 'model', ?, ?, ?, ?, ?, ?, ?, ?)`,
		wsID, name, spec.Role, spec.Avoid, spec.ModelID, orch, spec.PosX, spec.PosY, now(), now())
	if err := asConflict(err, fmt.Sprintf("agent %q in this workspace", name)); err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if _, err := ex.ExecContext(ctx, `UPDATE agents SET branch = ? WHERE id = ?`,
		newAgentBranch(wsBranch, name, id), id); err != nil {
		return 0, fmt.Errorf("set branch of agent %q: %w", name, err)
	}
	return id, nil
}

// modelForLog keeps a nil model out of the log as the word rather than as a
// pointer address, which is what slog would otherwise print.
func modelForLog(id *int64) any {
	if id == nil {
		return "none"
	}
	return *id
}

func (s *Store) GetWorkspace(ctx context.Context, id int64) (Workspace, error) {
	var w Workspace
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, branch, owner_id FROM workspaces WHERE id = ?`, id).
		Scan(&w.ID, &w.Name, &w.Description, &w.Branch, &w.OwnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, fmt.Errorf("workspace %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Workspace{}, fmt.Errorf("get workspace %d: %w", id, err)
	}
	// Read after the row is closed: holding a result set open while issuing
	// another query would want a second connection, and there is only one.
	w.TeamIDs, err = s.teamsOf(ctx, id)
	if err != nil {
		return Workspace{}, err
	}
	w.SharedBranchPath = w.SharedBranch()
	return w, nil
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, branch, owner_id FROM workspaces ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.Description, &w.Branch, &w.OwnerID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan workspace: %w", err)
		}
		w.SharedBranchPath = w.SharedBranch()
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// One query for every grant rather than one per workspace: the list is
	// rendered on the landing page and N+1 there is felt.
	grants, err := s.allTeamGrants(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].TeamIDs = grants[out[i].ID]
	}
	return out, nil
}

// Visible reports whether a user may see and act on a workspace: an admin
// sees everything, an owner sees their own, and a team grant shares one
// with every member of that team.
func Visible(w Workspace, u identity.User) bool {
	if u.IsAdmin() {
		return true
	}
	if w.OwnerID != nil && *w.OwnerID == u.ID {
		return true
	}
	for _, t := range w.TeamIDs {
		if u.InTeam(t) {
			return true
		}
	}
	return false
}

// ListWorkspacesFor returns only what this user may see.
func (s *Store) ListWorkspacesFor(ctx context.Context, u identity.User) ([]Workspace, error) {
	all, err := s.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := []Workspace{}
	for _, w := range all {
		if Visible(w, u) {
			out = append(out, w)
		}
	}
	return out, nil
}

// CanAccess resolves a workspace and reports whether the user may use it.
// Handlers call this before anything workspace-scoped — a filtered list
// alone would leave every other route open to a direct id.
func (s *Store) CanAccess(ctx context.Context, u identity.User, wsID int64) (bool, error) {
	w, err := s.GetWorkspace(ctx, wsID)
	if err != nil {
		return false, err
	}
	return Visible(w, u), nil
}

// WorkspaceOfAgent and friends resolve a nested resource back to its
// workspace, so routes keyed by agent, wire or binding id are checked too.
func (s *Store) WorkspaceOfAgent(ctx context.Context, agentID int64) (int64, error) {
	var wsID int64
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM agents WHERE id = ?`, agentID).Scan(&wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("agent %d: %w", agentID, ErrNotFound)
	}
	return wsID, err
}

func (s *Store) WorkspaceOfWire(ctx context.Context, wireID int64) (int64, error) {
	var wsID int64
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM wires WHERE id = ?`, wireID).Scan(&wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("wire %d: %w", wireID, ErrNotFound)
	}
	return wsID, err
}

func (s *Store) WorkspaceOfContextBinding(ctx context.Context, bindingID int64) (int64, error) {
	var wsID int64
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM context_bindings WHERE id = ?`, bindingID).Scan(&wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("context binding %d: %w", bindingID, ErrNotFound)
	}
	return wsID, err
}

func (s *Store) teamsOf(ctx context.Context, wsID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT team_id FROM workspace_teams WHERE workspace_id = ? ORDER BY team_id`, wsID)
	if err != nil {
		return nil, fmt.Errorf("workspace teams: %w", err)
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) allTeamGrants(ctx context.Context) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT workspace_id, team_id FROM workspace_teams ORDER BY workspace_id, team_id`)
	if err != nil {
		return nil, fmt.Errorf("team grants: %w", err)
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var ws, team int64
		if err := rows.Scan(&ws, &team); err != nil {
			return nil, err
		}
		out[ws] = append(out[ws], team)
	}
	return out, rows.Err()
}

// ShareWith adds a team. Idempotent, so clicking twice is not an error the
// operator has to think about.
func (s *Store) ShareWith(ctx context.Context, wsID, teamID int64) (Workspace, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace_teams (workspace_id, team_id, created_at) VALUES (?, ?, ?)
		 ON CONFLICT (workspace_id, team_id) DO NOTHING`, wsID, teamID, now()); err != nil {
		return Workspace{}, fmt.Errorf("share workspace %d with team %d: %w", wsID, teamID, err)
	}
	slog.Info("workspace shared with a team", "workspace_id", wsID, "team_id", teamID)
	return s.GetWorkspace(ctx, wsID)
}

// Unshare withdraws one team without touching the others.
func (s *Store) Unshare(ctx context.Context, wsID, teamID int64) (Workspace, error) {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM workspace_teams WHERE workspace_id = ? AND team_id = ?`, wsID, teamID); err != nil {
		return Workspace{}, fmt.Errorf("unshare workspace %d from team %d: %w", wsID, teamID, err)
	}
	slog.Info("workspace unshared from a team", "workspace_id", wsID, "team_id", teamID)
	return s.GetWorkspace(ctx, wsID)
}

// Clone copies a workspace's definition — agents, wires and gear grants —
// under a new owner, leaving the timeline behind. This is what replaced
// versioned templates: two people wanting the same setup each take a copy,
// and nothing they do afterwards touches the other.
func (s *Store) Clone(ctx context.Context, srcID int64, name string, ownerID int64) (Workspace, error) {
	src, err := s.GetWorkspace(ctx, srcID)
	if err != nil {
		return Workspace{}, err
	}
	agents, err := s.ListAgents(ctx, srcID)
	if err != nil {
		return Workspace{}, err
	}
	wires, err := s.ListWires(ctx, srcID)
	if err != nil {
		return Workspace{}, err
	}

	// The orchestrator is created with the workspace, so its definition has to
	// be in hand before the workspace exists. Its model travels as a pointer:
	// an orchestrator with no model is a workspace that has not been finished
	// yet, and the copy must be allowed to be in the same state rather than
	// fail on a foreign key.
	var orch AgentSpec
	for _, a := range agents {
		if a.IsOrchestrator {
			orch = AgentSpec{Name: a.Name, Role: a.Role, Avoid: a.Avoid, ModelID: a.ModelID}
		}
	}
	clone, err := s.CreateWorkspaceSpec(ctx, name, src.Description, orch, ownerID)
	if err != nil {
		return Workspace{}, err
	}

	// Map source agent ids to their copies so wires can be rebuilt.
	idMap := map[int64]int64{}
	cloneAgents, err := s.ListAgents(ctx, clone.ID)
	if err != nil {
		return Workspace{}, err
	}
	for _, a := range agents {
		if a.IsOrchestrator {
			for _, ca := range cloneAgents {
				if ca.IsOrchestrator {
					idMap[a.ID] = ca.ID
				}
			}
			continue
		}
		created, err := s.CreateAgentSpec(ctx, clone.ID, AgentSpec{
			Name: a.Name, Role: a.Role, Avoid: a.Avoid, ModelID: a.ModelID,
		})
		if err != nil {
			return Workspace{}, fmt.Errorf("clone: copy agent %q: %w", a.Name, err)
		}
		idMap[a.ID] = created.ID
	}

	for _, wire := range wires {
		from, okF := idMap[wire.FromAgentID]
		to, okT := idMap[wire.ToAgentID]
		if !okF || !okT {
			continue
		}
		if _, err := s.CreateWire(ctx, clone.ID, from, to, wire.Label); err != nil {
			return Workspace{}, fmt.Errorf("clone: copy wire: %w", err)
		}
	}

	slog.Info("workspace cloned", "from", srcID, "to", clone.ID, "agents", len(agents), "wires", len(wires))
	return s.GetWorkspace(ctx, clone.ID)
}

// DeleteWorkspace removes the workspace and everything the database holds about
// it. The caller removes its directory; see workdir.Remove and the delete
// handler, which does both.
//
// Most of it cascades from the foreign key, but not all of it, and the
// exceptions are deliberate rather than oversights: search_queries, inlet_runs
// and gear_connections carry a workspace_id with NO foreign key, because an
// audit row must be able to outlive the thing it audits. That reasoning is right
// while the workspace exists and stops applying the moment it does not — what is
// left is rows nobody can reach, in tables nothing prunes, keyed to an id SQLite
// will hand out again.
//
// So they are deleted explicitly, in the same transaction as the workspace. One
// transaction rather than four statements because a half-deleted workspace is
// worse than either outcome: the operator sees it gone and its audit rows stay
// forever, or the reverse.
func (s *Store) DeleteWorkspace(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete workspace %d: %w", id, err)
	}
	defer tx.Rollback()

	for _, table := range []string{"search_queries", "inlet_runs", "gear_connections"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE workspace_id = ?`, id); err != nil {
			return fmt.Errorf("delete workspace %d: clear %s: %w", id, table, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete workspace %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("workspace %d: %w", id, ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete workspace %d: %w", id, err)
	}
	slog.Info("workspace deleted (agents, wires, messages cascade; runs and audit rows removed)", "id", id)
	return nil
}

const agentSelect = `
	SELECT a.id, a.workspace_id, a.name, a.kind, a.role, a.avoid, a.model_id, a.is_orchestrator,
	       COALESCE(p.name || ' / ' || COALESCE(NULLIF(m.label, ''), m.model_name), ''),
	       a.pos_x, a.pos_y, a.branch
	FROM agents a
	LEFT JOIN models m ON m.id = a.model_id
	LEFT JOIN providers p ON p.id = m.provider_id`

func scanAgent(row interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var orch int
	if err := row.Scan(&a.ID, &a.WorkspaceID, &a.Name, &a.Kind, &a.Role, &a.Avoid, &a.ModelID, &orch,
		&a.ModelLabel, &a.PosX, &a.PosY, &a.Branch); err != nil {
		return Agent{}, err
	}
	a.IsOrchestrator = orch == 1
	return a, nil
}

// SetAgentAvoid replaces an agent's standing prohibitions. Like the canvas
// position, it is its own call rather than a field of UpdateAgent: a patch
// that only changes the rules must not read and write back a name, role and
// model that somebody else may have changed in between.
func (s *Store) SetAgentAvoid(ctx context.Context, id int64, avoid string) (Agent, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET avoid = ?, updated_at = ? WHERE id = ?`, avoid, now(), id)
	if err != nil {
		return Agent{}, fmt.Errorf("set agent %d prohibitions: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Agent{}, fmt.Errorf("agent %d: %w", id, ErrNotFound)
	}
	slog.Info("agent prohibitions changed", "agent_id", id, "rules", len(Rules(avoid)))
	return s.GetAgent(ctx, id)
}

// Rules splits an avoid text into the prohibitions it states: one per
// non-empty line, trimmed. Both the prompt and the log go through it, so what
// an operator is told they wrote is what the agent is told to obey.
func Rules(avoid string) []string {
	out := []string{}
	for _, line := range strings.Split(avoid, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// SetAgentPosition stores a blueprint canvas position.
func (s *Store) SetAgentPosition(ctx context.Context, id int64, x, y float64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET pos_x = ?, pos_y = ?, updated_at = ? WHERE id = ?`, x, y, now(), id)
	if err != nil {
		return fmt.Errorf("set agent %d position: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("agent %d: %w", id, ErrNotFound)
	}
	return nil
}

// CanDelegate reports whether a wire grants from -> to. The blueprint graph
// is the capability: no edge, no delegation.
func (s *Store) CanDelegate(ctx context.Context, wsID, fromAgentID, toAgentID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wires WHERE workspace_id = ? AND from_agent_id = ? AND to_agent_id = ?`,
		wsID, fromAgentID, toAgentID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check wire %d->%d: %w", fromAgentID, toAgentID, err)
	}
	return n > 0, nil
}

// DelegationTargets lists the agents a given agent is wired to.
func (s *Store) DelegationTargets(ctx context.Context, wsID, fromAgentID int64) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		agentSelect+` JOIN wires w ON w.to_agent_id = a.id
		 WHERE w.workspace_id = ? AND w.from_agent_id = ? ORDER BY a.name`, wsID, fromAgentID)
	if err != nil {
		return nil, fmt.Errorf("delegation targets for %d: %w", fromAgentID, err)
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan delegation target: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateAgent creates a worker agent bound to modelID — the shape the UI and
// the orchestrator's own tools have, where a model has always been chosen
// from the catalog first.
func (s *Store) CreateAgent(ctx context.Context, wsID int64, name, role string, modelID int64) (Agent, error) {
	return s.CreateAgentSpec(ctx, wsID, AgentSpec{Name: name, Role: role, ModelID: &modelID})
}

// CreateAgentSpec creates a worker agent from a full definition, including a
// model that may be absent.
func (s *Store) CreateAgentSpec(ctx context.Context, wsID int64, spec AgentSpec) (Agent, error) {
	ws, err := s.GetWorkspace(ctx, wsID)
	if err != nil {
		return Agent{}, err
	}
	id, err := insertAgent(ctx, s.db, wsID, ws.Branch, spec, false)
	if err != nil {
		return Agent{}, fmt.Errorf("create agent: %w", err)
	}
	slog.Info("agent created", "id", id, "workspace_id", wsID, "name", spec.Name, "model_id", modelForLog(spec.ModelID))
	return s.GetAgent(ctx, id)
}

func (s *Store) GetAgent(ctx context.Context, id int64) (Agent, error) {
	a, err := scanAgent(s.db.QueryRowContext(ctx, agentSelect+` WHERE a.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, fmt.Errorf("agent %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Agent{}, fmt.Errorf("get agent %d: %w", id, err)
	}
	return a, nil
}

// GetAgentByName resolves an agent by its name within a workspace — the
// handle the orchestrator's tools use.
func (s *Store) GetAgentByName(ctx context.Context, wsID int64, name string) (Agent, error) {
	a, err := scanAgent(s.db.QueryRowContext(ctx, agentSelect+` WHERE a.workspace_id = ? AND a.name = ?`, wsID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, fmt.Errorf("agent %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Agent{}, fmt.Errorf("get agent %q: %w", name, err)
	}
	return a, nil
}

func (s *Store) ListAgents(ctx context.Context, wsID int64) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, agentSelect+` WHERE a.workspace_id = ? ORDER BY a.is_orchestrator DESC, a.name`, wsID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAgent patches non-nil fields. The orchestrator's name is fixed;
// its role and model are editable.
func (s *Store) UpdateAgent(ctx context.Context, id int64, name, role *string, modelID *int64) (Agent, error) {
	a, err := s.GetAgent(ctx, id)
	if err != nil {
		return Agent{}, err
	}
	if name != nil {
		if a.IsOrchestrator {
			return Agent{}, errors.New("the orchestrator cannot be renamed")
		}
		a.Name = strings.TrimSpace(*name)
		if a.Name == "" {
			return Agent{}, errors.New("name cannot be empty")
		}
	}
	if role != nil {
		a.Role = *role
	}
	mid := a.ModelID
	if modelID != nil {
		mid = modelID
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE agents SET name = ?, role = ?, model_id = ?, updated_at = ? WHERE id = ?`,
		a.Name, a.Role, mid, now(), id)
	if err := asConflict(err, fmt.Sprintf("agent %q in this workspace", a.Name)); err != nil {
		return Agent{}, fmt.Errorf("update agent %d: %w", id, err)
	}
	slog.Info("agent updated", "id", id, "name", a.Name, "model_changed", modelID != nil)
	return s.GetAgent(ctx, id)
}

func (s *Store) DeleteAgent(ctx context.Context, id int64) error {
	a, err := s.GetAgent(ctx, id)
	if err != nil {
		return err
	}
	if a.IsOrchestrator {
		return errors.New("the orchestrator cannot be deleted — it is the workspace entry point")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete agent %d: %w", id, err)
	}
	slog.Info("agent deleted", "id", id, "name", a.Name)
	return nil
}

func (s *Store) CreateWire(ctx context.Context, wsID, fromAgentID, toAgentID int64, label string) (Wire, error) {
	if fromAgentID == toAgentID {
		return Wire{}, errors.New("an agent cannot be wired to itself")
	}
	for _, id := range []int64{fromAgentID, toAgentID} {
		a, err := s.GetAgent(ctx, id)
		if err != nil {
			return Wire{}, err
		}
		if a.WorkspaceID != wsID {
			return Wire{}, fmt.Errorf("agent %d belongs to another workspace", id)
		}
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO wires (workspace_id, from_agent_id, to_agent_id, label, created_at) VALUES (?, ?, ?, ?, ?)`,
		wsID, fromAgentID, toAgentID, label, now())
	if err := asConflict(err, "wire"); err != nil {
		return Wire{}, fmt.Errorf("create wire: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("wire created", "id", id, "workspace_id", wsID, "from", fromAgentID, "to", toAgentID)
	return Wire{ID: id, WorkspaceID: wsID, FromAgentID: fromAgentID, ToAgentID: toAgentID, Label: label}, nil
}

func (s *Store) ListWires(ctx context.Context, wsID int64) ([]Wire, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, workspace_id, from_agent_id, to_agent_id, label FROM wires WHERE workspace_id = ? ORDER BY id`, wsID)
	if err != nil {
		return nil, fmt.Errorf("list wires: %w", err)
	}
	defer rows.Close()
	out := []Wire{}
	for rows.Next() {
		var w Wire
		if err := rows.Scan(&w.ID, &w.WorkspaceID, &w.FromAgentID, &w.ToAgentID, &w.Label); err != nil {
			return nil, fmt.Errorf("scan wire: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWire(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM wires WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete wire %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("wire %d: %w", id, ErrNotFound)
	}
	slog.Info("wire deleted", "id", id)
	return nil
}

type ContextBinding struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	Path        string `json:"path"`
	AgentID     *int64 `json:"agent_id"` // nil = workspace-wide
}

func (s *Store) CreateContextBinding(ctx context.Context, wsID int64, path string, agentID *int64) (ContextBinding, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ContextBinding{}, errors.New("path is required")
	}
	if _, err := s.GetWorkspace(ctx, wsID); err != nil {
		return ContextBinding{}, err
	}
	if agentID != nil {
		a, err := s.GetAgent(ctx, *agentID)
		if err != nil {
			return ContextBinding{}, err
		}
		if a.WorkspaceID != wsID {
			return ContextBinding{}, fmt.Errorf("agent %d belongs to another workspace", *agentID)
		}
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO context_bindings (workspace_id, path, agent_id, created_at) VALUES (?, ?, ?, ?)`,
		wsID, path, agentID, now())
	if err := asConflict(err, fmt.Sprintf("context binding %q", path)); err != nil {
		return ContextBinding{}, fmt.Errorf("create context binding: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("context binding created", "id", id, "workspace_id", wsID, "path", path, "agent_id", agentID)
	return ContextBinding{ID: id, WorkspaceID: wsID, Path: path, AgentID: agentID}, nil
}

func (s *Store) ListContextBindings(ctx context.Context, wsID int64) ([]ContextBinding, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, workspace_id, path, agent_id FROM context_bindings WHERE workspace_id = ? ORDER BY path`, wsID)
	if err != nil {
		return nil, fmt.Errorf("list context bindings: %w", err)
	}
	defer rows.Close()
	out := []ContextBinding{}
	for rows.Next() {
		var b ContextBinding
		if err := rows.Scan(&b.ID, &b.WorkspaceID, &b.Path, &b.AgentID); err != nil {
			return nil, fmt.Errorf("scan context binding: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BindingsForAgent returns the paths an agent sees: workspace-wide plus its
// own, deduplicated, stable order.
func (s *Store) BindingsForAgent(ctx context.Context, wsID, agentID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT path FROM context_bindings
		 WHERE workspace_id = ? AND (agent_id IS NULL OR agent_id = ?)
		 ORDER BY path`, wsID, agentID)
	if err != nil {
		return nil, fmt.Errorf("bindings for agent %d: %w", agentID, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan binding path: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteContextBinding(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM context_bindings WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete context binding %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("context binding %d: %w", id, ErrNotFound)
	}
	slog.Info("context binding deleted", "id", id)
	return nil
}

// DeleteContextBindingByPath removes a binding by its (path, scope) pair —
// the handle the orchestrator's tools use.
func (s *Store) DeleteContextBindingByPath(ctx context.Context, wsID int64, path string, agentID *int64) error {
	q := `DELETE FROM context_bindings WHERE workspace_id = ? AND path = ? AND agent_id `
	args := []any{wsID, path}
	if agentID == nil {
		q += `IS NULL`
	} else {
		q += `= ?`
		args = append(args, *agentID)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("delete context binding %q: %w", path, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("context binding %q: %w", path, ErrNotFound)
	}
	slog.Info("context binding deleted", "workspace_id", wsID, "path", path, "agent_id", agentID)
	return nil
}

// AppendMessage persists one timeline entry and returns it.
func (s *Store) AppendMessage(ctx context.Context, wsID int64, agentID *int64, kind, content, meta string) (Message, error) {
	if meta == "" {
		meta = "{}"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO ws_messages (workspace_id, agent_id, kind, content, meta, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		wsID, agentID, kind, content, meta, now())
	if err != nil {
		return Message{}, fmt.Errorf("append message: %w", err)
	}
	id, _ := res.LastInsertId()
	var m Message
	err = s.db.QueryRowContext(ctx,
		`SELECT id, workspace_id, agent_id, kind, content, meta, created_at FROM ws_messages WHERE id = ?`, id).
		Scan(&m.ID, &m.WorkspaceID, &m.AgentID, &m.Kind, &m.Content, &m.Meta, &m.CreatedAt)
	if err != nil {
		return Message{}, fmt.Errorf("read back message %d: %w", id, err)
	}
	return m, nil
}

// DeleteMessage removes one entry from the timeline. The timeline is the
// other half of an agent's memory — it is replayed into the model on every
// turn — so an operator who spots something wrong in it needs a way to take
// it back out, not only to edit documents.
//
// A tool call and its result are removed together: leaving one without the
// other would produce a history providers reject. repairHistory would
// synthesise a replacement, but silently keeping half of what someone asked
// to forget is not what they asked for.
func (s *Store) DeleteMessage(ctx context.Context, id int64) error {
	var wsID int64
	var kind, meta string
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id, kind, meta FROM ws_messages WHERE id = ?`, id).
		Scan(&wsID, &kind, &meta)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("message %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("delete message %d: %w", id, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete message: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM ws_messages WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete message %d: %w", id, err)
	}

	// An assistant turn's tool results follow it directly; drop them too.
	if kind == "assistant" && strings.Contains(meta, "tool_calls") {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM ws_messages
			WHERE workspace_id = ? AND kind IN ('tool_result', 'delegation') AND id > ?
			  AND id < COALESCE((SELECT MIN(id) FROM ws_messages
			                     WHERE workspace_id = ? AND id > ? AND kind IN ('user', 'assistant')), 1e18)`,
			wsID, id, wsID, id); err != nil {
			return fmt.Errorf("delete message %d: orphaned results: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete message: commit: %w", err)
	}
	slog.Info("timeline entry forgotten", "message_id", id, "workspace_id", wsID, "kind", kind)
	return nil
}

// WorkspaceOfMessage resolves a timeline entry back to its workspace so the
// access check can run before anything is forgotten.
func (s *Store) WorkspaceOfMessage(ctx context.Context, id int64) (int64, error) {
	var wsID int64
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM ws_messages WHERE id = ?`, id).Scan(&wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("message %d: %w", id, ErrNotFound)
	}
	return wsID, err
}

// ListMessages returns the workspace timeline, optionally filtered to one
// agent (the per-agent activity view), oldest first.
func (s *Store) ListMessages(ctx context.Context, wsID int64, agentID *int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	q := `SELECT id, workspace_id, agent_id, kind, content, meta, created_at FROM ws_messages WHERE workspace_id = ?`
	args := []any{wsID}
	if agentID != nil {
		q += ` AND agent_id = ?`
		args = append(args, *agentID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.WorkspaceID, &m.AgentID, &m.Kind, &m.Content, &m.Meta, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	// Reverse to oldest-first for display.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
