-- Several web chats. topic '' is the main chat (always present); others are 'c<id>'. A chat may be focused on a
-- project bank: that bank is searched first and is where facts about the work land by default.
CREATE TABLE chats (
  id                bigserial PRIMARY KEY,
  topic             text NOT NULL UNIQUE,
  title             text NOT NULL DEFAULT '',
  auto_title        boolean NOT NULL DEFAULT true,    -- not renamed by the user: a generated title may replace it
  named_at          int NOT NULL DEFAULT 0,           -- number of user messages when the last generated title was made
  project_bank_id   bigint REFERENCES memory_banks(id) ON DELETE SET NULL,
  suggested_project text NOT NULL DEFAULT '',          -- a project the model thinks this chat belongs to (bank label or a new name)
  suggested_new     boolean NOT NULL DEFAULT false,    -- the suggestion is a project that does not exist yet
  suggestion_done   boolean NOT NULL DEFAULT false,    -- a suggestion was made or dismissed: do not ask again
  remember          boolean NOT NULL DEFAULT true,     -- false: messages of this chat are not kept for memory
  archived          boolean NOT NULL DEFAULT false,
  merged_into       text NOT NULL DEFAULT '',          -- topic this chat was merged into (it stays archived, restorable, until purged)
  merged_at         timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  last_at           timestamptz NOT NULL DEFAULT now()
);
INSERT INTO chats(topic, title, auto_title) VALUES ('', 'Main', false);
ALTER TABLE chat_messages ADD COLUMN moved_from text NOT NULL DEFAULT '';   -- topic a message came from when chats were merged
