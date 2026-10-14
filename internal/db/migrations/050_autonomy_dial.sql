-- The autonomy dial: how far PRISM may change itself without asking.
--  evolution_watch  an evolution that was applied automatically is watched; if the agent's results get clearly worse it is rolled back
--  job_proposals    proposals to merge near-duplicate scheduled jobs or rewrite one that keeps failing, from the evidence of its runs
--  cron_history     what a scheduled job looked like before each change, so any change can be undone
CREATE TABLE IF NOT EXISTS evolution_watch (
  proposal_id  bigint PRIMARY KEY,
  profile_id   bigint NOT NULL,
  agent        text NOT NULL,
  kind         text NOT NULL,
  applied_at   timestamptz NOT NULL DEFAULT now(),
  prev         jsonb NOT NULL DEFAULT '{}',        -- {soul, tools, traits} as they were
  before_done  int NOT NULL DEFAULT 0,
  before_total int NOT NULL DEFAULT 0,
  after_done   int NOT NULL DEFAULT 0,
  after_total  int NOT NULL DEFAULT 0,
  status       text NOT NULL DEFAULT 'watching' CHECK (status IN ('watching','kept','rolled_back','undone')),
  note         text NOT NULL DEFAULT '',
  decided_at   timestamptz
);

CREATE TABLE IF NOT EXISTS job_proposals (
  id         bigserial PRIMARY KEY,
  kind       text NOT NULL CHECK (kind IN ('merge','rewrite')),
  cron_ids   bigint[] NOT NULL,
  title      text NOT NULL DEFAULT '',
  new_prompt text NOT NULL,
  new_expr   text NOT NULL DEFAULT '',
  rationale  text NOT NULL DEFAULT '',
  evidence   text NOT NULL DEFAULT '',
  status     text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','applied','dismissed','undone')),
  auto       boolean NOT NULL DEFAULT false,
  undo_ids   bigint[] NOT NULL DEFAULT '{}',            -- cron_history rows that restore what applying it changed
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz
);
CREATE INDEX IF NOT EXISTS job_proposals_pending_idx ON job_proposals (status) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS cron_history (
  id      bigserial PRIMARY KEY,
  cron_id bigint NOT NULL,
  name    text NOT NULL,
  agent   text NOT NULL,
  expr    text NOT NULL,
  prompt  text NOT NULL,
  enabled boolean NOT NULL,
  reason  text NOT NULL DEFAULT '',
  at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS cron_history_idx ON cron_history (cron_id, id DESC);
