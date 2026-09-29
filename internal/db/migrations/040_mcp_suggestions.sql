-- A drafted agent proposed for a newly connected MCP server, pending the user's review.
CREATE TABLE mcp_agent_suggestions (
  id         bigserial PRIMARY KEY,
  server     text NOT NULL,
  draft      jsonb NOT NULL,
  status     text NOT NULL DEFAULT 'pending', -- pending | applied | dismissed
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mcp_agent_suggestions_status_idx ON mcp_agent_suggestions(status, id DESC);
