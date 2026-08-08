-- Agent-authored code runs on the operator's machine, so every execution
-- is recorded: what ran, on whose behalf, with what arguments, and what
-- came back. Invisible execution is the thing the approval gate exists to
-- prevent.
CREATE TABLE gear_runs (
    id           INTEGER PRIMARY KEY,
    gear_id      INTEGER NOT NULL REFERENCES gears (id) ON DELETE CASCADE,
    version      INTEGER NOT NULL,
    -- NULL agent = the operator (a dry run from the catalog).
    agent_id     INTEGER REFERENCES agents (id) ON DELETE SET NULL,
    workspace_id INTEGER REFERENCES workspaces (id) ON DELETE SET NULL,
    args         TEXT NOT NULL DEFAULT '{}',
    exit_code    INTEGER NOT NULL,
    timed_out    INTEGER NOT NULL DEFAULT 0,
    duration_ms  INTEGER NOT NULL,
    stdout       TEXT NOT NULL DEFAULT '',
    stderr       TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);

CREATE INDEX idx_gear_runs_gear ON gear_runs (gear_id, id DESC);

-- A fixed 60s cap is arbitrary: some gears legitimately take longer, and a
-- gear that always times out is useless.
ALTER TABLE gears ADD COLUMN timeout_seconds INTEGER NOT NULL DEFAULT 60;
