-- The network a gear was granted, and every connection it actually made.
--
-- The grant is decided at APPROVAL, in the same act and on the same screen as
-- the source: an operator reading the code is the only person in a position to
-- say whether this code should be able to reach out. It is therefore a column
-- on the gear rather than a table of its own — it has exactly the lifetime of
-- the approval it was part of, and forging a new version clears it along with
-- the approval, because approval covers exact content and so does this.
ALTER TABLE gears ADD COLUMN network_granted INTEGER NOT NULL DEFAULT 0;

-- Where it may reach: a JSON array of hosts. EMPTY MEANS ANYWHERE, and that is
-- a legitimate choice an operator may make — this is not a gate on them. It is
-- what makes the record below auditable ("this gear may reach api.example.com"
-- is a sentence someone can check a year later) and what a machine can test on
-- every connection instead of a person on every request.
ALTER TABLE gears ADD COLUMN network_hosts TEXT NOT NULL DEFAULT '[]';

-- One row per connection a gear opened, on the model of search_queries.
--
-- No foreign key, for the same reason that one has none: an audit row must
-- outlive the gear it audits, and SQLite recycles rowids, so gear_name is what
-- the log shows. The row is written BEFORE the socket is opened, so a crash, a
-- refusal or a timeout still leaves the attempt on record.
--
-- What is deliberately NOT here is the path, the query string, the headers and
-- the body. A gear that puts a credential in a URL would otherwise write it
-- into this table in plaintext, where the redactor that guards stdout, the run
-- record and the operator's live stream cannot reach it. Host, port and method
-- are what an audit needs and the most that can be safely kept.
CREATE TABLE gear_connections (
    id             INTEGER PRIMARY KEY,
    gear_id        INTEGER,
    gear_name      TEXT    NOT NULL DEFAULT '',
    version        INTEGER NOT NULL DEFAULT 0,
    workspace_id   INTEGER,
    agent_id       INTEGER,
    -- Empty means the operator: a dry run from the catalog has no agent.
    agent_name     TEXT    NOT NULL DEFAULT '',
    host           TEXT    NOT NULL,
    port           INTEGER NOT NULL DEFAULT 0,
    -- CONNECT for a tunnel (which is every https:// call), otherwise the plain
    -- HTTP method. It says what shape the traffic was, not what was in it.
    method         TEXT    NOT NULL DEFAULT '',
    -- The grant this connection was checked against, copied in rather than
    -- joined: the gear's list will change, and the question afterwards is what
    -- the rule WAS at the time.
    allowed        TEXT    NOT NULL DEFAULT '[]',
    -- 'open' is the only live state and every other one is terminal, so a row
    -- that is still 'open' after a restart is a lie the reconcile at startup
    -- turns into 'interrupted' — the same treatment search_queries gets.
    --
    -- The three refusals are separate because they are three different jobs:
    -- refused_destination is the operator's list doing its work, refused_local
    -- is this server protecting itself and cannot be granted away, and
    -- refused_no_grant is traffic that arrived without a live run behind it.
    state          TEXT    NOT NULL CHECK (state IN (
                       'open', 'closed', 'failed', 'interrupted',
                       'refused_destination', 'refused_local',
                       'refused_no_grant', 'refused_invalid')),
    bytes_sent     INTEGER NOT NULL DEFAULT 0,
    bytes_received INTEGER NOT NULL DEFAULT 0,
    error          TEXT    NOT NULL DEFAULT '',
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL
);

CREATE INDEX idx_gear_connections_gear ON gear_connections (gear_id, id DESC);
CREATE INDEX idx_gear_connections_ws ON gear_connections (workspace_id, id DESC);
