-- The install can start work because a clock said so.
--
-- Until now nothing could: every run began with somebody pressing send or
-- something outside making an HTTP request. "Every weeknight at 02:00, classify
-- yesterday's tickets" — the single most common shape an automation takes — was
-- not expressible at all.
--
-- A schedule points at an INLET TASK rather than carrying its own instruction
-- and agent. That keeps one definition of what a job is: the task already says
-- which agent, what to tell it, what it accepts and what success means, and a
-- scheduled firing is that same job with nobody on the other end. Two ways to
-- define a job would be two things to keep in step, and the second would be the
-- one that never got the expect block.
CREATE TABLE schedules (
    id           INTEGER PRIMARY KEY,
    workspace_id INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Deleting the task takes its schedules with it. A schedule pointing at a
    -- job that no longer exists would fire forever into nothing.
    task_id      INTEGER NOT NULL REFERENCES inlet_tasks (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,

    -- `every 15m`, or a five-field cron subset. Validated when it is written,
    -- never when it fires: the only moment an operator can act on "that is not
    -- a spec" is while they are still looking at what they typed.
    spec         TEXT    NOT NULL,
    -- An IANA name. Empty is UTC. The binary embeds tzdata, so this means the
    -- same thing on a laptop and in a container with no zoneinfo.
    tz           TEXT    NOT NULL DEFAULT '',

    -- The body handed to the task, exactly as an HTTP caller would have sent
    -- it. A JSON task's schema is checked against this when the schedule is
    -- saved, so a schedule cannot be created that will fail every time it runs.
    payload      TEXT    NOT NULL DEFAULT '{}',

    enabled      INTEGER NOT NULL DEFAULT 1,

    -- What to do when the previous firing has not finished. 'skip' is the
    -- default and the honest one: a job that takes longer than its interval
    -- will never catch up, and queueing every missed firing turns a slow job
    -- into a backlog that outlives the reason for it. 'run' is for the operator
    -- who genuinely wants each tick attempted.
    on_miss      TEXT    NOT NULL DEFAULT 'skip' CHECK (on_miss IN ('skip', 'run')),

    -- When this is next due, in UTC. The tick reads this rather than
    -- recomputing from the spec, so a schedule that has been enabled, edited or
    -- fired has one place that says what happens next.
    next_at      TEXT    NOT NULL,
    -- The last unit this schedule created, and what became of the last firing.
    -- Kept on the row because "did last night's job run" is the first question
    -- anybody asks, and it should not require joining a queue.
    last_work_id INTEGER,
    last_fired_at TEXT   NOT NULL DEFAULT '',
    last_outcome TEXT    NOT NULL DEFAULT '',
    fires        INTEGER NOT NULL DEFAULT 0,
    skips        INTEGER NOT NULL DEFAULT 0,

    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

-- One name per workspace, so an operator can say "the nightly one" and mean it.
CREATE UNIQUE INDEX idx_schedules_name ON schedules (workspace_id, name);
-- What the tick reads: the due ones, oldest first.
CREATE INDEX idx_schedules_due ON schedules (next_at) WHERE enabled = 1;
CREATE INDEX idx_schedules_task ON schedules (task_id);
