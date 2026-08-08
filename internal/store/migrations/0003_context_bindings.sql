-- Context bindings reference files in the Contextverse space by path;
-- content and versions live in Contextverse (contextd), never here.
-- agent_id NULL = workspace-wide (all agents see it).
CREATE TABLE context_bindings (
    id           INTEGER PRIMARY KEY,
    workspace_id INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    path         TEXT NOT NULL,
    agent_id     INTEGER REFERENCES agents (id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL
);

-- NULLs are distinct in plain UNIQUE constraints; the expression index
-- makes (workspace, path, scope) actually unique.
CREATE UNIQUE INDEX idx_context_bindings_unique
    ON context_bindings (workspace_id, path, COALESCE(agent_id, 0));
