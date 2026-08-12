-- A durable queue, so that a workspace already working DEFERS the next job
-- instead of destroying it.
--
-- Until now a delivery that met a busy workspace was settled `failed` with the
-- engine's busy error and answered 429. A burst of two hundred tickets was one
-- job done and a hundred and ninety-nine ledger rows in the same terminal state
-- a genuinely broken job gets — telling them apart meant string-matching an
-- error message. That is data loss dressed as backpressure.
--
-- One table rather than three. The lane rule, the queue and (later) the
-- scheduler are the same mechanism seen from three angles, and building them
-- separately means three protocols that have to agree with each other forever.
CREATE TABLE work (
    id           INTEGER PRIMARY KEY,
    -- What this unit is. 'callback' has no producer yet and costs nothing here;
    -- adding it to a CHECK later means rebuilding a table that by then has rows
    -- in it, which is the whole reason this file also rebuilds inlet_runs.
    kind         TEXT    NOT NULL CHECK (kind IN ('delivery', 'chat', 'callback')),
    workspace_id INTEGER NOT NULL,

    -- The serialisation domain, as a string: 'ws:42'. A string rather than the
    -- workspace id because the rule it expresses is "one at a time within this
    -- lane", and the day a lane means something narrower — one agent, one
    -- inlet — widening a TEXT column is free while retrofitting the CONCEPT is
    -- not: every consumer would by then have hardcoded workspace_id.
    lane         TEXT    NOT NULL,

    -- Everything the runner needs, as JSON. Deliberately not columns: the shape
    -- differs per kind and a table with a column for every kind's every field
    -- is a table that has to be migrated whenever a kind learns anything.
    args         TEXT    NOT NULL DEFAULT '{}',

    -- The caller's Idempotency-Key, stored as '<inletID>:<task>:<key>'. NULL
    -- when the caller sent none, because a partial unique index treats NULLs as
    -- distinct and that is exactly right here: unkeyed deliveries are all
    -- different from each other.
    idem_key     TEXT,

    state        TEXT    NOT NULL CHECK (state IN ('queued', 'claimed', 'done', 'dead')),

    -- The ledger row this unit exists to fill in, when it has one. A real
    -- nullable indexed column rather than a field inside args: the synchronous
    -- wait, a re-drive and the correlation this schema has never had all need
    -- to go from a run to its work row and back, and a JSON field cannot be
    -- indexed or joined.
    run_id       INTEGER,

    -- Not before this instant. The scheduler will set it; today everything is
    -- enqueued to run now, and the column exists so that adding a clock later
    -- is a writer rather than a migration.
    run_after    TEXT    NOT NULL,
    -- When this unit must be abandoned even if it is still running.
    deadline     TEXT    NOT NULL DEFAULT '',

    attempts     INTEGER NOT NULL DEFAULT 0,
    -- ONE by default, and this is a decision rather than a placeholder. A
    -- reclaimed delivery can re-run an agent that already spent tokens, wrote
    -- four files and sent something outward — at-least-once, not exactly-once.
    -- Shipping 3 and lowering it later means somebody's agent has already sent
    -- three emails. Retry is opt-in, per task, and this queue must never be
    -- described as retrying for you.
    max_attempts INTEGER NOT NULL DEFAULT 1,
    last_error   TEXT    NOT NULL DEFAULT '',

    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

-- THIS INDEX IS THE GUARANTEE.
--
-- One claimed unit per lane, enforced by the database. The obvious alternative
-- — claiming with a `WHERE NOT EXISTS (SELECT … WHERE lane = ? AND state =
-- 'claimed')` subquery — is correct under SQLite's single writer and silently
-- wrong the moment there is a second one: under READ COMMITTED two workers
-- claiming two different rows of the same lane both see an empty subquery and
-- both succeed. The subquery is kept in the claim as a SELECTION heuristic, to
-- avoid picking a row that will certainly fail; it is not what makes the rule
-- true.
CREATE UNIQUE INDEX idx_work_lane_claimed ON work (lane) WHERE state = 'claimed';

-- A key is unique within its workspace and kind, and only when there is one.
CREATE UNIQUE INDEX idx_work_idem ON work (kind, workspace_id, idem_key)
    WHERE idem_key IS NOT NULL;

-- What the claim loop reads: the oldest runnable unit.
CREATE INDEX idx_work_runnable ON work (run_after, id) WHERE state = 'queued';
CREATE INDEX idx_work_run ON work (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX idx_work_ws ON work (workspace_id, id DESC);

-- Timestamps everywhere in this schema are RFC3339 in UTC, stored as TEXT, and
-- here that is load-bearing rather than conventional: the claim orders by
-- run_after and the comparison is lexicographic. A local-time or non-padded
-- format would sort wrongly and the queue would run units in the wrong order
-- while looking healthy.

-- And the ledger learns the state it never had.
--
-- SQLite cannot widen a CHECK in place, so this is a rebuild — the same one
-- 0018 did, for the same reason, and safe for the same reason it was then:
-- inlet_runs deliberately carries no foreign keys and nothing references it.
--
-- Both new states go in now even though only one has a writer today. The other,
-- refused_budget, belongs to the spend ceiling that comes later; adding it here
-- costs nothing and saves a second copy-drop-rename on a bigger table.
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
    -- 'queued' is the state that ends the conflation this whole file exists for:
    -- waiting is not failing, and a caller must be able to tell them apart
    -- without reading an error string.
    state         TEXT    NOT NULL CHECK (state IN (
                      'queued', 'accepted', 'refused_schema', 'running',
                      'completed', 'failed', 'interrupted',
                      'refused_expectation', 'refused_output_schema',
                      'refused_budget')),
    result        TEXT    NOT NULL DEFAULT '',
    error         TEXT    NOT NULL DEFAULT '',
    did           TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

INSERT INTO inlet_runs_rebuilt (id, workspace_id, inlet_id, inlet_address, task_name,
                                agent_id, agent_name, payload_bytes, payload_path,
                                state, result, error, did, created_at, updated_at)
SELECT id, workspace_id, inlet_id, inlet_address, task_name,
       agent_id, agent_name, payload_bytes, payload_path,
       state, result, error, did, created_at, updated_at
  FROM inlet_runs;

DROP TABLE inlet_runs;
ALTER TABLE inlet_runs_rebuilt RENAME TO inlet_runs;

CREATE INDEX idx_inlet_runs_ws ON inlet_runs (workspace_id, id DESC);
CREATE INDEX idx_inlet_runs_inlet ON inlet_runs (inlet_id, id DESC);
