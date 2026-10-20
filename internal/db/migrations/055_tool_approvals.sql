-- Every time a tool asked "may I run?" and the user answered, so that a tool they always allow can be offered for arming (earned autonomy).
CREATE TABLE IF NOT EXISTS tool_approvals (
	id      bigserial PRIMARY KEY,
	tool    text NOT NULL,
	agent   text NOT NULL DEFAULT '',
	task_id bigint NOT NULL DEFAULT 0,
	allowed boolean NOT NULL,
	at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tool_approvals_tool ON tool_approvals (tool, at DESC);
