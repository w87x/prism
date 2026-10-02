-- Where a task's prompt tokens went (system prompt / tool definitions / user / agent / tool results), as of
-- the agent's latest turn. Kept on the task so a finished sub-agent's context can still be inspected.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS ctx_breakdown jsonb;
