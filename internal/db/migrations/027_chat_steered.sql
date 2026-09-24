-- A message the user sent while an agent was already working: it steers the running turn instead of
-- starting a new one. Recorded so the chat can say so.
ALTER TABLE chat_messages ADD COLUMN steered boolean NOT NULL DEFAULT false;
