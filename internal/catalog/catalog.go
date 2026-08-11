// Package catalog owns providers and models: the user-managed registry of
// everything Cogitorium can run inference on.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/llm"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

// asConflict converts SQLite unique-constraint violations into ErrConflict
// so the API can answer 409 with a clean message instead of a raw driver
// error.
func asConflict(err error, what string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%s: %w", what, ErrConflict)
	}
	return err
}

type Provider struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	BaseURL string `json:"base_url"`
	// HasKey tells the UI whether a key is stored; the key itself never
	// leaves the server.
	HasKey bool `json:"has_key"`

	apiKey string
}

type Model struct {
	ID           int64  `json:"id"`
	ProviderID   int64  `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	ProviderType string `json:"provider_type"`
	ModelName    string `json:"model_name"`
	Label        string `json:"label"`
	// Accepts is what this model can be shown besides text, as the operator
	// declared it when adding it: llm.PartImage, llm.PartDocument, or neither.
	// Empty is text-only, and text-only is the default for the reason spelled
	// out in migration 0017 — nothing here is probed or inferred.
	Accepts []string `json:"accepts"`
}

// Takes reports whether this model was declared able to take a part of this
// kind. Text needs no declaration: every model takes text.
func (m Model) Takes(kind string) bool {
	if kind == llm.PartText {
		return true
	}
	for _, a := range m.Accepts {
		if a == kind {
			return true
		}
	}
	return false
}

// display is how a model is named to an operator choosing between several.
// The label is what they typed and recognise; without one, the provider and
// the model name together are unambiguous where the model name alone is not —
// two providers can both serve "llama3".
func (m Model) display() string {
	if l := strings.TrimSpace(m.Label); l != "" {
		return l
	}
	return m.ProviderName + " / " + m.ModelName
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func validateProviderInput(providerType, baseURL string) (string, error) {
	switch providerType {
	case llm.TypeAnthropic, llm.TypeOpenAICompatible:
	default:
		return "", fmt.Errorf("type must be %q or %q", llm.TypeAnthropic, llm.TypeOpenAICompatible)
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = llm.DefaultBaseURL(providerType)
	}
	if baseURL == "" {
		return "", errors.New("base_url is required for openai-compatible providers")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return "", fmt.Errorf("base_url must start with http:// or https://")
	}
	return baseURL, nil
}

func (s *Store) CreateProvider(ctx context.Context, name, providerType, baseURL, apiKey string) (Provider, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Provider{}, errors.New("name is required")
	}
	baseURL, err := validateProviderInput(providerType, baseURL)
	if err != nil {
		return Provider{}, err
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO providers (name, type, base_url, api_key, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		name, providerType, baseURL, apiKey, now(), now())
	if err := asConflict(err, fmt.Sprintf("provider %q", name)); err != nil {
		return Provider{}, fmt.Errorf("create provider: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("provider created", "id", id, "name", name, "type", providerType, "base_url", baseURL, "has_key", apiKey != "")
	return s.GetProvider(ctx, id)
}

func (s *Store) GetProvider(ctx context.Context, id int64) (Provider, error) {
	var p Provider
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, type, base_url, api_key FROM providers WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Type, &p.BaseURL, &p.apiKey)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, fmt.Errorf("provider %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Provider{}, fmt.Errorf("get provider %d: %w", id, err)
	}
	p.HasKey = p.apiKey != ""
	return p, nil
}

func (s *Store) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, type, base_url, api_key FROM providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()

	out := []Provider{}
	for rows.Next() {
		var p Provider
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.BaseURL, &p.apiKey); err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		p.HasKey = p.apiKey != ""
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProvider patches non-nil fields. A nil apiKey keeps the stored key;
// an empty non-nil key clears it.
func (s *Store) UpdateProvider(ctx context.Context, id int64, name, baseURL, apiKey *string) (Provider, error) {
	p, err := s.GetProvider(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if name != nil {
		p.Name = strings.TrimSpace(*name)
		if p.Name == "" {
			return Provider{}, errors.New("name cannot be empty")
		}
	}
	if baseURL != nil {
		validated, err := validateProviderInput(p.Type, *baseURL)
		if err != nil {
			return Provider{}, err
		}
		p.BaseURL = validated
	}
	key := p.apiKey
	if apiKey != nil {
		key = *apiKey
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE providers SET name = ?, base_url = ?, api_key = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.BaseURL, key, now(), id)
	if err := asConflict(err, fmt.Sprintf("provider %q", p.Name)); err != nil {
		return Provider{}, fmt.Errorf("update provider %d: %w", id, err)
	}
	slog.Info("provider updated", "id", id, "name", p.Name, "base_url", p.BaseURL, "key_changed", apiKey != nil)
	return s.GetProvider(ctx, id)
}

func (s *Store) DeleteProvider(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete provider %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("provider %d: %w", id, ErrNotFound)
	}
	slog.Info("provider deleted (models cascade)", "id", id)
	return nil
}

// Client builds an llm.Client for a stored provider.
func (s *Store) Client(ctx context.Context, providerID int64) (llm.Client, Provider, error) {
	p, err := s.GetProvider(ctx, providerID)
	if err != nil {
		return nil, Provider{}, err
	}
	c, err := llm.New(p.Type, p.BaseURL, p.apiKey)
	if err != nil {
		return nil, Provider{}, err
	}
	return c, p, nil
}

// CreateModel adds a model to the catalog. accepts is what the operator says
// this model can be shown besides text — llm.PartImage, llm.PartDocument —
// and giving none means text only, which is the honest default for a model
// nobody has said anything about.
//
// It is variadic rather than a slice parameter so that adding a text-only
// model, which is most of them, still reads as one line and every existing
// caller says exactly what it always said.
func (s *Store) CreateModel(ctx context.Context, providerID int64, modelName, label string, accepts ...string) (Model, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return Model{}, errors.New("model_name is required")
	}
	stored, err := encodeAccepts(accepts)
	if err != nil {
		return Model{}, err
	}
	if _, err := s.GetProvider(ctx, providerID); err != nil {
		return Model{}, err
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO models (provider_id, model_name, label, accepts, created_at) VALUES (?, ?, ?, ?, ?)`,
		providerID, modelName, label, stored, now())
	if err := asConflict(err, fmt.Sprintf("model %q on this provider", modelName)); err != nil {
		return Model{}, fmt.Errorf("create model: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("model added to catalog", "id", id, "provider_id", providerID, "model_name", modelName, "accepts", stored)
	return s.GetModel(ctx, id)
}

// SetModelAccepts changes what an existing model may be shown. It exists
// because the alternative is deleting the model and adding it again, and the
// model's id is what every agent is bound by: correcting a modality would
// silently unbind every agent using it.
func (s *Store) SetModelAccepts(ctx context.Context, id int64, accepts []string) (Model, error) {
	stored, err := encodeAccepts(accepts)
	if err != nil {
		return Model{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE models SET accepts = ? WHERE id = ?`, stored, id)
	if err != nil {
		return Model{}, fmt.Errorf("update model %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Model{}, fmt.Errorf("model %d: %w", id, ErrNotFound)
	}
	slog.Info("model modality changed", "id", id, "accepts", stored)
	return s.GetModel(ctx, id)
}

// CheckAccepts refuses a turn's attachments before any request is built, when
// they include something the operator never said this model could take. The
// error names the model and the models that could take it, because "this
// model does not accept images" tells an operator what went wrong and nothing
// about what to do next.
func (s *Store) CheckAccepts(ctx context.Context, m Model, parts []llm.Part) error {
	for _, p := range parts {
		if m.Takes(p.Kind) {
			continue
		}
		file := "this file"
		if strings.TrimSpace(p.Name) != "" {
			file = strconv.Quote(p.Name)
		}
		return fmt.Errorf("%s cannot be sent to model %s: it was added to the catalog as %s, and the catalog is the only thing that says what a model can be shown. %s",
			file, strconv.Quote(m.display()), describeAccepts(m.Accepts), s.alternatives(ctx, p.Kind))
	}
	return nil
}

// alternatives names the models that were declared able to take this kind, so
// the operator can move the agent instead of guessing. A catalog with none is
// said so plainly rather than left as an empty list.
func (s *Store) alternatives(ctx context.Context, kind string) string {
	models, err := s.ListModels(ctx)
	if err != nil {
		// The refusal is the answer; a second failure while decorating it must
		// not replace it with a database error the operator cannot connect to
		// the file they attached.
		slog.Warn("could not list models to suggest an alternative", "err", err)
		models = nil
	}
	names := []string{}
	for _, m := range models {
		if m.Takes(kind) && kind != llm.PartText {
			names = append(names, strconv.Quote(m.display()))
		}
	}
	if len(names) == 0 {
		return fmt.Sprintf("No model in this catalog accepts %s. Add one and say so when adding it, or give the file's path to a gear instead — it is in the workspace either way.", plural(kind))
	}
	return fmt.Sprintf("Models that accept %s: %s. Bind this agent to one of those, or — if this model does take %s — say so on the model in the catalog.",
		plural(kind), strings.Join(names, ", "), plural(kind))
}

// encodeAccepts normalizes what the operator said a model can be shown into
// the stored form, and refuses anything the adapters cannot actually encode:
// an operator who types "audio" has to be told now, not by a model that
// answers nothing at the moment a file is in front of it.
func encodeAccepts(kinds []string) (string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, k := range kinds {
		k = strings.ToLower(strings.TrimSpace(k))
		switch k {
		case "":
			continue
		case llm.PartText:
			// Not a capability. Every model takes text, so storing it would
			// make the empty value mean two different things.
			continue
		case llm.PartImage, llm.PartDocument:
		default:
			return "", fmt.Errorf("accepts %q: a model can be declared able to take %q (png, jpeg, gif, webp) or %q (PDF) — text needs no declaration, every model takes it",
				k, llm.PartImage, llm.PartDocument)
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ","), nil
}

func decodeAccepts(stored string) []string {
	out := []string{}
	for _, k := range strings.Split(stored, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

func describeAccepts(accepts []string) string {
	if len(accepts) == 0 {
		return "text-only"
	}
	words := make([]string, 0, len(accepts))
	for _, a := range accepts {
		words = append(words, plural(a))
	}
	return "accepting text and " + strings.Join(words, " and ")
}

// plural names a part kind the way an operator would say it in a sentence.
func plural(kind string) string {
	switch kind {
	case llm.PartImage:
		return "images"
	case llm.PartDocument:
		return "PDF documents"
	}
	return kind
}

func (s *Store) GetModel(ctx context.Context, id int64) (Model, error) {
	var (
		m       Model
		accepts string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, m.provider_id, p.name, p.type, m.model_name, m.label, m.accepts
		FROM models m JOIN providers p ON p.id = m.provider_id
		WHERE m.id = ?`, id).
		Scan(&m.ID, &m.ProviderID, &m.ProviderName, &m.ProviderType, &m.ModelName, &m.Label, &accepts)
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, fmt.Errorf("model %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Model{}, fmt.Errorf("get model %d: %w", id, err)
	}
	m.Accepts = decodeAccepts(accepts)
	return m, nil
}

func (s *Store) ListModels(ctx context.Context) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.provider_id, p.name, p.type, m.model_name, m.label, m.accepts
		FROM models m JOIN providers p ON p.id = m.provider_id
		ORDER BY p.name, m.model_name`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()

	out := []Model{}
	for rows.Next() {
		var (
			m       Model
			accepts string
		)
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.ProviderName, &m.ProviderType, &m.ModelName, &m.Label, &accepts); err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}
		m.Accepts = decodeAccepts(accepts)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteModel(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM models WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete model %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("model %d: %w", id, ErrNotFound)
	}
	slog.Info("model removed from catalog", "id", id)
	return nil
}
