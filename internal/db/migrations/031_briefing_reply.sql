-- A briefing sometimes asks the user something; the reply is kept on the briefing and also handed to the agent that wrote it.
ALTER TABLE briefings ADD COLUMN reply text NOT NULL DEFAULT '';
ALTER TABLE briefings ADD COLUMN replied_at timestamptz;
