-- Document ingestion: sources can be watched for changes, can hold material from outside (untrusted), and
-- chunks remember a hash of their file's extracted text so a re-saved but unchanged file is not re-embedded.
ALTER TABLE doc_sources ADD COLUMN watch     boolean NOT NULL DEFAULT false;
ALTER TABLE doc_sources ADD COLUMN untrusted boolean NOT NULL DEFAULT false;
ALTER TABLE doc_chunks  ADD COLUMN hash text NOT NULL DEFAULT '';
