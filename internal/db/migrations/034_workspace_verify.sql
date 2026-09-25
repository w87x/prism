-- Per-repository check commands, the result of running them in a workspace, and the automatic review of it.
CREATE TABLE code_projects (
  repo       text PRIMARY KEY,
  build_cmd  text NOT NULL DEFAULT '',
  test_cmd   text NOT NULL DEFAULT '',
  lint_cmd   text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE code_workspaces ADD COLUMN verify_status text NOT NULL DEFAULT '';   -- '' | pass | fail | none
ALTER TABLE code_workspaces ADD COLUMN verify_output text NOT NULL DEFAULT '';
ALTER TABLE code_workspaces ADD COLUMN verify_hash   text NOT NULL DEFAULT '';   -- state of the workspace when it was verified
ALTER TABLE code_workspaces ADD COLUMN verified_at   timestamptz;
ALTER TABLE code_workspaces ADD COLUMN review_task_id bigint;
