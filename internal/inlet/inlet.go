// Package inlet owns the doors into a workspace from outside: their
// addresses, their keys, the tasks behind them, and the ledger of what came
// through.
//
// An inlet is not an agent. It has no model, no role and no memory. A caller
// posts to POST /i/{address}/{task-name} with the inlet's key; the task says
// what the body must look like, which agent does the work, and what sentence
// that agent is given along with the payload. When the agent has answered, the
// inlet's job is over.
package inlet

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
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

// What a task accepts. A JSON task's body is checked against its schema before
// any model is called; a file task's body is written into the workspace and the
// agent is given the path.
const (
	AcceptsJSON = "json"
	AcceptsFile = "file"
)

// KeyPrefix marks an inlet key as an inlet key. User tokens start "cg-", so a
// string found in a log, a CI variable or a paste is identifiable on sight as
// belonging to a door rather than to a person — which is the difference
// between revoking one inlet and forcing everybody to sign in again.
const KeyPrefix = "cgi"

// addressRe and nameRe keep both halves of the delivery URL readable and
// unambiguous as path segments: no slashes, no escapes, no case that a proxy
// or a client library might normalise differently on the way in.
var (
	addressRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,48}$`)
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,48}$`)
)

// Inlet is one door. The key hash is unexported and never leaves this package:
// the struct is serialised straight to the management API, and a credential
// digest is not something an operator's browser needs a copy of.
type Inlet struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	Address     string `json:"address"`
	Description string `json:"description"`
	// HasKey is what the UI needs: whether this door can be used at all. An
	// inlet with no key issued refuses every delivery.
	HasKey        bool   `json:"has_key"`
	KeyIssuedAt   string `json:"key_issued_at"`
	KeyLastUsedAt string `json:"key_last_used_at"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	Tasks         []Task `json:"tasks"`

	keyHash string
}

// Task is one job behind a door: how the caller selects it, what it accepts,
// who does the work, and what they are told to do with it.
type Task struct {
	ID      int64  `json:"id"`
	InletID int64  `json:"inlet_id"`
	Name    string `json:"name"`
	Accepts string `json:"accepts"`
	// Schema is a JSON Schema object, restricted to the subset in schema.go.
	// Empty object means any JSON body is accepted.
	Schema string `json:"schema"`
	// ContentType is the media type a file task accepts, e.g. "image/png" or
	// "image/*". Empty means any type.
	ContentType string `json:"content_type"`
	AgentName   string `json:"agent_name"`
	Instruction string `json:"instruction"`
	// Expect is what this task declares success to be: which gear must have
	// run, how many files must have appeared, what shape the answer must have.
	// It is checked against the run's RECORD, never against what the agent
	// wrote. The zero value declares nothing and is what every task carried
	// before this existed.
	Expect Expect `json:"expect"`
	// CallbackURL is where this install tells somebody a run finished. Empty is
	// the ordinary case and means nobody is told; the answer then reaches the
	// caller on the connection they delivered on, or by reading the run back.
	//
	// The host allowlist that decides whether this URL may be reached at all
	// lives in the config file rather than here: who the machine may talk to is
	// an operator's decision about the machine, and a task is editable by
	// anyone who can reach the workspace.
	CallbackURL string `json:"callback_url"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
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

// hashKey stores a key the way user tokens are stored: only the digest. The
// hashing is written out here rather than reached for across the identity
// package boundary because an inlet key is not a token — it resolves to a door,
// not to a user, and one shared verifier is a single edit away from letting
// either be presented where the other is expected.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// NewKey returns a key for an address. This value is the one chance anyone has
// to copy it: only its hash is stored.
func NewKey(address string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate inlet key: %w", err)
	}
	return fmt.Sprintf("%s-%s-%s", KeyPrefix, address, hex.EncodeToString(raw)), nil
}

// MatchesKey reports whether the presented key opens this door. An inlet that
// has never been issued a key matches nothing at all — including the empty
// string, which is what a request with no Authorization header would offer.
func (i Inlet) MatchesKey(key string) bool {
	if i.keyHash == "" || key == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashKey(key)), []byte(i.keyHash)) == 1
}

