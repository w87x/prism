-- Entities: named things (people, organisations, products, places, events, concepts) pulled out of memory
-- facts, plus typed relations between them — a second, coarser graph layered on top of the fact graph so
-- "what do I know about AMD AI 395" or "who does the user report to" can be answered without re-reading
-- every fact that happens to mention it. Extraction (internal/memory/entities.go) mirrors Reflect: an LLM
-- pass over a bank's facts, watermarked so it only looks at what is new.
CREATE TABLE memory_entities (
  id          bigserial PRIMARY KEY,
  bank_id     bigint NOT NULL REFERENCES memory_banks ON DELETE CASCADE,
  kind        text NOT NULL DEFAULT 'entity', -- person | organization | product | place | event | concept | entity (unclassified)
  name        text NOT NULL,
  aliases     text[] NOT NULL DEFAULT '{}',
  mentions    int NOT NULL DEFAULT 0,
  first_seen  timestamptz NOT NULL DEFAULT now(),
  last_seen   timestamptz NOT NULL DEFAULT now(),
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (bank_id, name)
);
CREATE INDEX memory_entities_bank_idx ON memory_entities(bank_id);

-- which facts an entity was extracted from ("why do you know this")
CREATE TABLE memory_entity_mentions (
  entity_id  bigint NOT NULL REFERENCES memory_entities ON DELETE CASCADE,
  fact_id    bigint NOT NULL REFERENCES memory_facts ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (entity_id, fact_id)
);
CREATE INDEX memory_entity_mentions_fact_idx ON memory_entity_mentions(fact_id);

-- typed relations between two entities (undirected storage like memory_links: a < b, one relation per pair)
CREATE TABLE memory_entity_links (
  a          bigint NOT NULL REFERENCES memory_entities ON DELETE CASCADE,
  b          bigint NOT NULL REFERENCES memory_entities ON DELETE CASCADE,
  kind       text NOT NULL DEFAULT 'related', -- temporal | semantic | related
  label      text NOT NULL DEFAULT '',        -- human-readable, e.g. "works at", "released on"
  weight     real NOT NULL DEFAULT 0.6,
  source     text NOT NULL DEFAULT 'auto',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (a, b),
  CHECK (a < b)
);
CREATE INDEX memory_entity_links_b_idx ON memory_entity_links(b);

-- watermark, same idea as memory_banks.reflected_at
ALTER TABLE memory_banks ADD COLUMN entities_at timestamptz;
