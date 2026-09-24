-- Mental models: saved questions about the user's world whose answers the memory keeps fresh (see memory/models.go).
CREATE TABLE memory_models (
  id           bigserial PRIMARY KEY,
  name         text NOT NULL UNIQUE,
  query        text NOT NULL,
  bank_id      bigint REFERENCES memory_banks(id) ON DELETE CASCADE,  -- NULL = every bank
  body         text NOT NULL DEFAULT '',
  sources      bigint[] NOT NULL DEFAULT '{}',
  refreshed_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);
