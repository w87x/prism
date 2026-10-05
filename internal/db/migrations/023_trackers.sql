-- Structured trackers: user-defined tables (e.g. "Apartments", "Hardware shortlist") that agents populate
-- with evidence-backed rows over time, diffing every update against what was there before so a refresh can
-- report exactly what changed — a job neither a memory fact (one sentence) nor a bookmark (one link) fits.
CREATE TABLE trackers (
  id          bigserial PRIMARY KEY,
  name        text UNIQUE NOT NULL,
  description text NOT NULL DEFAULT '',
  columns     jsonb NOT NULL DEFAULT '[]', -- [{"name":"Price","type":"number","required":true,"unit":"USD","description":"..."}]; type is optional (absent = free-form)
  conditions  jsonb NOT NULL DEFAULT '[]', -- alerts: [{"name":"cheap","field":"Price","op":"lte","value":2500,"notify":true}]
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
  -- active | missing (absent from a COMPLETE snapshot — "not observed", never "ceased to exist") | gone (retired on purpose)
  status     text NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  last_seen  timestamptz NOT NULL DEFAULT now(),
  missing_since timestamptz,
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
  -- added | changed | missing | reappeared | retired | condition_met | condition_cleared
  kind       text NOT NULL DEFAULT 'changed',
  run_id     text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracker_changes_tracker_idx ON tracker_changes(tracker_id, id DESC);
-- One row per refresh: the idempotency record. The same run_id with the same snapshot returns this receipt; the same
-- run_id with a different snapshot is refused, so a retried refresh can never double-apply.
CREATE TABLE tracker_runs (
  id          bigserial PRIMARY KEY,
  tracker_id  bigint NOT NULL REFERENCES trackers ON DELETE CASCADE,
  run_id      text NOT NULL,
  fingerprint text NOT NULL,
  complete    boolean NOT NULL,
  receipt     jsonb NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tracker_id, run_id)
);
-- Where each alert condition stood for a row, so only a real transition (false→true, true→false) fires.
CREATE TABLE tracker_condition_state (
  row_id     bigint NOT NULL REFERENCES tracker_rows ON DELETE CASCADE,
  name       text NOT NULL,
  result     text NOT NULL CHECK (result IN ('true','false')),
  changed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (row_id, name)
);
