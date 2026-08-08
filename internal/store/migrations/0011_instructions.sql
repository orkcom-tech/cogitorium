-- The instruction library is an INDEX, not a store. The text, its versions
-- and its history live in Contextverse at `path`; Cogitorium keeps only
-- what makes an instruction findable and attributable. Storing the content
-- here would rebuild the thing Contextverse exists to be.
--
-- No approval column, deliberately: an instruction is text that reaches a
-- prompt, not code that runs on the machine. A gate here would be ceremony
-- without a threat model, and habitual approving devalues the gate that
-- does protect something.
CREATE TABLE instructions (
    id                  INTEGER PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    description         TEXT NOT NULL DEFAULT '',
    tags                TEXT NOT NULL DEFAULT '[]',
    path                TEXT NOT NULL UNIQUE,
    origin_workspace_id INTEGER REFERENCES workspaces (id) ON DELETE SET NULL,
    created_by_agent_id INTEGER REFERENCES agents (id) ON DELETE SET NULL,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);
