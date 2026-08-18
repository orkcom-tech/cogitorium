-- Answers an operator gave in the interface, so they survive a restart.
--
-- WHY THIS IS NOT config.yaml. That file is only ever read, never written —
-- deliberately, because on Kubernetes it is a ConfigMap and a product that
-- rewrote its own ConfigMap would be fighting whatever applies the chart. So a
-- setting an operator changes from a browser has nowhere to live, and the first
-- one that needed to — "may this install ask whether a newer version exists" —
-- evaporated on every restart, which meant the product asked the same question
-- forever and looked like it had not listened.
--
-- WHY A KEY-VALUE TABLE rather than a column somewhere. There is one row in it
-- today. A column on a table would need a table that this belongs to, and there
-- isn't one: the install itself is not an entity in this schema. A settings
-- table is the honest shape for "facts about the install an operator decided",
-- and the alternative — a one-row `install` table with a column per answer — is
-- a migration for every future answer.
--
-- WHAT DOES NOT GO IN HERE, and this is the boundary that matters: anything the
-- config file must be able to forbid. The file is the CEILING and this table is
-- the answer under it. `update_check: off` in the file is absolute and cannot
-- be lifted by a row here — the check for that lives in update.Checker.SetMode,
-- which refuses to leave `off` at all. A row that could override the file would
-- turn an operator's decision on the server's own disk into a suggestion.
--
-- No credentials, ever. Secrets are encrypted in their own table (0020) and
-- this one is plaintext by design, because everything in it is meant to be
-- readable by whoever can read the database.
CREATE TABLE settings (
    key        TEXT NOT NULL PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
