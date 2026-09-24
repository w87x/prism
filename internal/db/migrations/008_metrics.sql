-- Per-call records behind the Usage dashboard (kept ~35 days).
CREATE TABLE llm_calls (
  id         bigserial PRIMARY KEY,
  ts         timestamptz NOT NULL DEFAULT now(),
  model      text NOT NULL,
  agent      text NOT NULL DEFAULT '',      -- '' = not an agent run (memory digest, extraction…)
  tokens_in  int NOT NULL DEFAULT 0,
  tokens_out int NOT NULL DEFAULT 0,
  ms         int NOT NULL DEFAULT 0,        -- request to last token
  wait_ms    int NOT NULL DEFAULT 0,        -- time queued for a model slot first
  ok         boolean NOT NULL DEFAULT true,
  err        text NOT NULL DEFAULT ''
);
CREATE INDEX llm_calls_ts_idx ON llm_calls(ts);
CREATE TABLE tool_calls (
  id    bigserial PRIMARY KEY,
  ts    timestamptz NOT NULL DEFAULT now(),
  agent text NOT NULL DEFAULT '',
  tool  text NOT NULL,
  ms    int NOT NULL DEFAULT 0,
  ok    boolean NOT NULL DEFAULT true,
  err   text NOT NULL DEFAULT ''
);
CREATE INDEX tool_calls_ts_idx ON tool_calls(ts);
