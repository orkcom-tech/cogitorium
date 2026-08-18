-- A clock can dial an agent or a gear directly, instead of only an inlet task.
--
-- 0024 pointed a schedule at an inlet task on purpose, and the reasoning was
-- sound as far as it went: the task already says which agent, what to tell it,
-- what it accepts and what success means, so a firing was that same job with
-- nobody on the other end, and two definitions of a job would be two things to
-- keep in step.
--
-- WHAT THAT MISSED is what a task actually IS. A task describes a DOOR somebody
-- else pushes work through — it has an inlet, an address, a key and a caller. A
-- schedule is not that. Bending one through the other meant that to get a
-- nightly job you first invented a receiver nobody would ever call, and the
-- receivers list filled with entries that had no inlet and no caller. That is a
-- worse lie than the one it avoided.
--
-- So a schedule may now carry its own target. The task path is unchanged and is
-- still the right shape when a job genuinely has a door as well as a clock.
--
-- WHY A REBUILD. task_id is `INTEGER NOT NULL REFERENCES inlet_tasks (id) ON
-- DELETE CASCADE` in 0024 and it has to become nullable, which SQLite cannot do
-- in place. The rebuild is safe here for the reason 0018's was not automatic:
-- NOTHING REFERENCES schedules. Its own foreign keys point outward, and those
-- are simply re-declared below, so there is no cascade for the drop to run
-- through — which matters because the migration runner wraps every file in a
-- transaction, and inside one `PRAGMA foreign_keys = OFF` is a no-op.

CREATE TABLE schedules_rebuilt (
    id           INTEGER PRIMARY KEY,
    workspace_id INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,

    -- What this clock dials. 'task' is every row that existed before this
    -- migration, which is why it is the default.
    target_kind TEXT NOT NULL DEFAULT 'task'
        CHECK (target_kind IN ('task', 'agent', 'gear')),

    -- The task path, unchanged in meaning and now nullable. Still CASCADE:
    -- a schedule whose task is gone has no instruction, no agent and no
    -- expectation, so it is not a job with a missing part — there is nothing
    -- left of it.
    task_id INTEGER REFERENCES inlet_tasks (id) ON DELETE CASCADE,

    -- The direct paths. SET NULL RATHER THAN CASCADE, and the difference is the
    -- whole point: deleting an agent must not silently delete the nightly job
    -- that used it. The row survives with a NULL target, the tick refuses it
    -- and says why, and the blueprint draws it BROKEN. "It stopped, and here is
    -- why" is a thing an operator can act on; "it vanished" is not.
    target_agent_id INTEGER REFERENCES agents (id) ON DELETE SET NULL,
    target_gear_id  INTEGER REFERENCES gears (id) ON DELETE SET NULL,

    -- What the target is given. An agent needs a sentence — a clock wired to an
    -- agent with nothing to say produces a turn with an empty prompt — and a
    -- gear needs arguments that fit its schema. One column each rather than one
    -- shared blob, because they are checked against different things: the
    -- instruction against nothing, the arguments against that gear's args
    -- schema, when the schedule is SAVED rather than when it fires.
    instruction TEXT NOT NULL DEFAULT '',
    args        TEXT NOT NULL DEFAULT '{}',

    name TEXT NOT NULL,
    spec TEXT NOT NULL,
    tz   TEXT NOT NULL DEFAULT '',

    -- The body handed to a task, exactly as an HTTP caller would have sent it.
    -- Unused by the direct paths, which carry `instruction` or `args` instead.
    payload TEXT NOT NULL DEFAULT '{}',

    enabled INTEGER NOT NULL DEFAULT 1,
    on_miss TEXT    NOT NULL DEFAULT 'skip' CHECK (on_miss IN ('skip', 'run')),

    next_at       TEXT    NOT NULL,
    last_work_id  INTEGER,
    last_fired_at TEXT    NOT NULL DEFAULT '',
    last_outcome  TEXT    NOT NULL DEFAULT '',
    fires         INTEGER NOT NULL DEFAULT 0,
    skips         INTEGER NOT NULL DEFAULT 0,

    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,

    -- A row may only carry the target its kind claims. This is about which
    -- columns are ALLOWED, not which are filled, and the difference is
    -- deliberate: an agent row whose agent has been deleted has a NULL
    -- target_agent_id and must still be storable, because SET NULL above is
    -- what makes a schedule survive its target and read as broken rather than
    -- vanishing. Requiring it NOT NULL here would turn every agent deletion
    -- into a constraint violation and take the schedule with it.
    --
    -- What this DOES forbid is a row wearing two targets — a 'gear' row with a
    -- leftover task_id, say — which is the shape the tick would read at 03:00
    -- with nobody watching and fire something nobody asked for. That a NEW
    -- agent or gear schedule actually names its target is checked in Go, where
    -- the operator is still on the other end of the error.
    CHECK (
        (target_kind = 'task'  AND task_id IS NOT NULL
                               AND target_agent_id IS NULL AND target_gear_id IS NULL)
     OR (target_kind = 'agent' AND task_id IS NULL AND target_gear_id IS NULL)
     OR (target_kind = 'gear'  AND task_id IS NULL AND target_agent_id IS NULL)
    )
);

INSERT INTO schedules_rebuilt (id, workspace_id, target_kind, task_id, name, spec, tz, payload,
                               enabled, on_miss, next_at, last_work_id, last_fired_at,
                               last_outcome, fires, skips, created_at, updated_at)
SELECT id, workspace_id, 'task', task_id, name, spec, tz, payload,
       enabled, on_miss, next_at, last_work_id, last_fired_at,
       last_outcome, fires, skips, created_at, updated_at
  FROM schedules;

DROP TABLE schedules;

ALTER TABLE schedules_rebuilt RENAME TO schedules;

-- The indexes 0024 had, re-made: a rebuild takes them with the old table.
CREATE UNIQUE INDEX idx_schedules_name ON schedules (workspace_id, name);
CREATE INDEX idx_schedules_due ON schedules (next_at) WHERE enabled = 1;
CREATE INDEX idx_schedules_task ON schedules (task_id);

-- And two the direct paths need, for the question "what happens when this agent
-- or this gear is deleted" — which is asked by the screen that has to say a
-- schedule is now broken.
CREATE INDEX idx_schedules_agent ON schedules (target_agent_id) WHERE target_agent_id IS NOT NULL;
CREATE INDEX idx_schedules_gear ON schedules (target_gear_id) WHERE target_gear_id IS NOT NULL;
