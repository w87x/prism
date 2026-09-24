-- A partial/failed task that needs a human decision stays in "Needs your attention" until it is
-- acknowledged (dismissed, or resolved by continuing/evolving/…); this records when.
ALTER TABLE tasks ADD COLUMN acknowledged_at timestamptz;
