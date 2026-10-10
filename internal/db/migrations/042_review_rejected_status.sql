-- A fact the user marks "not true" is retired with status 'rejected' (and never learned again), distinct from a plain
-- retirement. Written to be safe to run on a database that already has some of it: it only adds what is missing.
ALTER TABLE briefings ADD COLUMN IF NOT EXISTS questions jsonb NOT NULL DEFAULT '[]';

DO $$
DECLARE c text;
BEGIN
  FOR c IN SELECT conname FROM pg_constraint
           WHERE conrelid = 'memory_facts'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%retracted%'
  LOOP
    EXECUTE format('ALTER TABLE memory_facts DROP CONSTRAINT %I', c);
  END LOOP;
  ALTER TABLE memory_facts ADD CONSTRAINT memory_facts_status_check
    CHECK (status IN ('proposed','active','superseded','retracted','expired','rejected'));
END $$;
