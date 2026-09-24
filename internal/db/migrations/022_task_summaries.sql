-- Task-summary memory subsystem: after a task finishes, its goal, decisions, attempts, outcome and
-- unfinished work are distilled into one searchable row, linked back to the task (and from there to its
-- full transcript via task_transcript) — closing the "what did we try last week, and why did we reject it?"
-- gap that atomic memory facts (single sentences) cannot reliably answer.
CREATE TABLE task_summaries (
  id         bigserial PRIMARY KEY,
  task_id    bigint NOT NULL UNIQUE REFERENCES tasks ON DELETE CASCADE,
  agent      text NOT NULL DEFAULT '',
  title      text NOT NULL DEFAULT '',
  status     text NOT NULL DEFAULT '',
  goal       text NOT NULL DEFAULT '',
  decisions  text NOT NULL DEFAULT '',
  attempts   text NOT NULL DEFAULT '',
  outcome    text NOT NULL DEFAULT '',
  unfinished text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);

-- summarized_at: when a task's summarization attempt was resolved (stored, skipped as trivial, or given up
-- on) — NULL means still pending. summary_attempts caps retries so a task whose transcript permanently
-- fails to summarize (e.g. unparsable model output) cannot block the queue forever (see maxSummaryAttempts).
ALTER TABLE tasks ADD COLUMN summarized_at timestamptz;
ALTER TABLE tasks ADD COLUMN summary_attempts int NOT NULL DEFAULT 0;
CREATE INDEX tasks_needs_summary_idx ON tasks(id) WHERE summarized_at IS NULL AND finished_at IS NOT NULL;
