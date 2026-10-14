-- Team practices: how PRISM works like a team with a lead.
--  task_meta   acceptance criteria a delegator set for a task ("done means…"), whether the lead's check found them met, and the
--              user's thumbs up/down on the result
--  runbooks    a verified procedure written by the main model from successful runs of a routine job, followed by the cheap model
--  decisions   what was decided, why, and when to look at it again
--  review_log  what the reviewer approved, denied or passed to the user instead of asking the user
--  incidents   periods when no model could be reached (background work was paused)
--  standups    the weekly "what the team did" digests
CREATE TABLE IF NOT EXISTS task_meta (
  task_id      bigint PRIMARY KEY,
  criteria     text NOT NULL DEFAULT '',
  criteria_met boolean,
  criteria_gap text NOT NULL DEFAULT '',
  rating       smallint NOT NULL DEFAULT 0,
  rating_note  text NOT NULL DEFAULT '',
  rated_at     timestamptz,
  audit        text NOT NULL DEFAULT '',              -- '' (not audited) | ok | problem: a sampled re-check of a routine run by the main model
  audit_note   text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS task_meta_rated_idx ON task_meta (rated_at) WHERE rating <> 0;

CREATE TABLE IF NOT EXISTS runbooks (
  id          bigserial PRIMARY KEY,
  key         text NOT NULL UNIQUE,                  -- <kind>:<job name>, e.g. cron:Price check
  agent       text NOT NULL DEFAULT '',
  title       text NOT NULL DEFAULT '',
  steps       text NOT NULL DEFAULT '',
  status      text NOT NULL DEFAULT 'learning' CHECK (status IN ('learning','active','stale','off')),
  version     int NOT NULL DEFAULT 0,
  learn_wins  int NOT NULL DEFAULT 0,                -- successful main-model runs seen while learning
  wins        int NOT NULL DEFAULT 0,                -- successful runs that followed the runbook
  losses      int NOT NULL DEFAULT 0,
  fail_streak int NOT NULL DEFAULT 0,
  last_note   text NOT NULL DEFAULT '',              -- why it went stale
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS decisions (
  id         bigserial PRIMARY KEY,
  title      text NOT NULL,
  decision   text NOT NULL,
  rationale  text NOT NULL DEFAULT '',
  by_agent   text NOT NULL DEFAULT '',
  task_id    bigint,
  status     text NOT NULL DEFAULT 'active' CHECK (status IN ('active','superseded','closed')),
  supersedes bigint,
  made_at    timestamptz NOT NULL DEFAULT now(),
  review_at  timestamptz
);
CREATE INDEX IF NOT EXISTS decisions_status_idx ON decisions (status, review_at);

CREATE TABLE IF NOT EXISTS review_log (
  id       bigserial PRIMARY KEY,
  at       timestamptz NOT NULL DEFAULT now(),
  agent    text NOT NULL,
  tool     text NOT NULL,
  args     text NOT NULL DEFAULT '',
  verdict  text NOT NULL CHECK (verdict IN ('approve','deny','ask')),
  reason   text NOT NULL DEFAULT '',
  task_id  bigint
);
CREATE INDEX IF NOT EXISTS review_log_at_idx ON review_log (at DESC);

CREATE TABLE IF NOT EXISTS incidents (
  id         bigserial PRIMARY KEY,
  started_at timestamptz NOT NULL DEFAULT now(),
  ended_at   timestamptz,
  reason     text NOT NULL DEFAULT '',
  skipped    int NOT NULL DEFAULT 0                   -- background jobs not started because of it
);

CREATE TABLE IF NOT EXISTS standups (
  id         bigserial PRIMARY KEY,
  since      timestamptz NOT NULL,
  until      timestamptz NOT NULL,
  text       text NOT NULL,
  stats      jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
