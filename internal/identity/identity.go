// Package identity owns users, teams and API tokens.
//
// There is no solo mode here. A single-operator install is this same model
// with one admin user and no teams, which is why nothing in the codebase
// branches on "am I solo": the admin holds every capability, so every check
// passes. Growing into a team install is additive — create users, group
// them, grant — never a migration.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
)

var (
	ErrNotFound     = catalog.ErrNotFound
	ErrConflict     = catalog.ErrConflict
	ErrUnauthorized = errors.New("unauthenticated")
	ErrForbidden    = errors.New("not permitted")
)

const (
	RoleAdmin    = "admin"
	RoleTeamLead = "team-lead"
	RoleMember   = "member"

	// AdminName is the user seeded on first start; in a solo install it is
	// the only user there will ever be.
	AdminName = "admin"

	tokenPrefix = "cg"
)

type User struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Role  string  `json:"role"`
	Teams []int64 `json:"teams"`
}

func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

// InTeam reports whether the user belongs to a team, which is how a
// workspace granted to that team becomes visible to them.
func (u User) InTeam(teamID int64) bool {
	for _, id := range u.Teams {
		if id == teamID {
			return true
		}
	}
	return false
}

type Team struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Members []User `json:"members,omitempty"`
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

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newToken returns an opaque bearer token. Only its hash is ever stored, so
// this value is the one chance anyone has to copy it.
func newToken(userName string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return fmt.Sprintf("%s-%s-%s", tokenPrefix, userName, hex.EncodeToString(raw)), nil
}

// Bootstrap seeds the admin on first start and adopts any pre-existing
// workspaces, so an install that predates users does not lose them. It
// returns the admin's token exactly once — on the run that created it.
func (s *Store) Bootstrap(ctx context.Context) (admin User, token string, err error) {
	existing, err := s.GetUserByName(ctx, AdminName)
	if err == nil {
		return existing, "", nil
	}
	if !errors.Is(err, ErrNotFound) {
		return User{}, "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", fmt.Errorf("bootstrap: begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (name, role, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		AdminName, RoleAdmin, now(), now())
	if err != nil {
		return User{}, "", fmt.Errorf("bootstrap: create admin: %w", err)
	}
	id, _ := res.LastInsertId()

	token, err = newToken(AdminName)
	if err != nil {
		return User{}, "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tokens (user_id, name, token_hash, created_at) VALUES (?, 'bootstrap', ?, ?)`,
		id, hashToken(token), now()); err != nil {
		return User{}, "", fmt.Errorf("bootstrap: store token: %w", err)
	}

	// Workspaces created before there were users belong to the admin.
	adopted, err := tx.ExecContext(ctx, `UPDATE workspaces SET owner_id = ? WHERE owner_id IS NULL`, id)
	if err != nil {
		return User{}, "", fmt.Errorf("bootstrap: adopt workspaces: %w", err)
	}
	n, _ := adopted.RowsAffected()

	if err := tx.Commit(); err != nil {
		return User{}, "", fmt.Errorf("bootstrap: commit: %w", err)
	}
	slog.Info("admin user seeded", "id", id, "adopted_workspaces", n)
	return User{ID: id, Name: AdminName, Role: RoleAdmin}, token, nil
}

func (s *Store) loadTeams(ctx context.Context, u *User) error {
	rows, err := s.db.QueryContext(ctx, `SELECT team_id FROM team_members WHERE user_id = ?`, u.ID)
	if err != nil {
		return fmt.Errorf("load teams for user %d: %w", u.ID, err)
	}
	defer rows.Close()
	u.Teams = []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan team membership: %w", err)
		}
		u.Teams = append(u.Teams, id)
	}
	return rows.Err()
}

func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT id, name, role FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Name, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("user %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return User{}, fmt.Errorf("get user %d: %w", id, err)
	}
	return u, s.loadTeams(ctx, &u)
}

func (s *Store) GetUserByName(ctx context.Context, name string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT id, name, role FROM users WHERE name = ?`, name).
		Scan(&u.ID, &u.Name, &u.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("user %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return User{}, fmt.Errorf("get user %q: %w", name, err)
	}
	return u, s.loadTeams(ctx, &u)
}

// Authenticate resolves a bearer token to its user and records the use.
func (s *Store) Authenticate(ctx context.Context, token string) (User, error) {
	var userID int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM tokens WHERE token_hash = ?`, hashToken(token)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, fmt.Errorf("authenticate: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE token_hash = ?`, now(), hashToken(token)); err != nil {
		slog.Warn("could not record token use", "err", err)
	}
	return s.GetUser(ctx, userID)
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, role FROM users ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Role); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.loadTeams(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CreateUser adds a user and returns their first token, shown once.
func (s *Store) CreateUser(ctx context.Context, name, role string) (User, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return User{}, "", errors.New("name is required")
	}
	switch role {
	case RoleAdmin, RoleTeamLead, RoleMember:
	default:
		return User{}, "", fmt.Errorf("role must be admin, team-lead or member (got %q)", role)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", fmt.Errorf("create user: begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (name, role, created_at, updated_at) VALUES (?, ?, ?, ?)`, name, role, now(), now())
	if err := asConflict(err, fmt.Sprintf("user %q", name)); err != nil {
		return User{}, "", fmt.Errorf("create user: %w", err)
	}
	id, _ := res.LastInsertId()

	token, err := newToken(name)
	if err != nil {
		return User{}, "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tokens (user_id, name, token_hash, created_at) VALUES (?, 'initial', ?, ?)`,
		id, hashToken(token), now()); err != nil {
		return User{}, "", fmt.Errorf("create user: store token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return User{}, "", fmt.Errorf("create user: commit: %w", err)
	}
	slog.Info("user created", "id", id, "name", name, "role", role)
	return User{ID: id, Name: name, Role: role, Teams: []int64{}}, token, nil
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.Name == AdminName {
		return errors.New("the bootstrap admin cannot be deleted")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete user %d: %w", id, err)
	}
	slog.Info("user deleted", "id", id, "name", u.Name)
	return nil
}

func (s *Store) CreateTeam(ctx context.Context, name string) (Team, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Team{}, errors.New("name is required")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO teams (name, created_at) VALUES (?, ?)`, name, now())
	if err := asConflict(err, fmt.Sprintf("team %q", name)); err != nil {
		return Team{}, fmt.Errorf("create team: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Info("team created", "id", id, "name", name)
	return Team{ID: id, Name: name}, nil
}

func (s *Store) ListTeams(ctx context.Context) ([]Team, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM teams ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTeam(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM teams WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete team %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("team %d: %w", id, ErrNotFound)
	}
	slog.Info("team deleted", "id", id)
	return nil
}

func (s *Store) AddTeamMember(ctx context.Context, teamID, userID int64) error {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO team_members (team_id, user_id, created_at) VALUES (?, ?, ?)`,
		teamID, userID, now())
	if err != nil {
		return fmt.Errorf("add team member: %w", err)
	}
	slog.Info("team member added", "team_id", teamID, "user_id", userID)
	return nil
}

func (s *Store) RemoveTeamMember(ctx context.Context, teamID, userID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID)
	if err != nil {
		return fmt.Errorf("remove team member: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("membership: %w", ErrNotFound)
	}
	slog.Info("team member removed", "team_id", teamID, "user_id", userID)
	return nil
}
