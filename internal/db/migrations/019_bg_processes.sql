-- Process manager: the existing shell/python tools block until the command exits and cap output at ~20KB,
-- so a long-running command (a build, an indexing pass, a big transfer) either blocks the agent's whole turn
-- or overruns the output cap with no way to check on it later. bg_processes gives a background command a
-- durable identity: start it, come back and check status/log, send it input, cancel it.
CREATE TABLE bg_processes (
  id          bigserial PRIMARY KEY,
  task_id     bigint,                       -- the task this was started for, if any (0/NULL: a chat-driven run)
  agent       text NOT NULL DEFAULT '',
  command     text NOT NULL,
  cwd         text NOT NULL DEFAULT '',
  status      text NOT NULL DEFAULT 'running', -- running | exited | killed | failed | unknown
  exit_code   int,
  output      text NOT NULL DEFAULT '',      -- periodically flushed while running; final on completion
  created_at  timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX bg_processes_running_idx ON bg_processes(id) WHERE status = 'running';

-- Bug-class already fixed once for tasks (see 017/pending_asks): a process still "running" in the DB when
-- PRISM restarts has no live handle any more — the manager's in-memory map died with the old process. It is
-- marked 'unknown' on startup (see ProcessManager.Reconcile) rather than trusted at face value: the
-- underlying OS process may still be running (Setpgid means it isn't killed by PRISM exiting) or may be
-- long gone: either way PRISM cannot tell, and must not claim it does.
