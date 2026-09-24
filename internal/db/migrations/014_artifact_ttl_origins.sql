-- Ephemeral artifacts: hand-offs between agents that expire on their own.
ALTER TABLE artifacts ADD COLUMN expires_at timestamptz;                          -- NULL = kept until deleted
ALTER TABLE artifacts ADD COLUMN tainted    boolean NOT NULL DEFAULT false;       -- written while untrusted content was in scope
ALTER TABLE artifacts ADD COLUMN session_id bigint;                               -- the agent session that wrote it
CREATE INDEX artifacts_expires_idx ON artifacts(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX artifacts_session_idx ON artifacts(session_id) WHERE session_id IS NOT NULL;

-- Where a fact learned from the web came from (registrable domains); the same fact from independent sources is trusted more.
ALTER TABLE memory_facts ADD COLUMN origins text[] NOT NULL DEFAULT '{}';
