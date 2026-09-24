-- OAuth state of an MCP server that requires sign-in (client registration, tokens). Never sent to the UI.
ALTER TABLE mcp_servers ADD COLUMN oauth jsonb NOT NULL DEFAULT '{}'::jsonb;
