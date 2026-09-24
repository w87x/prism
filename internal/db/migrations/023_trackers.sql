-- Structured trackers: user-defined tables (e.g. "Apartments", "Hardware shortlist") that agents populate
-- with evidence-backed rows over time, diffing every update against what was there before so a refresh can
-- report exactly what changed — a job neither a memory fact (one sentence) nor a bookmark (one link) fits.
CREATE TABLE trackers (
  id          bigserial PRIMARY KEY,
  name        text UNIQUE NOT NULL,
  description text NOT NULL DEFAULT '',
  columns     jsonb NOT NULL DEFAULT '[]', -- [{"name":"Price","description":"..."}]
  created_by  text NOT NULL DEFAULT '',
  status      text NOT NULL DEFAULT 'active', -- active | archived
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE tracker_rows (
  id         bigserial PRIMARY KEY,
  tracker_id bigint NOT NULL REFERENCES trackers ON DELETE CASCADE,
  key        text NOT NULL, -- a stable id the populating agent chooses (e.g. a listing URL)
  data       jsonb NOT NULL DEFAULT '{}',
  source_url text NOT NULL DEFAULT '',
  status     text NOT NULL DEFAULT 'active', -- active | gone (retired, e.g. a listing taken down)
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tracker_id, key)
);
CREATE INDEX tracker_rows_tracker_idx ON tracker_rows(tracker_id, updated_at DESC);
CREATE TABLE tracker_changes (
  id         bigserial PRIMARY KEY,
  tracker_id bigint NOT NULL REFERENCES trackers ON DELETE CASCADE,
  row_id     bigint NOT NULL REFERENCES tracker_rows ON DELETE CASCADE,
  row_key    text NOT NULL DEFAULT '',
  field      text NOT NULL,
  old_value  text NOT NULL DEFAULT '',
  new_value  text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracker_changes_tracker_idx ON tracker_changes(tracker_id, id DESC);
