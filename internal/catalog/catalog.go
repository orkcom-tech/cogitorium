// Package catalog owns providers and models: the user-managed registry of
// everything Cogitorium can run inference on.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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

func (s *Store) CreateModel(ctx context.Context, providerID int64, modelName, label string) (Model, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return Model{}, errors.New("model_name is required")
	}
	if _, err := s.GetProvider(ctx, providerID); err != nil {
		return Model{}, err
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO models (provider_id, model_name, label, created_at) VALUES (?, ?, ?, ?)`,
		providerID, modelName, label, now())
	if err := asConflict(err, fmt.Sprintf("model %q on this provider", modelName)); err != nil {
		return Model{}, fmt.Errorf("create model: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("model added to catalog", "id", id, "provider_id", providerID, "model_name", modelName)
	return s.GetModel(ctx, id)
}

func (s *Store) GetModel(ctx context.Context, id int64) (Model, error) {
	var m Model
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, m.provider_id, p.name, p.type, m.model_name, m.label
		FROM models m JOIN providers p ON p.id = m.provider_id
		WHERE m.id = ?`, id).
		Scan(&m.ID, &m.ProviderID, &m.ProviderName, &m.ProviderType, &m.ModelName, &m.Label)
	if errors.Is(err, sql.ErrNoRows) {
		return Model{}, fmt.Errorf("model %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Model{}, fmt.Errorf("get model %d: %w", id, err)
	}
	return m, nil
}

func (s *Store) ListModels(ctx context.Context) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.provider_id, p.name, p.type, m.model_name, m.label
		FROM models m JOIN providers p ON p.id = m.provider_id
		ORDER BY p.name, m.model_name`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()

	out := []Model{}
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.ProviderName, &m.ProviderType, &m.ModelName, &m.Label); err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}
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
