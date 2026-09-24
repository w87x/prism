-- agent icons become Font Awesome names, assigned automatically
UPDATE agent_profiles SET icon = CASE name
  WHEN 'Atlas' THEN 'globe' WHEN 'Forge' THEN 'hammer' WHEN 'Metis' THEN 'dna' WHEN 'Mnemosyne' THEN 'database' WHEN 'Sherpa' THEN 'compass' WHEN 'Oneiros' THEN 'moon'
  WHEN 'Scout' THEN 'magnifying-glass' WHEN 'Cipher' THEN 'terminal' WHEN 'Abacus' THEN 'calculator' WHEN 'Quill' THEN 'pen-nib' WHEN 'Hearth' THEN 'house' ELSE '' END;

-- trusted skill hubs: agents may search and install from them on their own
ALTER TABLE skill_hubs ADD COLUMN trusted boolean NOT NULL DEFAULT false;

-- knowledge base: pages generated from memory, organised in folders
CREATE TABLE kb_folders (
  id         bigserial PRIMARY KEY,
  parent_id  bigint REFERENCES kb_folders ON DELETE CASCADE,
  name       text NOT NULL,
  query      text NOT NULL DEFAULT '',        -- smart folders describe what belongs inside
  smart      boolean NOT NULL DEFAULT false,
  max_pages  int NOT NULL DEFAULT 6,
  expanded_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE kb_pages (
  id           bigserial PRIMARY KEY,
  folder_id    bigint REFERENCES kb_folders ON DELETE SET NULL,
  title        text NOT NULL,
  query        text NOT NULL,
  body         text NOT NULL DEFAULT '',
  enrich       boolean NOT NULL DEFAULT false,  -- let an agent research gaps before writing
  auto         boolean NOT NULL DEFAULT true,   -- regenerate when memory changed
  agent        text NOT NULL DEFAULT '',
  status       text NOT NULL DEFAULT 'empty',   -- empty | generating | ready | error
  error        text NOT NULL DEFAULT '',
  sources      bigint[] NOT NULL DEFAULT '{}',  -- memory fact ids the page was built from
  generated_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kb_pages_folder_idx ON kb_pages(folder_id);
