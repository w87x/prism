-- Keep completed questions so a long wait or timeout can be diagnosed later.
CREATE TABLE IF NOT EXISTS ask_history (
  id bigserial PRIMARY KEY,
  run_id bigint NOT NULL,
  task_id bigint,
  agent text NOT NULL,
  kind text NOT NULL,
  question text NOT NULL,
  answer text NOT NULL DEFAULT '',
  outcome text NOT NULL,
  channel text NOT NULL DEFAULT '',
  topic text NOT NULL DEFAULT '',
  asked_at timestamptz NOT NULL,
  ended_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ask_history_task_idx ON ask_history(task_id, ended_at DESC);
