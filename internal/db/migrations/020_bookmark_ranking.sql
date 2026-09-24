-- Bookmark ranking: an agent-added bookmark starts at rank 1; being returned by bookmark_find nudges it up
-- (rank *= 1.01) so bookmarks that actually prove useful surface more readily; one not returned on a given
-- day decays (rank *= 0.99, applied at most once per calendar day — see last_decay); a bookmark that decays
-- past a negligible rank is deleted outright rather than lingering forever. A user-added bookmark starts
-- above everything the agents have accumulated (see TopmostBookmarkRank) — it was worth remembering on
-- purpose, not surfaced by a query match, so it should not have to earn its way up from the agent baseline.
ALTER TABLE bookmarks ADD COLUMN rank real NOT NULL DEFAULT 1;
ALTER TABLE bookmarks ADD COLUMN last_used timestamptz;
-- last_decay: the calendar day decay was last applied to this row, so an hourly maintenance tick can call
-- the decay pass freely without over-decaying — the row itself remembers whether today already happened.
ALTER TABLE bookmarks ADD COLUMN last_decay date;
