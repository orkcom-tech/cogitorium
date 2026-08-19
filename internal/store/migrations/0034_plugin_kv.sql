-- A plugin's own durable storage.
--
-- Its own table rather than a corner of settings: a plugin's data has a
-- different lifetime from the operator's configuration, and deleting a plugin
-- should take its rows with it — which is what the cascade below is for, if
-- plugins ever become rows. Today the plugin id is a string because a plugin
-- is a directory on disk, and cleanup happens on remove.
--
-- Values are bytes, not JSON. The host does not parse what a plugin stores:
-- doing so would make the host's JSON dialect part of a plugin's data format
-- forever, and a plugin storing a PNG thumbnail is not doing anything wrong.
CREATE TABLE plugin_kv (
    plugin     TEXT    NOT NULL,
    key        TEXT    NOT NULL,
    value      BLOB    NOT NULL,
    -- version increments on every write, and is what compare-and-set compares.
    -- A value-based CAS would make two writers who happen to write the same
    -- bytes indistinguishable from one writer, which is precisely the case CAS
    -- exists to tell apart.
    version    INTEGER NOT NULL DEFAULT 1,
    updated_at TEXT    NOT NULL,
    PRIMARY KEY (plugin, key)
) WITHOUT ROWID;

CREATE INDEX idx_plugin_kv_plugin ON plugin_kv (plugin);
