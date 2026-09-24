-- Deep analysis (memory.Analyze): a watermark like reflected_at, and a short profile card per bank that
-- the analysis keeps up to date (the user's bank card is shown to the memory model as background).
ALTER TABLE memory_banks ADD COLUMN analyzed_at timestamptz;
ALTER TABLE memory_banks ADD COLUMN card text NOT NULL DEFAULT '';
