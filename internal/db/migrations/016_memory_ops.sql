-- Undo log for bank merges and splits.
CREATE TABLE memory_ops (
  id         bigserial PRIMARY KEY,
  kind       text NOT NULL,            -- merge | split
  summary    text NOT NULL,
  payload    jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  undone_at  timestamptz
);
