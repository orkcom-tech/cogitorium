// Package planboard is the order of work, written down before the work starts.
//
// An instruction says how an agent should behave; a gear says what it may
// call. Neither says what comes first. In a conversation that is fine — the
// model decides an order and the person corrects it. In a workflow that fires
// on a clock it is not: nobody is watching, and a run that chose a different
// order than last night is a run nobody can compare to last night's.
//
// So a planboard is a sequence, and the ENGINE walks it. The model is handed
// one step and asked to do that step; it cannot skip to step five because
// step five is not in front of it. What the model decides is HOW a step is
// done, which is the part worth a model. The order is not.
//
// The position is kept per binding, and a binding is either one agent's or a
// whole workspace's. That is the difference between "this agent's running
// order" and "this workflow's plan, whoever runs next".
package planboard

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
	// ErrEmpty is returned rather than saving a plan with nothing in it. A
	// plan with no steps is a position that can never be satisfied: the engine
	// would hand the agent nothing and wait for it to be finished.
	ErrEmpty = errors.New("a planboard needs at least one step")
)

// Mode decides what happens to the position when a run begins.
type Mode string

const (
	// ModeResume carries the position between runs. A nightly cron picks up
	// where last night stopped.
	ModeResume Mode = "resume"
	// ModeRestart begins at step one every run. The plan is a procedure to be
	// walked from the top, not a journey through a backlog.
	ModeRestart Mode = "restart"
)

func (m Mode) valid() bool { return m == ModeResume || m == ModeRestart }

// Same name rule as gears and instructions, so the three catalogues can be
// searched with one habit.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,60}$`)

