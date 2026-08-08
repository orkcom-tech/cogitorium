CREATE TABLE providers (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    type       TEXT NOT NULL CHECK (type IN ('anthropic', 'openai-compatible')),
    base_url   TEXT NOT NULL,
    api_key    TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE models (
    id         INTEGER PRIMARY KEY,
    provider_id INTEGER NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    model_name TEXT NOT NULL,
    label      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (provider_id, model_name)
);
