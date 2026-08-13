-- The three durable tables written mid-run learn which run they belong to.
--
-- gear_runs, gear_connections and agent_usage have always recorded what
-- happened — a gear executing, a granted gear reaching a host, a model call and
-- its tokens — and none of them could be joined to the delivery that caused it.
-- The only correlation available was the timestamp, which is a guess dressed as
-- an answer: it happens to work today ONLY because the engine serialises runs
-- per workspace, and it stops working the moment anything else is true.
--
-- Named work_id rather than run_id on purpose. inlet_runs.id is already called
-- "the run" in every route, log line and UI string, and two columns called
-- run_id pointing at different tables is a join somebody will get backwards at
-- three in the morning.
--
-- Nullable, and no foreign key. A chat turn has a work unit but an operator's
-- ad-hoc gear run does not, so NULL means "not part of a queued unit" rather
-- than missing data; and the queue prunes settled units, so a hard reference
-- would either block that prune or delete the audit it points at. An audit row
-- must outlive the bookkeeping that produced it.
ALTER TABLE agent_usage      ADD COLUMN work_id INTEGER;
ALTER TABLE gear_runs        ADD COLUMN work_id INTEGER;
ALTER TABLE gear_connections ADD COLUMN work_id INTEGER;

CREATE INDEX idx_agent_usage_work      ON agent_usage (work_id)      WHERE work_id IS NOT NULL;
CREATE INDEX idx_gear_runs_work        ON gear_runs (work_id)        WHERE work_id IS NOT NULL;
CREATE INDEX idx_gear_connections_work ON gear_connections (work_id) WHERE work_id IS NOT NULL;

-- And the usage table gains the index the spend question needs.
--
-- Both existing aggregates are lifetime sums with no time window, so "what did
-- this workspace spend last week" could not be asked at all — the data was
-- there and the query was not.
CREATE INDEX idx_agent_usage_ws_time ON agent_usage (workspace_id, created_at);
