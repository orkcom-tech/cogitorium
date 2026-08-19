-- A fourth kind of work: a plugin's own background task.
--
-- SQLite cannot widen a CHECK in place, so this is the copy-drop-rename 0022
-- already did for inlet_runs, for the same reason. 0022 put 'callback' into the
-- CHECK before it had a producer precisely to avoid doing this again — and then
-- plugins arrived, which nothing in 0022 could have known about. This is the
-- cost it was trying to defer, paid once.
--
-- Safe for the same reason it was then: nothing has a foreign key to work.
--
-- workspace_id stays NOT NULL and a plugin unit stores 0. A plugin task is
-- ordinarily the install's work rather than a workspace's — refreshing a cache,
-- reconciling a listing — and inventing a nullable column would change what
-- every existing reader has to handle in order to express "none" for one kind.
-- Zero is not a workspace id anywhere in this schema.
CREATE TABLE work_rebuilt (
    id           INTEGER PRIMARY KEY,
    kind         TEXT    NOT NULL CHECK (kind IN ('delivery', 'chat', 'callback', 'plugin')),
    workspace_id INTEGER NOT NULL,
    lane         TEXT    NOT NULL,
    args         TEXT    NOT NULL DEFAULT '{}',
    idem_key     TEXT,
    state        TEXT    NOT NULL CHECK (state IN ('queued', 'claimed', 'done', 'dead')),
    run_id       INTEGER,
    run_after    TEXT    NOT NULL,
    deadline     TEXT    NOT NULL DEFAULT '',
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 1,
    last_error   TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

INSERT INTO work_rebuilt (id, kind, workspace_id, lane, args, idem_key, state, run_id,
                          run_after, deadline, attempts, max_attempts, last_error,
                          created_at, updated_at)
SELECT id, kind, workspace_id, lane, args, idem_key, state, run_id,
       run_after, deadline, attempts, max_attempts, last_error,
       created_at, updated_at
  FROM work;

DROP TABLE work;
ALTER TABLE work_rebuilt RENAME TO work;

-- Every index from 0022, verbatim. The lane index is the guarantee: one claimed
-- unit per lane, enforced by the database rather than by a subquery that is
-- silently wrong the moment there is a second writer.
CREATE UNIQUE INDEX idx_work_lane_claimed ON work (lane) WHERE state = 'claimed';
CREATE UNIQUE INDEX idx_work_idem ON work (kind, workspace_id, idem_key)
    WHERE idem_key IS NOT NULL;
CREATE INDEX idx_work_runnable ON work (run_after, id) WHERE state = 'queued';
CREATE INDEX idx_work_run ON work (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX idx_work_ws ON work (workspace_id, id DESC);
