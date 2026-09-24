-- Fact provenance and straightforward fact actions (code-review "why do you remember this?" item):
--
-- task_id: the task/conversation a fact was stored during or extracted from, when known — a durable,
-- clickable link back to "where did this come from" that survives long after memory_raw's own rows (which
-- carried task_id already) are deleted once distilled. Nullable: older facts, and facts stored outside any
-- task (e.g. a raw batch spanning messages from several tasks), simply have none.
ALTER TABLE memory_facts ADD COLUMN task_id bigint;

-- pinned: exempt from Prune's auto-archival and from rank's time-decay in retrieval scoring. The user's own
-- "this matters, always surface it" — distinct from rank, which reflects earned usage, not a standing say.
ALTER TABLE memory_facts ADD COLUMN pinned boolean NOT NULL DEFAULT false;
