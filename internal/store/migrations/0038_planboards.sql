-- A planboard is the order of work, written down before the work starts.
--
-- It exists because a cyclical workflow had nowhere to keep its plan. An
-- instruction says how to behave and a gear says what can be called, but
-- neither says what comes first, and so the sequence lived in whatever the
-- model decided this time. That is fine for a conversation and wrong for a
-- workflow that fires every night: the same input has to move through the
-- same steps, or the run is not repeatable and there is nothing to version.
--
-- The steps are stored here rather than in Contextverse, and that is the one
-- place this differs from the instruction library. An instruction is prose
-- whose history belongs with context; a planboard is a sequence the ENGINE
-- reads and enforces, with a position pointing into it. Storing the order in
-- a document would mean parsing prose to find out which step is step three.
CREATE TABLE planboards (
    id                  INTEGER PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    description         TEXT NOT NULL DEFAULT '',
    tags                TEXT NOT NULL DEFAULT '[]',  -- JSON array of strings
    -- 'resume' carries the position between runs, so a cron that fires nightly
    -- continues where last night stopped. 'restart' begins at step one every
    -- run, for a plan that is a procedure rather than a journey.
    --
    -- Both exist because both are ordinary: "work through the backlog" wants
    -- resume, "check these six things" wants restart, and guessing which one
    -- somebody meant would be wrong half the time.
    mode                TEXT NOT NULL DEFAULT 'resume' CHECK (mode IN ('resume', 'restart')),
    origin_workspace_id INTEGER REFERENCES workspaces (id) ON DELETE SET NULL,
    created_by_agent_id INTEGER REFERENCES agents (id) ON DELETE SET NULL,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

-- Steps are ordered by `ordinal`, which is contiguous and 1-based. Rewriting
-- the steps replaces the whole set: an edit that leaves gaps in the order
-- would leave a position pointing at a step that no longer exists.
CREATE TABLE planboard_steps (
    id           INTEGER PRIMARY KEY,
    planboard_id INTEGER NOT NULL REFERENCES planboards (id) ON DELETE CASCADE,
    ordinal      INTEGER NOT NULL,
    title        TEXT NOT NULL,
    body         TEXT NOT NULL DEFAULT '',
    UNIQUE (planboard_id, ordinal)
);

-- agent_id NULL = every agent in that workspace, exactly as a gear binds.
--
-- The two shapes mean different things and both are wanted. Bound to one
-- agent, the plan is that agent's own running order. Bound to the workspace,
-- every agent in it advances ONE position — which is how a workflow, rather
-- than an agent, gets a plan: whoever runs next picks up the step the last
-- one left.
CREATE TABLE planboard_bindings (
    id           INTEGER PRIMARY KEY,
    planboard_id INTEGER NOT NULL REFERENCES planboards (id) ON DELETE CASCADE,
    workspace_id INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent_id     INTEGER REFERENCES agents (id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL
);

-- One binding may be made once. Two rows would mean two positions in the same
-- plan for the same worker, and the engine would have no way to choose.
CREATE UNIQUE INDEX planboard_bindings_agent
    ON planboard_bindings (planboard_id, workspace_id, agent_id)
    WHERE agent_id IS NOT NULL;
CREATE UNIQUE INDEX planboard_bindings_workspace
    ON planboard_bindings (planboard_id, workspace_id)
    WHERE agent_id IS NULL;

-- Where the work stands. One row per binding, created on first use.
--
-- `cycle` counts completed passes rather than runs. A nightly workflow that
-- takes three nights to walk seven steps is on cycle 1 for three nights, and
-- that is the number a person means by "how many times has this gone round".
CREATE TABLE planboard_state (
    binding_id   INTEGER PRIMARY KEY REFERENCES planboard_bindings (id) ON DELETE CASCADE,
    step         INTEGER NOT NULL DEFAULT 1,
    cycle        INTEGER NOT NULL DEFAULT 0,
    -- Set when a run reported it could not finish the step, cleared when one
    -- does. It is kept so the next run is told what stopped the last one
    -- instead of walking into it again blind.
    blocked_note TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL
);
