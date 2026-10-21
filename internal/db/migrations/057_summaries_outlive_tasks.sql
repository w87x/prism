-- A task summary is the durable record of what was tried and why ("what did we do about this before?"); the task itself is cleaned up after a
-- few days. The summary used to be deleted with its task, so that memory only reached back as far as the retention setting (5 days in one
-- install) and the recurring-work learner had nothing to learn from. Summaries now have their own, much longer, retention (see maint.Cleanup).
ALTER TABLE task_summaries DROP CONSTRAINT IF EXISTS task_summaries_task_id_fkey;
