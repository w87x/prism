-- Core memory: a few short labelled blocks the user (and, carefully, agents) keep up to date, always present in every agent's prompt —
-- who the user is, how they like things done, what they are working on now. History keeps what each block said before.
CREATE TABLE IF NOT EXISTS core_blocks (
  label       text PRIMARY KEY,
  text        text NOT NULL DEFAULT '',
  description text NOT NULL DEFAULT '',
  position    int  NOT NULL DEFAULT 0,
  updated_by  text NOT NULL DEFAULT '',
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS core_block_history (
  id         bigserial PRIMARY KEY,
  label      text NOT NULL,
  text       text NOT NULL,
  updated_by text NOT NULL DEFAULT '',
  at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS core_block_history_label_idx ON core_block_history (label, id DESC);
INSERT INTO core_blocks (label, description, position) VALUES
  ('about_user', 'who the user is: name, work, family, where they live — a few lines', 1),
  ('preferences', 'how the user likes things done: tone, language, what to avoid', 2),
  ('now', 'what the user is working on or caring about at the moment', 3)
ON CONFLICT (label) DO NOTHING;
