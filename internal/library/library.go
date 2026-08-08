// Package library is the shared catalogue of instructions: reusable
// guidance an agent or a person wrote once and everyone can bind afterwards
// instead of retyping it into every role.
//
// It deliberately mirrors the gear catalogue in discovery — named,
// described, tagged, with provenance — and deliberately differs in two
// ways. There is no approval gate, because an instruction is text that
// reaches a prompt rather than code that runs on the machine; a gate
// without a threat model only teaches people to click through gates. And
// the text is not stored here: it lives in Contextverse at the recorded
// path, so versions and history stay where context already belongs, and
// this package holds nothing but the index.
package library

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
)

// Root is where instructions live in the Contextverse space — a sibling of
// the workspaces root, so the library is visible to any contextd client and
// not buried inside one workspace.
const Root = "library"

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,60}$`)

type Instruction struct {
	ID                int64    `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Tags              []string `json:"tags"`
	Path              string   `json:"path"`
	OriginWorkspaceID *int64   `json:"origin_workspace_id"`
	OriginWorkspace   string   `json:"origin_workspace"`
	CreatedByAgentID  *int64   `json:"created_by_agent_id"`
	CreatedByAgent    string   `json:"created_by_agent"`
	UpdatedAt         string   `json:"updated_at"`
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// PathFor names an instruction's file in the space.
func PathFor(name string) string { return Root + "/" + name + ".md" }

func asConflict(err error, what string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%s: %w", what, ErrConflict)
	}
	return err
}

// Save records an instruction in the index. The caller writes the text to
// Contextverse; this only remembers where it is and what it is for.
// Re-saving an existing name updates its description and tags — the text
// itself is versioned by Contextverse, not here.
func (s *Store) Save(ctx context.Context, name, description string, tags []string, wsID, agentID int64) (Instruction, error) {
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) {
		return Instruction{}, fmt.Errorf("instruction name %q is invalid: lowercase letters, digits, dashes and underscores (2-61 chars), starting with a letter", name)
	}
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return Instruction{}, err
	}

	var originWS, originAgent any
	if wsID != 0 {
		originWS = wsID
	}
	if agentID != 0 {
		originAgent = agentID
	}

	existing, err := s.GetByName(ctx, name)
	switch {
	case err == nil:
		if _, err := s.db.ExecContext(ctx,
			`UPDATE instructions SET description = ?, tags = ?, updated_at = ? WHERE id = ?`,
			description, string(tagsJSON), now(), existing.ID); err != nil {
			return Instruction{}, fmt.Errorf("update instruction %q: %w", name, err)
		}
		slog.Info("instruction updated", "id", existing.ID, "name", name)
		return s.Get(ctx, existing.ID)
	case !errors.Is(err, ErrNotFound):
		return Instruction{}, err
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO instructions (name, description, tags, path, origin_workspace_id, created_by_agent_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		name, description, string(tagsJSON), PathFor(name), originWS, originAgent, now(), now())
	if err := asConflict(err, fmt.Sprintf("instruction %q", name)); err != nil {
		return Instruction{}, fmt.Errorf("save instruction: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("instruction saved to the library", "id", id, "name", name, "workspace_id", wsID, "agent_id", agentID)
	return s.Get(ctx, id)
}

const selectCols = `
	SELECT i.id, i.name, i.description, i.tags, i.path, i.origin_workspace_id,
	       COALESCE(w.name, ''), i.created_by_agent_id, COALESCE(a.name, ''), i.updated_at
	FROM instructions i
	LEFT JOIN workspaces w ON w.id = i.origin_workspace_id
	LEFT JOIN agents a ON a.id = i.created_by_agent_id`

func scan(row interface{ Scan(...any) error }) (Instruction, error) {
	var in Instruction
	var tags string
	if err := row.Scan(&in.ID, &in.Name, &in.Description, &tags, &in.Path, &in.OriginWorkspaceID,
		&in.OriginWorkspace, &in.CreatedByAgentID, &in.CreatedByAgent, &in.UpdatedAt); err != nil {
		return Instruction{}, err
	}
	if err := json.Unmarshal([]byte(tags), &in.Tags); err != nil {
		slog.Warn("instruction has unparseable tags", "instruction", in.Name, "err", err)
		in.Tags = []string{}
	}
	return in, nil
}

func (s *Store) Get(ctx context.Context, id int64) (Instruction, error) {
	in, err := scan(s.db.QueryRowContext(ctx, selectCols+` WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Instruction{}, fmt.Errorf("instruction %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Instruction{}, fmt.Errorf("get instruction %d: %w", id, err)
	}
	return in, nil
}

func (s *Store) GetByName(ctx context.Context, name string) (Instruction, error) {
	in, err := scan(s.db.QueryRowContext(ctx, selectCols+` WHERE i.name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Instruction{}, fmt.Errorf("instruction %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Instruction{}, fmt.Errorf("get instruction %q: %w", name, err)
	}
	return in, nil
}

// List returns the catalogue, optionally narrowed by tag or free text over
// name and description — the same filters the gear catalogue takes, so the
// two feel like one idea rather than two.
func (s *Store) List(ctx context.Context, tag, query string) ([]Instruction, error) {
	rows, err := s.db.QueryContext(ctx, selectCols+` ORDER BY i.name`)
	if err != nil {
		return nil, fmt.Errorf("list instructions: %w", err)
	}
	defer rows.Close()

	out := []Instruction{}
	for rows.Next() {
		in, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan instruction: %w", err)
		}
		if tag != "" && !containsFold(in.Tags, tag) {
			continue
		}
		if query != "" {
			q := strings.ToLower(query)
			if !strings.Contains(strings.ToLower(in.Name), q) &&
				!strings.Contains(strings.ToLower(in.Description), q) {
				continue
			}
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// Delete removes an instruction from the index. The text stays in
// Contextverse — dropping a catalogue entry is not a reason to destroy
// someone's writing, and Contextverse owns its own deletion.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM instructions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete instruction %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("instruction %d: %w", id, ErrNotFound)
	}
	slog.Info("instruction removed from the library; its text remains in Contextverse", "id", id)
	return nil
}
