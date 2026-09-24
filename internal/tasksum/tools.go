package tasksum

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/tools"
)

// RegisterTools installs task_summary_find: a search over past finished tasks' goals, decisions, attempts
// and outcomes — available to every agent, the same way memory_find is, since "has something like this
// already been tried?" comes up in any specialist's work, not just Atlas's.
func RegisterTools(reg *tools.Registry, s *Store) {
	reg.Register(&tools.Tool{
		Name: "task_summary_find", Category: "memory", Risk: tools.RiskRead,
		Description: "Search past finished tasks by what they were trying to do: goal, decisions made, approaches attempted — including ones that failed or were abandoned — outcome, and anything left unfinished. Use before starting work that might already have been tried, or when asked what was done before and why.",
		Params:      tools.Obj("query", tools.Str("query", "what to look for"), tools.Int("limit", "max results (default 5)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				Query string
				Limit int
			}](raw)
			if err != nil {
				return "", err
			}
			hits, err := s.Find(ctx, a.Query, a.Limit)
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				return "No matching past tasks.", nil
			}
			var sb strings.Builder
			for _, h := range hits {
				fmt.Fprintf(&sb, "%s [%s · task #%d · %s]\nGoal: %s\n", h.Title, h.Status, h.TaskID, h.CreatedAt.Format("2006-01-02"), h.Goal)
				if h.Decisions != "" {
					fmt.Fprintf(&sb, "Decisions: %s\n", h.Decisions)
				}
				if h.Attempts != "" {
					fmt.Fprintf(&sb, "Attempts: %s\n", h.Attempts)
				}
				fmt.Fprintf(&sb, "Outcome: %s\n", h.Outcome)
				if h.Unfinished != "" {
					fmt.Fprintf(&sb, "Unfinished: %s\n", h.Unfinished)
				}
				sb.WriteString("\n")
			}
			return sb.String(), nil
		},
	})
}
