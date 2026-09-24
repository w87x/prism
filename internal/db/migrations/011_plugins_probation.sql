-- Runtime plugins: small Python tools an agent writes and the user approves.
CREATE TABLE plugins (
  id          bigserial PRIMARY KEY,
  name        text UNIQUE NOT NULL,           -- the tool is called plugin_<name>
  description text NOT NULL,
  params      jsonb NOT NULL DEFAULT '{}'::jsonb,   -- JSON schema of the arguments
  code        text NOT NULL,
  hash        text NOT NULL,                  -- sha256 of code as approved: a changed row never runs
  status      text NOT NULL DEFAULT 'pending', -- pending | approved | disabled
  network     boolean NOT NULL DEFAULT false,
  timeout_s   int NOT NULL DEFAULT 30,
  created_by  text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  approved_at timestamptz
);

-- Agents hired by other agents start on probation: they work, but with no exec tools, until the user confirms.
ALTER TABLE agent_profiles ADD COLUMN probation boolean NOT NULL DEFAULT false;
