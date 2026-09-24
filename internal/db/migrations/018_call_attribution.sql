-- Delegation-overhead measurement: llm_calls/tool_calls previously carried only an agent name, so there was
-- no way to attribute a call to the request it served — the entry agent's own routing/synthesis calls and a
-- delegated specialist's calls were indistinguishable in aggregate. run_id/parent_run mirror Engine.RunInfo
-- exactly (set for every run, task-backed or plain chat), so a query can walk a call's ancestry back to its
-- root run without needing a separate durable "runs" table. task_id is kept too, for the (fewer) cases where
-- attributing to a task specifically is more useful than the run tree.
ALTER TABLE llm_calls  ADD COLUMN task_id    bigint;
ALTER TABLE llm_calls  ADD COLUMN run_id     bigint;
ALTER TABLE llm_calls  ADD COLUMN parent_run bigint;
ALTER TABLE tool_calls ADD COLUMN task_id    bigint;
ALTER TABLE tool_calls ADD COLUMN run_id     bigint;
ALTER TABLE tool_calls ADD COLUMN parent_run bigint;
CREATE INDEX llm_calls_run_idx   ON llm_calls(run_id)  WHERE run_id IS NOT NULL;
CREATE INDEX tool_calls_run_idx  ON tool_calls(run_id) WHERE run_id IS NOT NULL;
