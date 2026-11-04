-- A planner's draft is retained with the request. Links to real tasks allow the UI to show evidence-based status.
CREATE TABLE IF NOT EXISTS work_items (
  task_id bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  item_no int NOT NULL,
  title text NOT NULL,
  done_when text NOT NULL DEFAULT '',
  depends_on text[] NOT NULL DEFAULT '{}',
  delegate_id bigint REFERENCES tasks(id) ON DELETE SET NULL,
  PRIMARY KEY (task_id, item_no)
);
