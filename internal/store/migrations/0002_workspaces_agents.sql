CREATE TABLE workspaces (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

-- kind is extensible on purpose (requirement 11): 'model' binds a catalog
-- model; future kinds (e.g. a remote agent endpoint) extend the CHECK.
CREATE TABLE agents (
    id              INTEGER PRIMARY KEY,
    workspace_id    INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    kind            TEXT NOT NULL DEFAULT 'model' CHECK (kind IN ('model')),
    role            TEXT NOT NULL DEFAULT '',
    model_id        INTEGER REFERENCES models (id) ON DELETE SET NULL,
    is_orchestrator INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE (workspace_id, name)
);

CREATE TABLE wires (
    id            INTEGER PRIMARY KEY,
    workspace_id  INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    from_agent_id INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    to_agent_id   INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    label         TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    UNIQUE (workspace_id, from_agent_id, to_agent_id)
);

-- The workspace timeline: operator input, agent output, tool activity,
-- delegations. agent_id NULL = the operator. meta is JSON (tool call ids,
-- inputs, delegation targets) so turns can be reconstructed exactly.
CREATE TABLE ws_messages (
    id           INTEGER PRIMARY KEY,
    workspace_id INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent_id     INTEGER REFERENCES agents (id) ON DELETE SET NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('user', 'assistant', 'tool_call', 'tool_result', 'delegation', 'error')),
    content      TEXT NOT NULL DEFAULT '',
    meta         TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL
);

CREATE INDEX idx_ws_messages_ws ON ws_messages (workspace_id, id);
CREATE INDEX idx_ws_messages_agent ON ws_messages (agent_id, id);