// CreateInlet opens a door on a workspace. It has no key yet: issuing one is a
// separate act, so that "this inlet exists" and "this inlet can be used" are
// two decisions an operator makes on purpose rather than one that happens by
// itself.
func (s *Store) CreateInlet(ctx context.Context, wsID int64, address, description string) (Inlet, error) {
	address = strings.TrimSpace(strings.ToLower(address))
	if !addressRe.MatchString(address) {
		return Inlet{}, fmt.Errorf("inlet address %q is invalid: use lowercase letters, digits and hyphens (2-49 characters) starting with a letter — it is a path segment in POST /i/{address}/{task}", address)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO inlets (workspace_id, address, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		wsID, address, description, now(), now())
	if err := asConflict(err, fmt.Sprintf("inlet address %q is already taken by another door in this install", address)); err != nil {
		return Inlet{}, fmt.Errorf("create inlet: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("inlet created", "id", id, "workspace_id", wsID, "address", address)
	return s.GetInlet(ctx, id)
}

const inletSelect = `SELECT id, workspace_id, address, description,
       COALESCE(key_hash, ''), COALESCE(key_issued_at, ''), COALESCE(key_last_used_at, ''),
       created_at, updated_at
  FROM inlets`

func scanInlet(row interface{ Scan(...any) error }) (Inlet, error) {
	var i Inlet
	if err := row.Scan(&i.ID, &i.WorkspaceID, &i.Address, &i.Description,
		&i.keyHash, &i.KeyIssuedAt, &i.KeyLastUsedAt, &i.CreatedAt, &i.UpdatedAt); err != nil {
		return Inlet{}, err
	}
	i.HasKey = i.keyHash != ""
	i.Tasks = []Task{}
	return i, nil
}

func (s *Store) GetInlet(ctx context.Context, id int64) (Inlet, error) {
	in, err := scanInlet(s.db.QueryRowContext(ctx, inletSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Inlet{}, fmt.Errorf("inlet %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Inlet{}, fmt.Errorf("get inlet %d: %w", id, err)
	}
	// Tasks are read after the row is closed: holding a result set open while
	// issuing another query would want a second connection, and there is one.
	in.Tasks, err = s.ListTasks(ctx, in.ID)
	if err != nil {
		return Inlet{}, err
	}
	return in, nil
}

// ByAddress is the delivery lookup. It carries the key hash home in the
// returned value so authenticating a request is one query, not two.
func (s *Store) ByAddress(ctx context.Context, address string) (Inlet, error) {
	in, err := scanInlet(s.db.QueryRowContext(ctx, inletSelect+` WHERE address = ?`, strings.ToLower(address)))
	if errors.Is(err, sql.ErrNoRows) {
		return Inlet{}, fmt.Errorf("inlet %q: %w", address, ErrNotFound)
	}
	if err != nil {
		return Inlet{}, fmt.Errorf("get inlet %q: %w", address, err)
	}
	return in, nil
}

func (s *Store) ListInlets(ctx context.Context, wsID int64) ([]Inlet, error) {
	rows, err := s.db.QueryContext(ctx, inletSelect+` WHERE workspace_id = ? ORDER BY address`, wsID)
	if err != nil {
		return nil, fmt.Errorf("list inlets: %w", err)
	}
	out := []Inlet{}
	for rows.Next() {
		in, err := scanInlet(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan inlet: %w", err)
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range out {
		tasks, err := s.ListTasks(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Tasks = tasks
	}
	return out, nil
}

func (s *Store) DeleteInlet(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM inlets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete inlet %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("inlet %d: %w", id, ErrNotFound)
	}
	slog.Info("inlet deleted (its tasks cascade; its ledger rows do not)", "id", id)
	return nil
}

// WorkspaceOfInlet resolves a door back to its workspace so the HTTP layer can
// run the same access check it runs on everything else in that workspace.
func (s *Store) WorkspaceOfInlet(ctx context.Context, id int64) (int64, error) {
	var wsID int64
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM inlets WHERE id = ?`, id).Scan(&wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("inlet %d: %w", id, ErrNotFound)
	}
	return wsID, err
}

func (s *Store) WorkspaceOfTask(ctx context.Context, taskID int64) (int64, error) {
	var wsID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT i.workspace_id FROM inlet_tasks t JOIN inlets i ON i.id = t.inlet_id WHERE t.id = ?`,
		taskID).Scan(&wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("inlet task %d: %w", taskID, ErrNotFound)
	}
	return wsID, err
}

// IssueKey mints this inlet's key and returns it once. Issuing again replaces
// the old one, which is how a key is rotated: the previous string stops working
// the moment this returns, so a leaked key is closed by issuing a new one
// rather than by deleting the door and rebuilding its tasks.
func (s *Store) IssueKey(ctx context.Context, id int64) (string, error) {
	in, err := s.GetInlet(ctx, id)
	if err != nil {
		return "", err
	}
	key, err := NewKey(in.Address)
	if err != nil {
		return "", err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE inlets SET key_hash = ?, key_issued_at = ?, key_last_used_at = NULL, updated_at = ? WHERE id = ?`,
		hashKey(key), now(), now(), id)
	if err != nil {
		return "", fmt.Errorf("issue key for inlet %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("inlet %d: %w", id, ErrNotFound)
	}
	slog.Info("inlet key issued", "inlet_id", id, "address", in.Address, "replaced_existing", in.HasKey)
	return key, nil
}

// NoteKeyUse records that the key opened the door. Best effort by design: the
// delivery has already been authenticated by the time this runs, and losing a
// timestamp is not a reason to refuse a job.
func (s *Store) NoteKeyUse(ctx context.Context, id int64) {
	if _, err := s.db.ExecContext(ctx, `UPDATE inlets SET key_last_used_at = ? WHERE id = ?`, now(), id); err != nil {
		slog.Warn("could not record inlet key use", "inlet_id", id, "err", err)
	}
}

// AddTask puts a job behind a door. Everything that can be checked now is
// checked now — the name, the schema, the content type — because the
// alternative is a caller discovering it at delivery time, when the operator
// who wrote it is not the one reading the error.
func (s *Store) AddTask(ctx context.Context, inletID int64, t Task) (Task, error) {
	t.Name = strings.TrimSpace(strings.ToLower(t.Name))
	if !nameRe.MatchString(t.Name) {
		return Task{}, fmt.Errorf("task name %q is invalid: use lowercase letters, digits, underscores and hyphens (2-49 characters) starting with a letter — it is a path segment in POST /i/{address}/{task}", t.Name)
	}
	t.AgentName = strings.TrimSpace(t.AgentName)
	if t.AgentName == "" {
		return Task{}, errors.New("a task needs a target agent: give the name of an agent in this workspace")
	}
	if strings.TrimSpace(t.Instruction) == "" {
		return Task{}, errors.New("a task needs an instruction: it is the sentence the agent is given along with the payload")
	}

	switch t.Accepts {
	case AcceptsJSON:
		if strings.TrimSpace(t.Schema) == "" {
			t.Schema = "{}"
		}
		if err := CheckSchema(t.Schema); err != nil {
			return Task{}, err
		}
		t.ContentType = ""
	case AcceptsFile:
		if err := CheckContentType(t.ContentType); err != nil {
			return Task{}, err
		}
		t.Schema = "{}"
	default:
		return Task{}, fmt.Errorf("a task accepts %q or %q (got %q)", AcceptsJSON, AcceptsFile, t.Accepts)
	}

	// check normalises as well as refuses, so what is stored is what will be
	// compared: a gear name with a stray space would otherwise match nothing
	// and read, in the ledger, as work that never happened.
	if err := t.Expect.check(); err != nil {
		return Task{}, err
	}
	expect, err := t.Expect.encode()
	if err != nil {
		return Task{}, err
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO inlet_tasks (inlet_id, name, accepts, payload_schema, content_type, agent_name, instruction, expect, callback_url, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inletID, t.Name, t.Accepts, t.Schema, t.ContentType, t.AgentName, t.Instruction, expect,
		strings.TrimSpace(t.CallbackURL), now(), now())
	if err := asConflict(err, fmt.Sprintf("task %q already exists on this inlet", t.Name)); err != nil {
		return Task{}, fmt.Errorf("add inlet task: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("inlet task added", "id", id, "inlet_id", inletID, "task", t.Name, "accepts", t.Accepts,
		"agent", t.AgentName, "expects", t.Expect.Declared())
	return s.GetTask(ctx, id)
}

const taskSelect = `SELECT id, inlet_id, name, accepts, payload_schema, content_type, agent_name, instruction, expect, callback_url, created_at, updated_at
  FROM inlet_tasks`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var expect string
	if err := row.Scan(&t.ID, &t.InletID, &t.Name, &t.Accepts, &t.Schema, &t.ContentType,
		&t.AgentName, &t.Instruction, &expect, &t.CallbackURL, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Task{}, err
	}
	// A block that will not decode fails the read rather than resolving to "no
	// expectations". A task whose requirements quietly vanished would go on
	// answering 200 for runs that did nothing, which is the failure this whole
	// feature exists to end.
	var err error
	if t.Expect, err = decodeExpect(expect); err != nil {
		return Task{}, fmt.Errorf("inlet task %q (%d): %w", t.Name, t.ID, err)
	}
	return t, nil
}

func (s *Store) GetTask(ctx context.Context, id int64) (Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("inlet task %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("get inlet task %d: %w", id, err)
	}
	return t, nil
}

// TaskByName is how a delivery selects its job. An unknown name is ErrNotFound,
// which the delivery route answers as 404 — the same answer as an unknown
// address, so a caller probing for task names learns nothing an operator did
// not already tell them.
func (s *Store) TaskByName(ctx context.Context, inletID int64, name string) (Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, taskSelect+` WHERE inlet_id = ? AND name = ?`, inletID, strings.ToLower(name)))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("inlet task %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("get inlet task %q: %w", name, err)
	}
	return t, nil
}

func (s *Store) ListTasks(ctx context.Context, inletID int64) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, taskSelect+` WHERE inlet_id = ? ORDER BY name`, inletID)
	if err != nil {
		return nil, fmt.Errorf("list inlet tasks: %w", err)
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan inlet task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM inlet_tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete inlet task %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("inlet task %d: %w", id, ErrNotFound)
	}
	slog.Info("inlet task deleted", "id", id)
	return nil
}
