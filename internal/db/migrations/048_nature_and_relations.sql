-- More team practices, borrowed from animal groups and from how people work together.
--  trails      ant trails: what worked leaves a trace that fades; routing prefers fresh traces
--  antibodies  immune memory: a short rule, injected only when its trigger shows up again; unused ones expire
--  trust       how far an agent's calls to a tool have earned going through without asking
--  repairs     apologise-and-repair: what went wrong, what was changed, and whether it held
--  handover    a short note written when a task stops, so whoever continues does not start from raw transcripts
CREATE TABLE IF NOT EXISTS trails (
  agent      text NOT NULL,
  kind       text NOT NULL,                    -- '*' for the agent overall, otherwise a tool name
  weight     real NOT NULL DEFAULT 0,          -- as of updated_at; decays with a half-life (see agent/trails.go)
  wins       int NOT NULL DEFAULT 0,
  losses     int NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (agent, kind)
);

CREATE TABLE IF NOT EXISTS antibodies (
  id         bigserial PRIMARY KEY,
  agent      text NOT NULL DEFAULT '*',        -- '*' = every agent
  triggers   text[] NOT NULL DEFAULT '{}',     -- lower-case words or tool names; any one in the task or toolset fires it
  rule       text NOT NULL,
  source     text NOT NULL DEFAULT '',
  hits       int NOT NULL DEFAULT 0,
  last_hit   timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (agent, rule)
);

CREATE TABLE IF NOT EXISTS trust (
  agent      text NOT NULL,
  tool       text NOT NULL,
  wins       int NOT NULL DEFAULT 0,
  losses     int NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (agent, tool)
);

CREATE TABLE IF NOT EXISTS repairs (
  id         bigserial PRIMARY KEY,
  task_id    bigint NOT NULL,
  agent      text NOT NULL,
  what       text NOT NULL DEFAULT '',          -- the user's complaint
  status     text NOT NULL DEFAULT 'open' CHECK (status IN ('open','held','failed')),
  repair_task bigint,
  due_at     timestamptz NOT NULL,
  checked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS repairs_due_idx ON repairs (due_at) WHERE status = 'open';

ALTER TABLE task_meta ADD COLUMN IF NOT EXISTS handover text NOT NULL DEFAULT '';
