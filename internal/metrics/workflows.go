package metrics

import (
	"context"
	"time"
)

// WorkflowTrace reports observable costs for each root task. Calls without a task ID,
// including some background actions, cannot be reliably assigned to a request.
type WorkflowTrace struct {
	TaskID              int64  `json:"task_id"`
	Status              string `json:"status"`
	ModelCalls          int    `json:"model_calls"`
	ModelMS             int64  `json:"model_ms"`
	ToolCalls           int    `json:"tool_calls"`
	Delegations         int    `json:"delegations"`
	Questions           int    `json:"questions"`
	FailedTools         int    `json:"failed_tools"`
	MaxDepth            int    `json:"max_depth"`
	RepeatedAssignments int    `json:"repeated_assignments"`
}

func (s *Store) WorkflowTraces(ctx context.Context, since time.Duration) ([]WorkflowTrace, error) {
	rows, err := s.DB.Query(ctx, `SELECT r.id,r.status,
 (SELECT count(*) FROM llm_calls l JOIN tasks t ON t.id=l.task_id WHERE t.root_id=r.id),
 (SELECT COALESCE(sum(l.ms),0) FROM llm_calls l JOIN tasks t ON t.id=l.task_id WHERE t.root_id=r.id),
 (SELECT count(*) FROM tool_calls c JOIN tasks t ON t.id=c.task_id WHERE t.root_id=r.id),
 (SELECT count(*) FROM tasks t WHERE t.root_id=r.id AND t.id<>r.id),
 (SELECT count(*) FROM ask_history a JOIN tasks t ON t.id=a.task_id WHERE t.root_id=r.id),
 (SELECT count(*) FROM tool_calls c JOIN tasks t ON t.id=c.task_id WHERE t.root_id=r.id AND NOT c.ok),
 (SELECT COALESCE(max(t.depth),0) FROM tasks t WHERE t.root_id=r.id),
 (SELECT COALESCE(sum(n-1),0) FROM (SELECT count(*) AS n FROM tasks t WHERE t.root_id=r.id AND t.id<>r.id GROUP BY lower(t.to_agent),lower(trim(t.input)) HAVING count(*)>1) repeats)
 FROM tasks r WHERE r.parent_id IS NULL AND r.created_at >= $1 ORDER BY r.id DESC LIMIT 100`, time.Now().Add(-since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkflowTrace{}
	for rows.Next() {
		var v WorkflowTrace
		if err := rows.Scan(&v.TaskID, &v.Status, &v.ModelCalls, &v.ModelMS, &v.ToolCalls, &v.Delegations, &v.Questions, &v.FailedTools, &v.MaxDepth, &v.RepeatedAssignments); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
