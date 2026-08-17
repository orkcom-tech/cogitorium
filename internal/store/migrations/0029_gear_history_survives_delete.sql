-- Deleting a gear erased the evidence that it ever ran.
--
-- gear_runs.gear_id was ON DELETE CASCADE, so removing a gear took its whole
-- execution history with it — which is exactly backwards. The moment you most
-- need to know what a gear did is after you have decided it should not exist:
-- somebody approved agent-written code, it ran forty times against production,
-- and the first response is to delete it. Under CASCADE that response also
-- destroyed the only record of the forty runs.
--
-- The run now outlives the gear. gear_id goes NULL and the name is kept on the
-- row, so a deleted gear's runs are still readable and still say which gear
-- they were: "deploy (deleted)" rather than a row with a dangling id or no row
-- at all.
--
-- SQLite cannot alter a foreign key, so the table is rebuilt. Every existing
-- row is carried over with its gear's current name filled in.

CREATE TABLE gear_runs_kept (
    id           INTEGER PRIMARY KEY,
    -- NULL means the gear has been deleted. The run still happened.
    gear_id      INTEGER REFERENCES gears (id) ON DELETE SET NULL,
    -- Denormalised on purpose: it is the only thing left once gear_id is NULL,
    -- and a name read through a join cannot survive the row it joins to.
    gear_name    TEXT    NOT NULL DEFAULT '',
    version      INTEGER NOT NULL,
    agent_id     INTEGER REFERENCES agents (id) ON DELETE SET NULL,
    workspace_id INTEGER REFERENCES workspaces (id) ON DELETE SET NULL,
    args         TEXT    NOT NULL DEFAULT '{}',
    exit_code    INTEGER NOT NULL,
    timed_out    INTEGER NOT NULL DEFAULT 0,
    duration_ms  INTEGER NOT NULL,
    stdout       TEXT    NOT NULL DEFAULT '',
    stderr       TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL
);

INSERT INTO gear_runs_kept (id, gear_id, gear_name, version, agent_id, workspace_id,
                            args, exit_code, timed_out, duration_ms, stdout, stderr, created_at)
SELECT r.id, r.gear_id, COALESCE(g.name, ''), r.version, r.agent_id, r.workspace_id,
       r.args, r.exit_code, r.timed_out, r.duration_ms, r.stdout, r.stderr, r.created_at
FROM gear_runs r LEFT JOIN gears g ON g.id = r.gear_id;

DROP TABLE gear_runs;
ALTER TABLE gear_runs_kept RENAME TO gear_runs;

CREATE INDEX idx_gear_runs_gear ON gear_runs (gear_id, id DESC);
-- Runs of a gear that no longer exists are found by name, which is the only
-- handle left on them.
CREATE INDEX idx_gear_runs_name ON gear_runs (gear_name, id DESC);

-- Who approved what, when, and with which grants.
--
-- Approving a gear is the single most consequential act in this product: it is
-- a human saying that code an agent wrote may run on this machine. Until now it
-- left one trace — a status column reading 'approved' — which cannot answer any
-- of the questions asked after something goes wrong. Who said yes. When. To
-- WHICH VERSION, given that a gear can be edited afterwards. And what they
-- granted it at the same moment, since the credentials and the network reach
-- are decided in the same breath and are most of what makes the decision
-- dangerous.
--
-- Append-only by construction: nothing updates or deletes a row here, and the
-- gear's own status column stays as the answer to "what is it now". This is the
-- answer to "how did it get that way".
CREATE TABLE gear_approvals (
    id         INTEGER PRIMARY KEY,
    gear_id    INTEGER REFERENCES gears (id) ON DELETE SET NULL,
    gear_name  TEXT    NOT NULL DEFAULT '',
    -- The version as it stood at the moment of the decision. A gear approved
    -- at v3 and edited to v4 is not an approved gear, and this is what lets
    -- anybody see that.
    version    INTEGER NOT NULL,
    status     TEXT    NOT NULL CHECK (status IN ('approved', 'disabled', 'pending')),
    -- NULL when the actor cannot be attributed: a single-operator install
    -- before accounts, or a user deleted since. The decision still happened.
    user_id    INTEGER REFERENCES users (id) ON DELETE SET NULL,
    user_name  TEXT    NOT NULL DEFAULT '',
    -- The two grants, as they stood after this decision. Stored as text so the
    -- row is readable on its own, without joining to a gear that may be gone.
    env_names  TEXT    NOT NULL DEFAULT '',
    network    TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL
);

CREATE INDEX idx_gear_approvals_gear ON gear_approvals (gear_id, id DESC);
