-- A job proposal can turn a group of "check X / check Y / check Z" jobs into one tracker (one row per item) plus a single refresh job.
--   plan          the tracker to create: name, description, columns and the starting rows
--   new_agent     the agent that should run the refresh job (it must hold the tracker tools), when that differs from the kept job's
--   tracker_name  the tracker that applying the proposal created (kept when the proposal is undone: it may hold data by then)
ALTER TABLE job_proposals DROP CONSTRAINT IF EXISTS job_proposals_kind_check;
ALTER TABLE job_proposals ADD CONSTRAINT job_proposals_kind_check CHECK (kind IN ('merge','rewrite','tracker'));
ALTER TABLE job_proposals ADD COLUMN IF NOT EXISTS plan jsonb NOT NULL DEFAULT '{}';
ALTER TABLE job_proposals ADD COLUMN IF NOT EXISTS new_agent text NOT NULL DEFAULT '';
ALTER TABLE job_proposals ADD COLUMN IF NOT EXISTS tracker_name text NOT NULL DEFAULT '';
