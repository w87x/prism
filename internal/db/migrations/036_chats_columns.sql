-- Columns added to the chats feature after migration 035 had already been applied on some installs (035 was edited in
-- place). Idempotent: a database that already has them is left alone.
ALTER TABLE chats ADD COLUMN IF NOT EXISTS named_at    int NOT NULL DEFAULT 0;
ALTER TABLE chats ADD COLUMN IF NOT EXISTS merged_into text NOT NULL DEFAULT '';
ALTER TABLE chats ADD COLUMN IF NOT EXISTS merged_at   timestamptz;
ALTER TABLE chat_messages ADD COLUMN IF NOT EXISTS moved_from text NOT NULL DEFAULT '';
