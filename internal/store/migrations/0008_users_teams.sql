-- One model, not two modes: a solo install is this same schema with a
-- single admin user and no teams. Nothing branches on "am I solo".
CREATE TABLE users (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    role       TEXT NOT NULL CHECK (role IN ('admin', 'team-lead', 'member')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE teams (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);

CREATE TABLE team_members (
    team_id    INTEGER NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (team_id, user_id)
);

-- Only the hash is stored; the token itself is shown once at creation.
CREATE TABLE tokens (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name        TEXT NOT NULL DEFAULT '',
    token_hash  TEXT NOT NULL UNIQUE,
    created_at  TEXT NOT NULL,
    last_used_at TEXT
);

-- Workspaces gain an owner and an optional team grant. Existing rows have
-- neither; they are adopted by the seeded admin on first start.
ALTER TABLE workspaces ADD COLUMN owner_id INTEGER REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE workspaces ADD COLUMN team_id INTEGER REFERENCES teams (id) ON DELETE SET NULL;

CREATE INDEX idx_workspaces_owner ON workspaces (owner_id);
CREATE INDEX idx_workspaces_team ON workspaces (team_id);
