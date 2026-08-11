-- The run ledger records what a delivery DID, not only what its agent said.
--
-- Until now a row's whole account of a job was `result` — the agent's prose.
-- Prose is not evidence: a 3b model told to run a gear made no tool calls at
-- all and wrote a sentence saying it had, and the row said completed with that
-- sentence in it. Nothing distinguished it from the row of a job that happened.
--
-- Two changes, and both want the table rebuilt: SQLite cannot widen a CHECK in
-- place. The rebuild is safe here in a way it is not for most tables in this
-- schema — inlet_runs deliberately carries no foreign keys and nothing
-- references it (0016 says why) — so the drop and rename below cannot cascade
-- into anything. That matters because the migration runner wraps every file in
-- a transaction, and inside one PRAGMA foreign_keys = OFF is a no-op: a table
-- with references either way could not be rebuilt this way at all.

CREATE TABLE inlet_runs_rebuilt (
    id            INTEGER PRIMARY KEY,
    workspace_id  INTEGER NOT NULL,
    inlet_id      INTEGER,
    inlet_address TEXT    NOT NULL,
    task_name     TEXT    NOT NULL,
    agent_id      INTEGER,
    agent_name    TEXT    NOT NULL,
    payload_bytes INTEGER NOT NULL DEFAULT 0,
    payload_path  TEXT    NOT NULL DEFAULT '',
    -- Two more terminal states, and they are separate on purpose: "the model
    -- produced garbage" and "the work did not happen" want different reactions
    -- from whoever is paged. refused_expectation means the record did not meet
    -- what the task declared success to be — the gear did not run, the files
    -- did not appear — and refused_output_schema means the answer did not fit
    -- the shape the task declared. Collapsing them into 'failed' would send
    -- somebody to read a stack trace for a job that simply never started.
    state         TEXT    NOT NULL CHECK (state IN (
                      'accepted', 'refused_schema', 'running',
                      'completed', 'failed', 'interrupted',
                      'refused_expectation', 'refused_output_schema')),
    result        TEXT    NOT NULL DEFAULT '',
    error         TEXT    NOT NULL DEFAULT '',
    -- did is the record of what the run actually did, as JSON: which tools ran
    -- and how each ended, which files appeared with their paths and sizes, how
    -- many model calls it took and what they cost. Written by the engine's own
    -- bookkeeping, never by reading what an agent wrote.
    --
    -- Empty means NO RECORD WAS KEPT, which is not the same as a record showing
    -- nothing happened. It is what a row still in flight holds, and what every
    -- row copied over below holds, because those ran before anything was
    -- collected and claiming they did nothing would be a fresh lie in place of
    -- the old one. A record that says nothing happened is an object with empty
    -- lists in it, and that is a fact about the run.
    did           TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

INSERT INTO inlet_runs_rebuilt (id, workspace_id, inlet_id, inlet_address, task_name,
                                agent_id, agent_name, payload_bytes, payload_path,
                                state, result, error, created_at, updated_at)
SELECT id, workspace_id, inlet_id, inlet_address, task_name,
       agent_id, agent_name, payload_bytes, payload_path,
       state, result, error, created_at, updated_at
  FROM inlet_runs;

DROP TABLE inlet_runs;
ALTER TABLE inlet_runs_rebuilt RENAME TO inlet_runs;

CREATE INDEX idx_inlet_runs_ws ON inlet_runs (workspace_id, id DESC);
CREATE INDEX idx_inlet_runs_inlet ON inlet_runs (inlet_id, id DESC);
