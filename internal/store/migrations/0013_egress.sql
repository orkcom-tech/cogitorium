-- The internet gate: who may ask to go out, and what was actually asked.
--
-- Two keys, both human. agent_egress is the standing permission an operator
-- draws on the blueprint; it grants only the right to ASK. Every individual
-- search still stops the turn and waits for a person.

CREATE TABLE agent_egress (
    workspace_id INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent_id     INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    -- sha256 over role, model and the agent's bound context paths, taken when
    -- the human granted. Re-checked on every call, so the grant is to an agent
    -- an operator REVIEWED rather than to an integer wearing a name: an
    -- orchestrator that rewrites the granted agent's role or binds a secrets
    -- document into it invalidates the grant instead of inheriting it.
    fingerprint  TEXT    NOT NULL,
    granted_by   INTEGER REFERENCES users (id) ON DELETE SET NULL,
    -- How the granter proved who they were: 'bearer' or 'loopback-implicit'.
    -- A row saying admin + loopback-implicit is not evidence that a human did
    -- it, and the log must not let anyone read it as such.
    granted_auth TEXT    NOT NULL,
    created_at   TEXT    NOT NULL,
    id           INTEGER PRIMARY KEY,
    UNIQUE (workspace_id, agent_id)
);

CREATE TABLE search_queries (
    id            INTEGER PRIMARY KEY,
    workspace_id  INTEGER NOT NULL,
    -- Deliberately no foreign key: an audit row must outlive the agent it
    -- audits, and SQLite recycles rowids, so agent_name is what the log shows.
    agent_id      INTEGER,
    agent_name    TEXT    NOT NULL,
    chain         TEXT    NOT NULL DEFAULT '[]',
    request_token TEXT    NOT NULL UNIQUE,
    query         TEXT    NOT NULL,
    blob_flag     INTEGER NOT NULL DEFAULT 0,
    overlap       TEXT    NOT NULL DEFAULT '[]',
    -- There is no 'allowed' state on purpose. "A human said yes" is recorded
    -- in decided_by/decided_auth/decided_at; "bytes left this machine" is
    -- 'sent' or 'failed'. An audit that collapses the two cannot answer the
    -- question it exists for. Refused attempts get rows too — an attempt the
    -- gate blocked is exactly what an operator wants to see.
    state         TEXT    NOT NULL CHECK (state IN (
                      'awaiting', 'denied', 'timeout', 'abandoned', 'interrupted',
                      'sent', 'failed',
                      'refused_invalid', 'refused_budget', 'refused_busy',
                      'refused_revoked', 'refused_latched')),
    decided_by    INTEGER REFERENCES users (id) ON DELETE SET NULL,
    decided_name  TEXT,
    decided_auth  TEXT,
    decided_at    TEXT,
    http_status   INTEGER,
    result_bytes  INTEGER,
    error         TEXT,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

CREATE INDEX idx_search_queries_ws ON search_queries (workspace_id, id DESC);
CREATE INDEX idx_search_queries_agent ON search_queries (agent_id, created_at DESC);
