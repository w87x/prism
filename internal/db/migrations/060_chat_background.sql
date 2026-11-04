-- Results of interrupted delegations must survive until Atlas's next turn.
CREATE TABLE IF NOT EXISTS chat_background_results (
  id bigserial PRIMARY KEY,
  channel text NOT NULL,
  topic text NOT NULL DEFAULT '',
  moved_from text NOT NULL DEFAULT '',
  task_id bigint,
  result text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  consumed_at timestamptz
);
CREATE INDEX IF NOT EXISTS chat_background_pending_idx ON chat_background_results(channel, topic, id) WHERE consumed_at IS NULL;
