-- task_meta (acceptance criteria, ratings, handovers) had no link to its task: after a data reset the ids restarted, and new tasks
-- inherited the old tasks' criteria. Rows without a task go, and from now on a task takes its meta with it.
DELETE FROM task_meta WHERE task_id NOT IN (SELECT id FROM tasks);
ALTER TABLE task_meta DROP CONSTRAINT IF EXISTS task_meta_task_fk;
ALTER TABLE task_meta ADD CONSTRAINT task_meta_task_fk FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE;
