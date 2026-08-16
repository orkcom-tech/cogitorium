-- External MCP servers: somebody else's tools, granted to an agent the way a
-- gear is.
--
-- READ THIS BEFORE THE COLUMNS. A gear is source this install holds, versioned,
-- approved line by line by an operator who read it, and run in a container that
-- cannot see the server's filesystem. An external MCP server is none of those.
-- It is a command. Cogitorium never sees its source and cannot check its tool
-- list against anything — that list is the server's own account of itself. In
-- this first cut the child runs on the host, as this server's user, outside the
-- sandbox, so an approved MCP server can open the database this table lives in
-- and read every provider key in it.
--
-- Nothing here fixes that. What these tables do is make every step an act an
-- operator took deliberately, and make a change after the fact visible.

CREATE TABLE mcp_servers (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    -- The executable and its arguments as a JSON array. Two columns and never
    -- one string: one string means a shell, and a shell means the arguments are
    -- parsed by something with its own opinion about quoting.
    command TEXT NOT NULL,
    args    TEXT NOT NULL DEFAULT '[]',
    cwd     TEXT NOT NULL DEFAULT '',
    -- The NAMES this server is given at spawn, resolved through the same
    -- secrets.Resolver a gear's env_names go through (0020). No value is ever
    -- in this column.
    env_names TEXT NOT NULL DEFAULT '[]',
    -- stdio only. The CHECK is the honest statement that the other transports
    -- are not built; widening it later costs a table rebuild, which 0010 had to
    -- do for exactly this reason and said so. One rebuild is cheaper than a
    -- column that silently accepts "sse" and is ignored.
    transport TEXT NOT NULL DEFAULT 'stdio' CHECK (transport IN ('stdio')),
    -- The same vocabulary as a gear's, deliberately: the approval screen, the
    -- store constants and the operator's mental model all carry over.
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'disabled')),
    -- sha256 over command, args, cwd, env_names and transport, taken when the
    -- operator approved. Recomputed at every spawn; a mismatch refuses and puts
    -- the row back to pending — the same rule as forging a new gear version
    -- clearing its approval, applied to the only thing here that can be hashed.
    --
    -- What it does NOT cover, and this is the honest limit: the bytes at that
    -- path. `npx @thing/server@latest` refetches on every spawn and this column
    -- never changes.
    approved_fingerprint TEXT NOT NULL DEFAULT '',
    timeout_seconds      INTEGER NOT NULL DEFAULT 60,
    -- No origin_workspace_id and no created_by_agent_id, and their absence is
    -- the design: a gear has them because an agent forges one. Nothing but an
    -- operator ever writes this table.
    created_by_user_id INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

-- The tool list, cached and approved one tool at a time.
--
-- Cached because the engine rebuilds an agent's tool list on every iteration of
-- its loop; asking a child process each time would be several round-trips per
-- model call. So the child is touched when a tool is CALLED and when an operator
-- explicitly asks it to list, and at no other time.
--
-- Approved one at a time because a server's tool list is the server's own claim
-- about itself and it can change between spawns. A server that grows a
-- run_shell tool after approval does not thereby acquire it.
CREATE TABLE mcp_tools (
    id        INTEGER NOT NULL PRIMARY KEY,
    server_id INTEGER NOT NULL REFERENCES mcp_servers (id) ON DELETE CASCADE,
    -- What the server calls it: somebody else's namespace, which may hold
    -- characters and lengths no model provider accepts.
    remote_name TEXT NOT NULL,
    -- What the model is offered. Computed once and stored, so dispatch is a
    -- lookup rather than a re-derivation that can drift from what was offered.
    offered_name TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    input_schema TEXT NOT NULL DEFAULT '{}',
    approved     INTEGER NOT NULL DEFAULT 0,
    -- When it was first seen and when it was last in a listing. A tool that
    -- stopped being offered is kept rather than deleted: the question later is
    -- "what did this server used to expose", and a delete cannot answer it.
    first_seen_at TEXT NOT NULL,
    listed_at     TEXT NOT NULL,
    UNIQUE (server_id, remote_name)
);

-- Unique across the whole install, because the offered name is what a model
-- says and what dispatch looks up. Two servers offering the same name would
-- otherwise make which one runs a matter of row order.
CREATE UNIQUE INDEX idx_mcp_tools_offered ON mcp_tools (offered_name);

-- Which agents may reach which server. A sibling of gear_bindings rather than a
-- column on it: a binding row with a nullable gear_id AND a nullable server_id
-- is a table where every query has to say which kind it means, and half of them
-- would forget.
CREATE TABLE mcp_bindings (
    id           INTEGER PRIMARY KEY,
    server_id    INTEGER NOT NULL REFERENCES mcp_servers (id) ON DELETE CASCADE,
    workspace_id INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- NULL means the whole workspace, exactly as in gear_bindings.
    agent_id   INTEGER REFERENCES agents (id) ON DELETE CASCADE,
    created_at TEXT NOT NULL
);

-- COALESCE, because NULLs are distinct from each other in a SQLite unique
-- index: without it, "bound to the whole workspace" could be inserted any
-- number of times.
CREATE UNIQUE INDEX idx_mcp_bindings_unique
    ON mcp_bindings (server_id, workspace_id, COALESCE(agent_id, 0));
