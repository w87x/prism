-- A scheduled job that has done what it was for ("the movie is out and downloaded") is finished, not deleted: it stays in the list
-- with the reason, switched off, and can be switched on again.
ALTER TABLE crons ADD COLUMN IF NOT EXISTS finished_at timestamptz;
ALTER TABLE crons ADD COLUMN IF NOT EXISTS finished_note text NOT NULL DEFAULT '';