type Planboard struct {
	ID                int64    `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Tags              []string `json:"tags"`
	Mode              Mode     `json:"mode"`
	Steps             []Step   `json:"steps"`
	OriginWorkspaceID *int64   `json:"origin_workspace_id"`
	OriginWorkspace   string   `json:"origin_workspace"`
	CreatedByAgentID  *int64   `json:"created_by_agent_id"`
	CreatedByAgent    string   `json:"created_by_agent"`
	UpdatedAt         string   `json:"updated_at"`
}

// Step is one thing to do, in the order it is to be done.
type Step struct {
	Ordinal int    `json:"ordinal"`
	Title   string `json:"title"`
	Body    string `json:"body"`
}

// Binding attaches a plan to an agent, or to every agent in a workspace.
type Binding struct {
	ID          int64  `json:"id"`
	PlanboardID int64  `json:"planboard_id"`
	Planboard   string `json:"planboard"`
	WorkspaceID int64  `json:"workspace_id"`
	// AgentID nil means every agent in the workspace shares one position.
	AgentID *int64 `json:"agent_id"`
	Agent   string `json:"agent"`
}

// State is where the work stands for one binding.
type State struct {
	Step        int    `json:"step"`
	Cycle       int    `json:"cycle"`
	BlockedNote string `json:"blocked_note"`
	UpdatedAt   string `json:"updated_at"`
}

// Assignment is what the engine needs to run one step: the plan, the binding
// that owns the position, where that position is, and the step it points at.
type Assignment struct {
	Planboard Planboard
	Binding   Binding
	State     State
	Current   Step
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func ValidateName(name string) error {
	if !nameRe.MatchString(strings.TrimSpace(name)) {
		return fmt.Errorf("planboard name %q is invalid: lowercase letters, digits, dashes and underscores (2-61 chars), starting with a letter", name)
	}
	return nil
}

func asConflict(err error, what string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%s: %w", what, ErrConflict)
	}
	return err
}

// Save writes a plan and its steps as one thing.
//
// The steps are replaced wholesale rather than patched. A plan is an order,
// and an order edited step by step passes through states that are not orders —
// two step threes, or a gap where step four was — while a position is pointing
// into it.
func (s *Store) Save(ctx context.Context, name, description string, tags []string, mode Mode, steps []Step, wsID, agentID int64) (Planboard, error) {
	name = strings.TrimSpace(name)
	if err := ValidateName(name); err != nil {
		return Planboard{}, err
	}
	if mode == "" {
		mode = ModeResume
	}
	if !mode.valid() {
		return Planboard{}, fmt.Errorf("planboard mode %q is invalid: resume or restart", mode)
	}
	steps = clean(steps)
	if len(steps) == 0 {
		return Planboard{}, ErrEmpty
	}
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return Planboard{}, err
	}

	var originWS, originAgent any
	if wsID != 0 {
		originWS = wsID
	}
	if agentID != 0 {
		originAgent = agentID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Planboard{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM planboards WHERE name = ?`, name).Scan(&id)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx,
			`UPDATE planboards SET description = ?, tags = ?, mode = ?, updated_at = ? WHERE id = ?`,
			description, string(tagsJSON), string(mode), now(), id); err != nil {
			return Planboard{}, fmt.Errorf("update planboard %q: %w", name, err)
		}
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `
			INSERT INTO planboards (name, description, tags, mode, origin_workspace_id, created_by_agent_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			name, description, string(tagsJSON), string(mode), originWS, originAgent, now(), now())
		if err := asConflict(err, fmt.Sprintf("planboard %q", name)); err != nil {
			return Planboard{}, fmt.Errorf("save planboard: %w", err)
		}
		id, _ = res.LastInsertId()
	default:
		return Planboard{}, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM planboard_steps WHERE planboard_id = ?`, id); err != nil {
		return Planboard{}, fmt.Errorf("replace steps of planboard %q: %w", name, err)
	}
	for i, st := range steps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO planboard_steps (planboard_id, ordinal, title, body) VALUES (?, ?, ?, ?)`,
			id, i+1, st.Title, st.Body); err != nil {
			return Planboard{}, fmt.Errorf("save step %d of planboard %q: %w", i+1, name, err)
		}
	}

	// A shorter plan leaves positions pointing past the end. Pull them back to
	// the last step rather than leaving a binding the engine cannot serve.
	if _, err := tx.ExecContext(ctx, `
		UPDATE planboard_state SET step = ?, updated_at = ?
		WHERE step > ? AND binding_id IN (SELECT id FROM planboard_bindings WHERE planboard_id = ?)`,
		len(steps), now(), len(steps), id); err != nil {
		return Planboard{}, fmt.Errorf("clamp positions of planboard %q: %w", name, err)
	}

	if err := tx.Commit(); err != nil {
		return Planboard{}, err
	}
	slog.Info("planboard saved", "id", id, "name", name, "steps", len(steps), "mode", mode)
	return s.Get(ctx, id)
}

// clean drops steps with no title. A blank line in a list somebody pasted is
// not a step, and saving it would give the agent an empty instruction to
// report done.
func clean(in []Step) []Step {
	out := make([]Step, 0, len(in))
	for _, st := range in {
		st.Title = strings.TrimSpace(st.Title)
		st.Body = strings.TrimSpace(st.Body)
		if st.Title == "" {
			continue
		}
		out = append(out, st)
	}
	return out
}

const selectCols = `
	SELECT p.id, p.name, p.description, p.tags, p.mode, p.origin_workspace_id,
	       COALESCE(w.name, ''), p.created_by_agent_id, COALESCE(a.name, ''), p.updated_at
	FROM planboards p
	LEFT JOIN workspaces w ON w.id = p.origin_workspace_id
	LEFT JOIN agents a ON a.id = p.created_by_agent_id`

func scan(row interface{ Scan(...any) error }) (Planboard, error) {
	var p Planboard
	var tags, mode string
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &tags, &mode, &p.OriginWorkspaceID,
		&p.OriginWorkspace, &p.CreatedByAgentID, &p.CreatedByAgent, &p.UpdatedAt); err != nil {
		return Planboard{}, err
	}
	p.Mode = Mode(mode)
	if err := json.Unmarshal([]byte(tags), &p.Tags); err != nil {
		slog.Warn("planboard has unparseable tags", "planboard", p.Name, "err", err)
		p.Tags = []string{}
	}
	return p, nil
}

func (s *Store) steps(ctx context.Context, id int64) ([]Step, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ordinal, title, body FROM planboard_steps WHERE planboard_id = ? ORDER BY ordinal`, id)
	if err != nil {
		return nil, fmt.Errorf("steps of planboard %d: %w", id, err)
	}
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.Ordinal, &st.Title, &st.Body); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (Planboard, error) {
	p, err := scan(s.db.QueryRowContext(ctx, selectCols+` WHERE p.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Planboard{}, fmt.Errorf("planboard %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Planboard{}, fmt.Errorf("get planboard %d: %w", id, err)
	}
	p.Steps, err = s.steps(ctx, p.ID)
	return p, err
}

func (s *Store) GetByName(ctx context.Context, name string) (Planboard, error) {
	p, err := scan(s.db.QueryRowContext(ctx, selectCols+` WHERE p.name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Planboard{}, fmt.Errorf("planboard %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Planboard{}, fmt.Errorf("get planboard %q: %w", name, err)
	}
	p.Steps, err = s.steps(ctx, p.ID)
	return p, err
}

// List returns the catalogue, narrowed the same way the gear and instruction
// catalogues narrow.
func (s *Store) List(ctx context.Context, tag, query string) ([]Planboard, error) {
	rows, err := s.db.QueryContext(ctx, selectCols+` ORDER BY p.name`)
	if err != nil {
		return nil, fmt.Errorf("list planboards: %w", err)
	}
	defer rows.Close()

	found := []Planboard{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan planboard: %w", err)
		}
		if tag != "" && !containsFold(p.Tags, tag) {
			continue
		}
		if query != "" {
			q := strings.ToLower(query)
			if !strings.Contains(strings.ToLower(p.Name), q) &&
				!strings.Contains(strings.ToLower(p.Description), q) {
				continue
			}
		}
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range found {
		st, err := s.steps(ctx, found[i].ID)
		if err != nil {
			return nil, err
		}
		found[i].Steps = st
	}
	return found, nil
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// Delete removes the plan, its steps, its bindings and their positions. All
// four are the same object; leaving a position behind would resurrect the
// plan the moment a plan of that name existed again.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM planboards WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete planboard %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("planboard %d: %w", id, ErrNotFound)
	}
	slog.Info("planboard deleted", "id", id)
	return nil
}
