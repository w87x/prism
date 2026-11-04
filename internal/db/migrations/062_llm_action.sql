-- Attribute background model calls to the operation that requested them.
ALTER TABLE llm_calls ADD COLUMN IF NOT EXISTS action text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS llm_calls_action_ts_idx ON llm_calls(action, ts) WHERE action <> '';
