-- Valid-time on entity relations: an edge like "works at" is true for a stretch of time, not forever. When
-- a new relation supersedes an old one (the user changes jobs: same source entity, same normalised label,
-- a different target), the old edge is closed off (valid_to/invalidated_by) and archived rather than
-- silently overwritten, so "who did the user work for before" stays answerable. Mirrors how memory_facts
-- already carries valid_from/valid_to/supersedes for the fact graph.
ALTER TABLE memory_entity_links ADD COLUMN valid_from    timestamptz NOT NULL DEFAULT now();
ALTER TABLE memory_entity_links ADD COLUMN valid_to      timestamptz;
ALTER TABLE memory_entity_links ADD COLUMN invalidated_by text NOT NULL DEFAULT ''; -- '' | 'replaced' | 'contradicted'
CREATE INDEX memory_entity_links_open_idx ON memory_entity_links(a, b) WHERE valid_to IS NULL;

-- Archive of relation states an edge passed through before being replaced or closed. The live edge always
-- stays in memory_entity_links (so a<b, one row per pair keeps holding); history rows are read-only.
CREATE TABLE memory_entity_link_history (
  id             bigserial PRIMARY KEY,
  a              bigint NOT NULL REFERENCES memory_entities ON DELETE CASCADE,
  b              bigint NOT NULL REFERENCES memory_entities ON DELETE CASCADE,
  kind           text NOT NULL,
  label          text NOT NULL DEFAULT '',
  weight         real NOT NULL DEFAULT 0.5,
  source         text NOT NULL DEFAULT 'auto',
  valid_from     timestamptz NOT NULL,
  valid_to       timestamptz NOT NULL,
  invalidated_by text NOT NULL DEFAULT 'replaced',
  archived_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX memory_entity_link_history_pair_idx ON memory_entity_link_history(a, b);
