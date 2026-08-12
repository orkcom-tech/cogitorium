-- Named values a gear is given at run time.
--
-- A gear is given NAMES; it reads the VALUES from its environment. The model
-- never sees a value — an agent's answer leaves the building in an inlet
-- response and in the chat, so a secret in a prompt is a secret published.
--
-- variables and secrets live in ONE table because they are one mechanism with
-- one difference: a variable's value is shown in the interface and in the
-- record, a secret's is shown once when set and never again. Two tables would
-- mean two lookups, two scopes, and two chances for one name to mean different
-- things depending on which was asked first.
CREATE TABLE env_values (
    id           INTEGER PRIMARY KEY,
    -- NULL is the install-wide value; a row with a workspace is that
    -- workspace's override of the same name.
    workspace_id INTEGER REFERENCES workspaces (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    kind         TEXT    NOT NULL CHECK (kind IN ('variable', 'secret')),
    -- A variable's value is stored as it was typed. A secret's is
    -- AES-256-GCM ciphertext, and the key that opens it is deliberately NOT in
    -- this database: it comes from COGITORIUM_SECRET_KEY in the environment,
    -- following COGITORIUM_ADMIN_TOKEN's precedent. A key stored beside its own
    -- ciphertext protects nothing.
    value        TEXT    NOT NULL,
    -- Which key sealed it: the first 8 hex characters of the derived key's
    -- SHA-256. Enough to tell "the server was started with a different key"
    -- from "this row is corrupt" — different problems with different fixes,
    -- and an operator who cannot tell them apart will try the wrong one.
    key_id       TEXT    NOT NULL DEFAULT '',
    description  TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

-- COALESCE because SQLite treats every NULL as distinct, so a plain
-- UNIQUE (name, workspace_id) would happily hold two global rows for one name
-- and resolution would then depend on row order. gear_bindings does the same
-- thing for the same reason.
CREATE UNIQUE INDEX idx_env_values_name ON env_values (name, COALESCE(workspace_id, 0));

-- Which names a gear asks for, declared when it is forged, like its args
-- schema. This is the whole of the operator's control over a gear that holds
-- credentials: the approval screen shows this list beside the source, so the
-- decision "this code gets these named credentials" is made with both halves
-- visible. Shown apart, it is made blind.
--
-- A JSON array of names. Never a value — nothing in this column is a secret,
-- which is what makes it safe to export in a bundle and print in a log.
--
-- Added in place: no CHECK to widen, so the table is not rebuilt and existing
-- gears are untouched. '[]' is "this gear asks for nothing", which is every
-- gear that exists today, and such a gear runs exactly as it did before.
ALTER TABLE gears ADD COLUMN env_names TEXT NOT NULL DEFAULT '[]';
