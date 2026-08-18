-- An MCP server may be a command OR a URL.
--
-- 0027 wrote `CHECK (transport IN ('stdio'))` and said, in its own comment, that
-- this was the honest statement that the other transports were not built and
-- that widening it would cost a table rebuild. This is that rebuild.
--
-- WHY IT HAD TO HAPPEN. Two thirds of the servers in the public registry are
-- reachable only over HTTP — they are somebody else's hosted service, not a
-- package to spawn. A product that could install the other third would be a
-- product whose library is mostly buttons that do nothing, and "we support MCP
-- servers, except most of them" is not a sentence worth shipping.
--
-- The rebuild is safe here in the way 0018's was: mcp_tools and mcp_bindings
-- reference mcp_servers, so the drop below WOULD cascade — which is exactly why
-- the new table is filled and renamed while the old one still exists, and the
-- children are rebuilt around it rather than deleted. See the order at the
-- bottom.

CREATE TABLE mcp_servers_rebuilt (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',

    -- Now three, and each names a real thing rather than a placeholder:
    --
    --   stdio            a child process on this host, as in 0027.
    --   streamable-http  one POST per message to a single endpoint, the answer
    --                    arriving as JSON or as an SSE stream scoped to that
    --                    request. The current transport.
    --   sse              the deprecated 2024-11-05 shape: a GET that opens a
    --                    stream whose first event names a URL to POST to. Kept
    --                    because it is deployed, not because it is good.
    transport TEXT NOT NULL DEFAULT 'stdio'
        CHECK (transport IN ('stdio', 'streamable-http', 'sse')),

    -- The stdio half. Empty on a remote server.
    command TEXT NOT NULL DEFAULT '',
    args    TEXT NOT NULL DEFAULT '[]',
    cwd     TEXT NOT NULL DEFAULT '',
    -- The NAMES this child is given at spawn, resolved through the same
    -- secrets.Resolver a gear's go through. No value is ever in this column.
    env_names TEXT NOT NULL DEFAULT '[]',

    -- The remote half. Empty on a stdio server.
    url TEXT NOT NULL DEFAULT '',
    -- Headers, as a JSON object of HEADER NAME -> NAMED VALUE. `{"Authorization":
    -- "JIRA_TOKEN"}` means: at connect time, resolve JIRA_TOKEN the way a gear's
    -- named value is resolved and send it as the Authorization header.
    --
    -- NAMES ON BOTH SIDES, and the second one is the point. A remote MCP server
    -- is authenticated with a bearer token, and the obvious design — a `headers`
    -- column holding what to send — would put a live credential in plaintext in
    -- this database, which is the one thing 0020 exists to prevent. So this
    -- column holds a mapping and never a secret, exactly as env_names does, and
    -- the value is fetched at the moment it is used.
    header_names TEXT NOT NULL DEFAULT '{}',

    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'disabled')),

    -- sha256 over everything above that decides what gets contacted and with
    -- what: for stdio the command, args, cwd and env names; for a remote server
    -- THE URL and the header names. A URL that changed after approval is the
    -- remote equivalent of a command that changed, and it is worse, because
    -- nothing about a hostname looks different when it starts pointing
    -- somewhere else.
    approved_fingerprint TEXT NOT NULL DEFAULT '',
    timeout_seconds      INTEGER NOT NULL DEFAULT 60,

    created_by_user_id INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,

    -- A row must carry the half its transport needs. Enforced here because this
    -- is what the spawn path reads with nobody watching: a 'streamable-http'
    -- row with an empty url and a leftover command is a row that would run a
    -- local process when an operator believed they had configured a URL.
    CHECK (
        (transport = 'stdio' AND command <> '' AND url = '')
     OR (transport IN ('streamable-http', 'sse') AND url <> '' AND command = '')
    )
);

INSERT INTO mcp_servers_rebuilt (id, name, description, transport, command, args, cwd, env_names,
                                 status, approved_fingerprint, timeout_seconds,
                                 created_by_user_id, created_at, updated_at)
SELECT id, name, description, transport, command, args, cwd, env_names,
       -- Every existing row keeps its status and its fingerprint. The
       -- fingerprint's INPUTS have not changed for a stdio server — the new
       -- columns are empty on all of them — so an approval that was valid
       -- before this migration is still valid after it, and nothing an operator
       -- already read has to be read again.
       status, approved_fingerprint, timeout_seconds,
       created_by_user_id, created_at, updated_at
  FROM mcp_servers;

-- The children are rebuilt rather than allowed to cascade. Dropping the parent
-- with foreign keys enforced would take every tool and every binding with it,
-- and inside the migration runner's transaction `PRAGMA foreign_keys = OFF` is
-- a no-op — so the rows are lifted out, the parent is swapped, and they go back
-- against the new one.
CREATE TABLE mcp_tools_kept AS SELECT * FROM mcp_tools;
CREATE TABLE mcp_bindings_kept AS SELECT * FROM mcp_bindings;

DROP TABLE mcp_tools;
DROP TABLE mcp_bindings;
DROP TABLE mcp_servers;

ALTER TABLE mcp_servers_rebuilt RENAME TO mcp_servers;

CREATE TABLE mcp_tools (
    id            INTEGER NOT NULL PRIMARY KEY,
    server_id     INTEGER NOT NULL REFERENCES mcp_servers (id) ON DELETE CASCADE,
    remote_name   TEXT    NOT NULL,
    offered_name  TEXT    NOT NULL,
    description   TEXT    NOT NULL DEFAULT '',
    input_schema  TEXT    NOT NULL DEFAULT '{}',
    approved      INTEGER NOT NULL DEFAULT 0,
    first_seen_at TEXT    NOT NULL,
    listed_at     TEXT    NOT NULL,
    UNIQUE (server_id, remote_name)
);

CREATE TABLE mcp_bindings (
    id           INTEGER PRIMARY KEY,
    server_id    INTEGER NOT NULL REFERENCES mcp_servers (id) ON DELETE CASCADE,
    workspace_id INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent_id     INTEGER REFERENCES agents (id) ON DELETE CASCADE,
    created_at   TEXT    NOT NULL
);

INSERT INTO mcp_tools SELECT * FROM mcp_tools_kept;
INSERT INTO mcp_bindings SELECT * FROM mcp_bindings_kept;

DROP TABLE mcp_tools_kept;
DROP TABLE mcp_bindings_kept;

CREATE UNIQUE INDEX idx_mcp_tools_offered ON mcp_tools (offered_name);
CREATE UNIQUE INDEX idx_mcp_bindings_unique
    ON mcp_bindings (server_id, workspace_id, COALESCE(agent_id, 0));
