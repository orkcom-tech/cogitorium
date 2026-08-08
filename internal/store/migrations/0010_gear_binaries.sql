-- A gear can now be an executable, not only a script for an interpreter.
-- SQLite has no ALTER for CHECK constraints, so the table is rebuilt.
CREATE TABLE gears_new (
    id                  INTEGER PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    description         TEXT NOT NULL DEFAULT '',
    tags                TEXT NOT NULL DEFAULT '[]',
    origin_workspace_id INTEGER REFERENCES workspaces (id) ON DELETE SET NULL,
    created_by_agent_id INTEGER REFERENCES agents (id) ON DELETE SET NULL,
    version             INTEGER NOT NULL DEFAULT 1,
    runtime             TEXT NOT NULL CHECK (runtime IN ('python', 'node', 'bash', 'binary')),
    entrypoint          TEXT NOT NULL,
    args_schema         TEXT NOT NULL DEFAULT '{}',
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'disabled')),
    timeout_seconds     INTEGER NOT NULL DEFAULT 60,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

INSERT INTO gears_new (id, name, description, tags, origin_workspace_id, created_by_agent_id,
                       version, runtime, entrypoint, args_schema, status, timeout_seconds,
                       created_at, updated_at)
SELECT id, name, description, tags, origin_workspace_id, created_by_agent_id,
       version, runtime, entrypoint, args_schema, status, timeout_seconds,
       created_at, updated_at
FROM gears;

DROP TABLE gears;
ALTER TABLE gears_new RENAME TO gears;

-- Uploaded files are not all text. Content stays in the existing column for
-- source; binary files carry base64 and say so, so review can show source
-- as source and refuse to render a blob as if it were code.
ALTER TABLE gear_files ADD COLUMN encoding TEXT NOT NULL DEFAULT 'utf8';
