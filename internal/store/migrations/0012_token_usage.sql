-- Token spend, recorded per turn against the agent that spent it.
--
-- A separate table rather than columns on ws_messages: a turn's cost belongs
-- to the turn, not to the assistant message it happened to produce, and a
-- turn that ends in a tool call or an error still costs tokens. Keeping it
-- separate also means the message history stays exactly what it was.
--
-- reported distinguishes "the provider told us nothing" from "the turn cost
-- zero" — not every OpenAI-compatible server returns usage, and a total that
-- silently reads 0 for such a provider would be a lie the operator only finds
-- out about from a bill.
CREATE TABLE agent_usage (
    id            INTEGER PRIMARY KEY,
    workspace_id  INTEGER NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    agent_id      INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    model_label   TEXT NOT NULL DEFAULT '',
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    reported      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
);

CREATE INDEX idx_agent_usage_agent ON agent_usage (agent_id, id);
CREATE INDEX idx_agent_usage_ws ON agent_usage (workspace_id, id);
