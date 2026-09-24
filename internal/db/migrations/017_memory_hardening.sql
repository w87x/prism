-- Fixes from a code-inspection review of the memory and task-recovery paths.

-- Bug: raw→fact background extraction dropped facts silently on a Store() failure, then deleted the raw
-- messages anyway. attempts/last_error let a batch be retried instead of lost; memory_pending_facts holds
-- facts the model already extracted but that failed to store, so a retry never re-asks the model (which
-- could phrase things differently and produce a near-duplicate) — it only re-attempts the exact same Store.
ALTER TABLE memory_raw ADD COLUMN attempts int NOT NULL DEFAULT 0;
ALTER TABLE memory_raw ADD COLUMN last_error text NOT NULL DEFAULT '';

CREATE TABLE memory_pending_facts (
  id         bigserial PRIMARY KEY,
  raw_ids    bigint[] NOT NULL,               -- memory_raw rows this batch came from; they are kept until every fact here is resolved
  bank       text NOT NULL,
  agent      text NOT NULL DEFAULT '',
  text       text NOT NULL,
  tags       text[] NOT NULL DEFAULT '{}',
  confidence real NOT NULL DEFAULT 0.7,
  tainted    boolean NOT NULL DEFAULT false,
  attempts   int NOT NULL DEFAULT 0,
  last_error text NOT NULL DEFAULT '',
  given_up   boolean NOT NULL DEFAULT false,  -- exceeded the retry cap; kept for inspection, no longer retried
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX memory_pending_facts_raw_idx ON memory_pending_facts USING gin(raw_ids);
CREATE INDEX memory_pending_facts_retry_idx ON memory_pending_facts(id) WHERE NOT given_up;

-- Bug: raw messages carried no taint/source signal, so a fact distilled from a web page an agent read (via
-- the sub-agent that fetched it) got whatever confidence the extraction model felt like assigning, with no
-- floor tied to how trustworthy the source actually was.
ALTER TABLE memory_raw ADD COLUMN tainted boolean NOT NULL DEFAULT false;

-- Embedding identity: which model produced a fact's vector, so switching embedding models can't silently
-- compare vectors from two different spaces as if they were comparable (matching dimensions is not enough).
ALTER TABLE memory_facts ADD COLUMN embed_model text NOT NULL DEFAULT '';

-- Bug: pending user-facing asks (tool approvals, clarifying questions) lived only in RAM — a restart lost
-- them with no record they had ever been asked. Persisted here; cleared on answer/timeout in the normal
-- path, and reconciled (not silently resumed) on startup if the process died while one was outstanding.
CREATE TABLE pending_asks (
  id         bigint PRIMARY KEY, -- the engine's own in-memory ask id (Engine.askSeq), not a separate sequence
  run_id     bigint NOT NULL,
  task_id    bigint,
  agent      text NOT NULL,
  kind       text NOT NULL,
  question   text NOT NULL,
  options    text[] NOT NULL DEFAULT '{}',
  tool       text NOT NULL DEFAULT '',
  args       text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Bug: a requeued (restart-interrupted) task re-ran with its original input appended as a fresh instruction,
-- with nothing telling the agent it may have already taken side-effecting actions. restarts makes a resumed
-- task visible and drives a reconciliation notice instead of a silent blind repeat.
ALTER TABLE tasks ADD COLUMN restarts int NOT NULL DEFAULT 0;
