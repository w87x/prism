-- An agent can go to sleep and be resumed later with the same context (the sleep tool): the task goes back to 'queued' with a
-- wake-up time, and the dispatcher leaves it alone until then. wake_note says why it slept (shown to it when it wakes).
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS wake_at timestamptz;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS wake_note text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS tasks_wake_idx ON tasks (wake_at) WHERE wake_at IS NOT NULL;
