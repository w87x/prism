-- when each synthesis level (2 = cross-bank synthesis, 3 = principles) last ran
CREATE TABLE IF NOT EXISTS memory_synth (
  level  int PRIMARY KEY,
  ran_at timestamptz NOT NULL DEFAULT now()
);
