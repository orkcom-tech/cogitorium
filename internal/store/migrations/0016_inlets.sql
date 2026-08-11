-- Inlets: a door into a workspace from outside.
--
-- An inlet is not an agent. It has no model, no role and no memory — only an
-- address, a key, and a list of tasks. It gets its own table rather than a new
-- value of agents.kind because that column's CHECK is kind IN ('model'), and
-- widening a CHECK in SQLite means rebuilding the table. This migration runner
-- wraps every migration in a transaction, where PRAGMA foreign_keys=OFF is a
-- no-op, so the rebuild would cascade through wires and token_usage and take
-- the blueprint with it.

CREATE TABLE inlets (
    id               INTEGER PRIMARY KEY,
    workspace_id     INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The address is the whole path segment a caller posts to
    -- (POST /i/{address}/{task}), so it is unique across the install rather
    -- than per workspace: that URL carries no workspace id to disambiguate it.
    address          TEXT NOT NULL UNIQUE,
    description      TEXT NOT NULL DEFAULT '',
    -- Only the hash is stored, exactly as with user tokens: the key is shown
    -- once when it is issued and cannot be recovered afterwards. NULL means no
    -- key has been issued yet, and an inlet in that state refuses every
    -- delivery — which is the state an imported inlet has to arrive in, since a
    -- bundle carries an inlet's shape and never its credential.
    key_hash         TEXT UNIQUE,
    key_issued_at    TEXT,
    key_last_used_at TEXT,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE INDEX idx_inlets_ws ON inlets (workspace_id);

CREATE TABLE inlet_tasks (
    id             INTEGER PRIMARY KEY,
    inlet_id       INTEGER NOT NULL REFERENCES inlets (id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    -- What the task accepts: a JSON body checked against payload_schema before
    -- any model is called, or a file of content_type written into the
    -- workspace's own directory.
    accepts        TEXT NOT NULL CHECK (accepts IN ('json', 'file')),
    payload_schema TEXT NOT NULL DEFAULT '{}',
    content_type   TEXT NOT NULL DEFAULT '',
    -- The target is stored by NAME, the handle delegation already uses. An id
    -- would need a foreign key, and both of its behaviours are wrong here:
    -- ON DELETE CASCADE would silently delete the task when an agent is
    -- removed, and SET NULL would leave a task pointing at nothing with no
    -- word for the operator about which agent it wanted.
    agent_name     TEXT NOT NULL,
    -- The sentence the agent is given along with the payload.
    instruction    TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE (inlet_id, name)
);

-- The run ledger. Modelled on search_queries, which solved the same problem:
-- without it nothing can answer "did job 4471 happen".
CREATE TABLE inlet_runs (
    id            INTEGER PRIMARY KEY,
    workspace_id  INTEGER NOT NULL,
    -- Deliberately no foreign key on inlet_id or agent_id: a ledger row must
    -- outlive the door it records and the agent that did the work, and SQLite
    -- recycles rowids, so the stored names are what the log shows.
    inlet_id      INTEGER,
    inlet_address TEXT    NOT NULL,
    task_name     TEXT    NOT NULL,
    agent_id      INTEGER,
    agent_name    TEXT    NOT NULL,
    -- What arrived, and for a file task the workspace-relative path the agent
    -- was handed. The bytes themselves are never copied in here: the point of
    -- a file task is that the agent gets a path.
    payload_bytes INTEGER NOT NULL DEFAULT 0,
    payload_path  TEXT    NOT NULL DEFAULT '',
    -- Every outcome a delivery can have is named here, so a row is never in a
    -- state the reader has to guess at. 'accepted' and 'running' are the two
    -- live states: the row is written 'accepted' BEFORE the payload is looked
    -- at, so a crash between the POST and the answer still leaves evidence,
    -- and startup reconciliation turns whatever is still live into
    -- 'interrupted' — a pause cannot survive the process that held it.
    state         TEXT    NOT NULL CHECK (state IN (
                      'accepted', 'refused_schema', 'running',
                      'completed', 'failed', 'interrupted')),
    result        TEXT    NOT NULL DEFAULT '',
    error         TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

CREATE INDEX idx_inlet_runs_ws ON inlet_runs (workspace_id, id DESC);
CREATE INDEX idx_inlet_runs_inlet ON inlet_runs (inlet_id, id DESC);
