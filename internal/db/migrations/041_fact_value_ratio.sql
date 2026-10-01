-- value_ratio: how durable/reusable a fact is likely to be — a standing attribute ("user has equipment Z")
-- versus a one-off event ("we downloaded files X and Y") — distinct from confidence (how sure we are it's
-- true) and from rank (which only grows after repeated retrieval, so it says nothing about a brand-new
-- fact). Set at extraction time; a fact the user typed in directly is taken at their word as valuable.
-- Folded into Find's ranking weight alongside confidence — it does not affect pruning, which stays driven
-- by actual usage/staleness as before.
ALTER TABLE memory_facts ADD COLUMN value_ratio real NOT NULL DEFAULT 0.5;

-- carried through the pending-facts retry queue too, so a transient Store() failure doesn't lose the
-- extraction prompt's own value_ratio classification on retry.
ALTER TABLE memory_pending_facts ADD COLUMN value_ratio real NOT NULL DEFAULT 0.5;
