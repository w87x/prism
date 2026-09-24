-- Monitors: short-lived, frequent watches. expires_at ends them on their own; samples keeps recent
-- (time, fraction-done) points so the scheduler can estimate a finish time; announce makes the watch report
-- progress changes to the user while it runs, not only when it completes.
ALTER TABLE intents ADD COLUMN expires_at timestamptz;
ALTER TABLE intents ADD COLUMN samples jsonb NOT NULL DEFAULT '[]';
ALTER TABLE intents ADD COLUMN announce boolean NOT NULL DEFAULT false;
