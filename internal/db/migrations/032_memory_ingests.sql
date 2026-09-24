-- Documents being (or already) taught to memory (see ingest/).
CREATE TABLE memory_ingests (
  id          bigserial PRIMARY KEY,
  name        text NOT NULL,
  path        text NOT NULL,
  kind        text NOT NULL DEFAULT '',
  title       text NOT NULL DEFAULT '',
  me          text NOT NULL DEFAULT '',
  since       timestamptz,
  status      text NOT NULL DEFAULT 'running',   -- running | done | failed | cancelled | interrupted
  total       int NOT NULL DEFAULT 0,
  done        int NOT NULL DEFAULT 0,
  facts       int NOT NULL DEFAULT 0,
  error       text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
