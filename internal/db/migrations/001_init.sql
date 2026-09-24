-- PRISM schema v1

CREATE TABLE settings (
  key        text PRIMARY KEY,
  value      jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- ── LLM providers / keys / models ─────────────────────────────────────────
CREATE TABLE providers (
  id       bigserial PRIMARY KEY,
  name     text UNIQUE NOT NULL,
  kind     text NOT NULL,              -- openai | lmstudio | gemini | grok | openrouter | custom
  base_url text NOT NULL,
  enabled  boolean NOT NULL DEFAULT true
);
CREATE TABLE provider_keys (
  id             bigserial PRIMARY KEY,
  provider_id    bigint NOT NULL REFERENCES providers ON DELETE CASCADE,
  label          text NOT NULL DEFAULT '',
  api_key        text NOT NULL DEFAULT '',
  enabled        boolean NOT NULL DEFAULT true,
  priority       int NOT NULL DEFAULT 0,
  cooldown_until timestamptz,
  last_error     text NOT NULL DEFAULT '',
  uses           bigint NOT NULL DEFAULT 0
);
CREATE TABLE models (
  id             bigserial PRIMARY KEY,
  name           text UNIQUE NOT NULL,     -- friendly reference used by agents
  provider_id    bigint NOT NULL REFERENCES providers ON DELETE CASCADE,
  model_id       text NOT NULL,            -- id sent to the API
  kind           text NOT NULL DEFAULT 'chat',   -- chat | embedding
  context_window int NOT NULL DEFAULT 32768,
  supports_tools boolean NOT NULL DEFAULT true,
  temperature    real,
  max_output     int NOT NULL DEFAULT 0
);
CREATE TABLE model_lists (
  id     bigserial PRIMARY KEY,
  name   text UNIQUE NOT NULL,
  kind   text NOT NULL DEFAULT 'chat',
  models text[] NOT NULL DEFAULT '{}'      -- ordered fallback chain of model names
);

-- ── agents ────────────────────────────────────────────────────────────────
CREATE TABLE agent_profiles (
  id             bigserial PRIMARY KEY,
  name           text UNIQUE NOT NULL,
  grp            text NOT NULL DEFAULT 'General',
  description    text NOT NULL DEFAULT '',
  soul           text NOT NULL DEFAULT '',
  traits         text[] NOT NULL DEFAULT '{}',
  tools          text[] NOT NULL DEFAULT '{}',   -- explicit toolset (beyond the always-on base)
  skills         text[] NOT NULL DEFAULT '{}',
  banks          text[] NOT NULL DEFAULT '{}',   -- extra memory banks readable, "kind:name"
  model          text NOT NULL DEFAULT '',       -- model or list name; empty → default
  role           text NOT NULL DEFAULT 'worker', -- entry | maint | worker
  system         boolean NOT NULL DEFAULT false, -- well-known; cannot be deleted
  can_delegate   boolean NOT NULL DEFAULT false,
  auto_tools     boolean NOT NULL DEFAULT true,  -- let Sherpa pick extra tools per task
  max_iterations int NOT NULL DEFAULT 24,
  enabled        boolean NOT NULL DEFAULT true,
  soul_version   int NOT NULL DEFAULT 1,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE soul_history (
  id         bigserial PRIMARY KEY,
  profile_id bigint NOT NULL REFERENCES agent_profiles ON DELETE CASCADE,
  version    int NOT NULL,
  soul       text NOT NULL,
  reason     text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE evolution_proposals (
  id         bigserial PRIMARY KEY,
  profile_id bigint NOT NULL REFERENCES agent_profiles ON DELETE CASCADE,
  kind       text NOT NULL DEFAULT 'soul',   -- soul | skill | trait
  proposal   text NOT NULL,
  rationale  text NOT NULL DEFAULT '',
  status     text NOT NULL DEFAULT 'pending', -- pending | applied | rejected
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz
);

-- ── sessions (agent instances) & tasks ────────────────────────────────────
CREATE TABLE sessions (
  id         bigserial PRIMARY KEY,
  agent      text NOT NULL,
  kind       text NOT NULL DEFAULT 'task',   -- task | chat | cron
  key        text NOT NULL DEFAULT '',       -- chat sessions: web | tg:dm | tg:topic:<id>
  task_id    bigint,
  scratchpad text NOT NULL DEFAULT '',
  status     text NOT NULL DEFAULT 'idle',
  tokens_in  bigint NOT NULL DEFAULT 0,
  tokens_out bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_key_idx ON sessions(kind, key);
CREATE TABLE session_messages (
  id           bigserial PRIMARY KEY,
  session_id   bigint NOT NULL REFERENCES sessions ON DELETE CASCADE,
  role         text NOT NULL,
  content      text NOT NULL DEFAULT '',
  tool_calls   jsonb,
  tool_call_id text NOT NULL DEFAULT '',
  name         text NOT NULL DEFAULT '',
  provenance   text NOT NULL DEFAULT 'agent',  -- user | agent | tool | web | mcp | memory | system
  tainted      boolean NOT NULL DEFAULT false, -- untrusted content in scope
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX session_messages_idx ON session_messages(session_id, id);

CREATE TABLE tasks (
  id          bigserial PRIMARY KEY,
  parent_id   bigint REFERENCES tasks ON DELETE SET NULL,
  root_id     bigint,
  from_kind   text NOT NULL,                  -- user | agent | cron | intent | telegram | system
  from_name   text NOT NULL DEFAULT '',
  to_agent    text NOT NULL,
  title       text NOT NULL DEFAULT '',
  input       text NOT NULL,
  status      text NOT NULL DEFAULT 'queued', -- queued | running | waiting_input | done | failed | cancelled
  result      text NOT NULL DEFAULT '',
  error       text NOT NULL DEFAULT '',
  question    text NOT NULL DEFAULT '',
  depth       int NOT NULL DEFAULT 0,
  priority    int NOT NULL DEFAULT 0,
  session_id  bigint,
  tokens_in   bigint NOT NULL DEFAULT 0,
  tokens_out  bigint NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now(),
  started_at  timestamptz,
  finished_at timestamptz
);
CREATE INDEX tasks_status_idx ON tasks(status, priority DESC, id);

-- user-visible conversation log (per channel)
CREATE TABLE chat_messages (
  id         bigserial PRIMARY KEY,
  role       text NOT NULL,                   -- user | agent | system
  agent      text NOT NULL DEFAULT '',
  text       text NOT NULL,
  channel    text NOT NULL DEFAULT 'web',     -- web | telegram
  topic      text NOT NULL DEFAULT '',
  task_id    bigint,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chat_messages_idx ON chat_messages(channel, topic, id);

-- ── memory ────────────────────────────────────────────────────────────────
CREATE TABLE memory_banks (
  id          bigserial PRIMARY KEY,
  kind        text NOT NULL,                  -- profile | project | domain | user
  name        text NOT NULL,
  owner       text NOT NULL DEFAULT '',       -- agent name for profile banks
  description text NOT NULL DEFAULT '',
  status      text NOT NULL DEFAULT 'active',
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (kind, name, owner)
);
CREATE TABLE memory_facts (
  id            bigserial PRIMARY KEY,
  bank_id       bigint NOT NULL REFERENCES memory_banks ON DELETE CASCADE,
  text          text NOT NULL,
  tags          text[] NOT NULL DEFAULT '{}',
  embedding     bytea,
  rank          real NOT NULL DEFAULT 1,
  hits          int NOT NULL DEFAULT 0,
  confidence    real NOT NULL DEFAULT 0.7,
  source        text NOT NULL DEFAULT '',
  supersedes    bigint,
  superseded_by bigint,
  valid_from    timestamptz NOT NULL DEFAULT now(),
  valid_to      timestamptz,
  last_used     timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  tsv           tsvector GENERATED ALWAYS AS (to_tsvector('simple', text)) STORED
);
CREATE INDEX memory_facts_bank_idx ON memory_facts(bank_id) WHERE valid_to IS NULL;
CREATE INDEX memory_facts_tsv_idx ON memory_facts USING gin(tsv);
CREATE TABLE memory_raw (
  id         bigserial PRIMARY KEY,
  from_name  text NOT NULL,
  to_name    text NOT NULL,
  channel    text NOT NULL DEFAULT '',
  topic      text NOT NULL DEFAULT '',
  text       text NOT NULL,
  task_id    bigint,
  processed  boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX memory_raw_idx ON memory_raw(processed, id);

-- ── tools, MCP, skills ────────────────────────────────────────────────────
CREATE TABLE tool_settings (
  name    text PRIMARY KEY,
  enabled boolean NOT NULL DEFAULT true,
  armed   boolean            -- NULL → default for the tool's risk class
);
CREATE TABLE mcp_servers (
  id        bigserial PRIMARY KEY,
  name      text UNIQUE NOT NULL,
  transport text NOT NULL DEFAULT 'stdio',   -- stdio | http
  command   text NOT NULL DEFAULT '',
  args      text[] NOT NULL DEFAULT '{}',
  env       jsonb NOT NULL DEFAULT '{}',
  url       text NOT NULL DEFAULT '',
  headers   jsonb NOT NULL DEFAULT '{}',
  enabled   boolean NOT NULL DEFAULT true,
  armed     boolean NOT NULL DEFAULT false
);
CREATE TABLE skills (
  id          bigserial PRIMARY KEY,
  name        text UNIQUE NOT NULL,
  description text NOT NULL DEFAULT '',
  body        text NOT NULL DEFAULT '',
  source      text NOT NULL DEFAULT 'local',  -- local | hub:<name>
  adapted     boolean NOT NULL DEFAULT false,
  enabled     boolean NOT NULL DEFAULT true,
  dir         text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE skill_hubs (
  id      bigserial PRIMARY KEY,
  name    text UNIQUE NOT NULL,
  repo    text NOT NULL,                      -- github "owner/repo" or https URL of a git tree
  path    text NOT NULL DEFAULT '',
  ref     text NOT NULL DEFAULT '',
  enabled boolean NOT NULL DEFAULT true
);

-- ── autonomy ──────────────────────────────────────────────────────────────
CREATE TABLE crons (
  id       bigserial PRIMARY KEY,
  name     text NOT NULL,
  agent    text NOT NULL,
  expr     text NOT NULL,
  prompt   text NOT NULL,
  enabled  boolean NOT NULL DEFAULT true,
  system   boolean NOT NULL DEFAULT false,
  last_run timestamptz,
  next_run timestamptz
);
CREATE TABLE intents (
  id         bigserial PRIMARY KEY,
  type       text NOT NULL DEFAULT 'intent',  -- intent | watch
  owner      text NOT NULL,                   -- agent profile to wake
  description text NOT NULL,
  predicate  jsonb NOT NULL,                  -- {kind, ...params}
  cadence_s  int NOT NULL DEFAULT 300,
  repeat     boolean NOT NULL DEFAULT false,
  status     text NOT NULL DEFAULT 'active',  -- active | fired | cancelled | error
  progress   text NOT NULL DEFAULT '',
  last_check timestamptz,
  next_due   timestamptz NOT NULL DEFAULT now(),
  last_error text NOT NULL DEFAULT '',
  notify     boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  fired_at   timestamptz
);
CREATE TABLE briefings (
  id         bigserial PRIMARY KEY,
  agent      text NOT NULL,
  title      text NOT NULL,
  body       text NOT NULL,
  importance int NOT NULL DEFAULT 1,
  status     text NOT NULL DEFAULT 'new',     -- new | delivered | dismissed
  created_at timestamptz NOT NULL DEFAULT now()
);

-- ── user data ─────────────────────────────────────────────────────────────
CREATE TABLE bookmarks (
  id          bigserial PRIMARY KEY,
  url         text UNIQUE NOT NULL,
  title       text NOT NULL DEFAULT '',
  description text NOT NULL DEFAULT '',
  keywords    text[] NOT NULL DEFAULT '{}',
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE artifacts (
  id         bigserial PRIMARY KEY,
  name       text NOT NULL,
  mime       text NOT NULL DEFAULT 'text/plain',
  path       text NOT NULL,
  size       bigint NOT NULL DEFAULT 0,
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE downloads (
  id         bigserial PRIMARY KEY,
  url        text NOT NULL,
  dest       text NOT NULL,
  status     text NOT NULL DEFAULT 'queued', -- queued | running | done | failed | cancelled
  bytes      bigint NOT NULL DEFAULT 0,
  total      bigint NOT NULL DEFAULT 0,
  error      text NOT NULL DEFAULT '',
  owner      text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE TABLE doc_sources (
  id      bigserial PRIMARY KEY,
  name    text UNIQUE NOT NULL,
  path    text NOT NULL,
  globs   text[] NOT NULL DEFAULT '{*.md,*.txt}',
  indexed_at timestamptz
);
CREATE TABLE doc_chunks (
  id        bigserial PRIMARY KEY,
  source_id bigint NOT NULL REFERENCES doc_sources ON DELETE CASCADE,
  file      text NOT NULL,
  mtime     timestamptz NOT NULL,
  ord       int NOT NULL,
  text      text NOT NULL,
  embedding bytea,
  tsv       tsvector GENERATED ALWAYS AS (to_tsvector('simple', text)) STORED
);
CREATE INDEX doc_chunks_tsv_idx ON doc_chunks USING gin(tsv);
CREATE INDEX doc_chunks_file_idx ON doc_chunks(source_id, file);

-- ── integrations ──────────────────────────────────────────────────────────
CREATE TABLE telegram_topics (
  id         bigserial PRIMARY KEY,
  chat_id    bigint NOT NULL,
  thread_id  bigint NOT NULL,
  name       text NOT NULL,
  purpose    text NOT NULL DEFAULT '',
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (chat_id, thread_id),
  UNIQUE (chat_id, name)
);

CREATE TABLE logs (
  id      bigserial PRIMARY KEY,
  ts      timestamptz NOT NULL DEFAULT now(),
  level   text NOT NULL DEFAULT 'info',
  source  text NOT NULL DEFAULT '',
  message text NOT NULL
);
CREATE INDEX logs_ts_idx ON logs(id DESC);
