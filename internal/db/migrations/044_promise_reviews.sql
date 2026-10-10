-- Promise review: after an autonomous run (cron, standing intent) a reviewer checks whether what the agent said it did, or
-- would do ("I looked for the movie; if it is out I will download it"), actually happened. What did not is recorded here.
CREATE TABLE IF NOT EXISTS promise_reviews (
  id          bigserial PRIMARY KEY,
  task_id     bigint NOT NULL,
  agent       text NOT NULL,
  title       text NOT NULL DEFAULT '',
  promises    jsonb NOT NULL DEFAULT '[]',       -- [{text, status, evidence, fix}]
  status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open','fixing','dismissed')),
  fix_task_id bigint,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS promise_reviews_open_idx ON promise_reviews (status) WHERE status = 'open';
