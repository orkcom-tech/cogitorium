-- Versions of a workflow.
--
-- A workspace is a workflow, and a workflow that cannot be rolled back is one
-- nobody dares change. Every version is the WHOLE of what the workflow was —
-- its agents, the wires between them, the gears they may call, what they read,
-- and the clocks that start them — because a version number that identifies
-- some of that identifies no behaviour at all.
--
-- The snapshot is one JSON document rather than a set of shadow tables. Shadow
-- tables would mean every future change to an agent, a wire or a schedule
-- needing a matching change here, forever, and the first one somebody forgets
-- silently stops being versioned. A document written by one function and read
-- by one function has exactly one place to forget.
--
-- Taken by a person, with a message. The other arrangement — a version per
-- change — was considered and is a list of four hundred entries nobody can
-- read, which is a history in the same sense that a disk is a backup.
--
-- Numbers are per workspace and never reused. Rolling back writes a NEW
-- version rather than deleting the ones after it: a history that can be
-- rewritten cannot be produced in an argument about what ran.
CREATE TABLE workflow_versions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    number       INTEGER NOT NULL,
    message      TEXT NOT NULL,
    -- Who saved it, by name rather than by id: a version outlives the account
    -- that took it, and "saved by 4" answers nothing a year later.
    author       TEXT NOT NULL,
    -- restored_from is the version this one was rolled back to, when it was.
    -- Null for an ordinary save. It is what makes a rollback readable as an
    -- event rather than as a version that mysteriously resembles an old one.
    restored_from INTEGER,
    snapshot     TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    UNIQUE (workspace_id, number)
);

CREATE INDEX workflow_versions_by_workspace ON workflow_versions (workspace_id, number DESC);
