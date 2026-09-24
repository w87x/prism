-- Image input: messages reference uploaded images (stored as artifacts); models say whether they can see them.
ALTER TABLE session_messages ADD COLUMN images bigint[] NOT NULL DEFAULT '{}';
ALTER TABLE chat_messages    ADD COLUMN images bigint[] NOT NULL DEFAULT '{}';
ALTER TABLE models           ADD COLUMN vision boolean NOT NULL DEFAULT true;
