-- Telegram topic organisation for crons and intents: each gets a resolved topic name (its own, or shared with
-- others of the same project); project_bank_id records the project match so a shared topic is never deleted
-- just because one of several crons/intents using it was removed.
ALTER TABLE crons ADD COLUMN IF NOT EXISTS topic text NOT NULL DEFAULT '';
ALTER TABLE crons ADD COLUMN IF NOT EXISTS project_bank_id bigint REFERENCES memory_banks ON DELETE SET NULL;
ALTER TABLE intents ADD COLUMN IF NOT EXISTS topic text NOT NULL DEFAULT '';
ALTER TABLE intents ADD COLUMN IF NOT EXISTS project_bank_id bigint REFERENCES memory_banks ON DELETE SET NULL;
-- carries the firing cron/intent's resolved topic to the task that runs it, so its "reports back to the user"
-- notice (internal/agent/orchestrator.go) lands in the right place instead of always the DM.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS notify_topic text NOT NULL DEFAULT '';
