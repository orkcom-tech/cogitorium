-- Gears are agent-forged tools that persist in the environment. The
-- catalog is global (names are unique across the install); provenance
-- records which workspace and agent forged each one.
CREATE TABLE gears (
    id                  INTEGER PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    description         TEXT NOT NULL DEFAULT '',
    tags                TEXT NOT NULL DEFAULT '[]',  -- JSON array of strings
    origin_workspace_id INTEGER REFERENCES workspaces (id) ON DELETE SET NULL,
    created_by_agent_id INTEGER REFERENCES agents (id) ON DELETE SET NULL,
    version             INTEGER NOT NULL DEFAULT 1,
    runtime             TEXT NOT NULL CHECK (runtime IN ('python', 'node', 'bash')),
    entrypoint          TEXT NOT NULL,
    args_schema         TEXT NOT NULL DEFAULT '{}',  -- JSON Schema for the arguments
    -- Agent-authored code runs with the operator's privileges, so nothing
    -- executes until the operator approves this exact version.
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'disabled')),
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

-- Source is versioned with the gear so an approval covers exact content.
CREATE TABLE gear_files (
    id      INTEGER PRIMARY KEY,
    gear_id INTEGER NOT NULL REFERENCES gears (id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    path    TEXT NOT NULL,
    content TEXT NOT NULL,
    UNIQUE (gear_id, version, path)
);

-- agent_id NULL = every agent in that workspace.
CREATE TABLE gear_bindings (
    id           INTEGER PRIMARY KEY,
    gear_id      INTEGER NOT NULL REFERENCES gears (id) ON DELETE CASCADE,
    workspace_id INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent_id     INTEGER REFERENCES agents (id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_gear_bindings_unique
    ON gear_bindings (gear_id, workspace_id, COALESCE(agent_id, 0));
