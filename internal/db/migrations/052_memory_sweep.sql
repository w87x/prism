-- The near-duplicate sweep judges each pair of look-alike facts once; this remembers the pairs it has seen (and what it decided), so a pair
-- the model called "distinct" or the user undid is never asked about again.
CREATE TABLE IF NOT EXISTS memory_sweep_pairs (
	a        bigint NOT NULL REFERENCES memory_facts(id) ON DELETE CASCADE,
	b        bigint NOT NULL REFERENCES memory_facts(id) ON DELETE CASCADE,
	verdict  text NOT NULL,
	judged_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (a, b)
);
