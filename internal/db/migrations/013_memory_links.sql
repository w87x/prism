-- Links between memory facts (associations across and within banks) and the agent a raw message teaches.
CREATE TABLE memory_links (
  a          bigint NOT NULL REFERENCES memory_facts ON DELETE CASCADE,
  b          bigint NOT NULL REFERENCES memory_facts ON DELETE CASCADE,
  kind       text NOT NULL DEFAULT 'related',      -- related | supports | contradicts | evidence (conclusion ↔ the fact it rests on)
  weight     real NOT NULL DEFAULT 0.5,            -- 0..1; strengthened when both facts are retrieved together
  note       text NOT NULL DEFAULT '',
  source     text NOT NULL DEFAULT 'auto',         -- auto | agent | user
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (a, b),
  CHECK (a < b)
);
CREATE INDEX memory_links_b_idx ON memory_links(b);

-- the agent whose profile bank a raw message may teach (empty: guess from the participants)
ALTER TABLE memory_raw ADD COLUMN agent text NOT NULL DEFAULT '';

-- conclusions: derived beliefs synthesised from several facts (evidence links), revised as evidence changes
ALTER TABLE memory_facts ADD COLUMN kind text NOT NULL DEFAULT 'fact';   -- fact | conclusion
ALTER TABLE memory_banks ADD COLUMN reflected_at timestamptz;
