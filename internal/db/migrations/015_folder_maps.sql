-- Folder maps: a one-line summary for every file and subfolder under an attached folder, so agents can find files by content.
CREATE TABLE folder_maps (
  id         bigserial PRIMARY KEY,
  root       text NOT NULL UNIQUE,                 -- absolute path
  status     text NOT NULL DEFAULT 'queued',       -- queued | running | done | failed
  total      int NOT NULL DEFAULT 0,               -- entries to summarise
  done       int NOT NULL DEFAULT 0,
  truncated  boolean NOT NULL DEFAULT false,       -- the folder had more entries than the cap
  error      text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE folder_map_entries (
  map_id  bigint NOT NULL REFERENCES folder_maps ON DELETE CASCADE,
  path    text NOT NULL,                           -- relative to the root, '' for the root itself
  kind    text NOT NULL,                           -- file | dir
  size    bigint NOT NULL DEFAULT 0,
  mtime   timestamptz,
  summary text NOT NULL DEFAULT '',
  PRIMARY KEY (map_id, path)
);
