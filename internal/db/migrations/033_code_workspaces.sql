-- Isolated git worktrees where agents do coding work; the user reviews the diff and keeps, applies or discards it.
CREATE TABLE code_workspaces (
  id         bigserial PRIMARY KEY,
  repo       text NOT NULL,
  path       text NOT NULL,
  branch     text NOT NULL,
  base       text NOT NULL,          -- commit the worktree started from
  agent      text NOT NULL DEFAULT '',
  task_id    bigint,
  status     text NOT NULL DEFAULT 'open',   -- open | kept | applied | discarded
  created_at timestamptz NOT NULL DEFAULT now()
);
