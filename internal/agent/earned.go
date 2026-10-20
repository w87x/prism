package agent

import (
	"context"

	"prism/internal/tools"
)

// Earned autonomy. Trust grows with use: someone who has allowed the same tool again and again, and never refused it, is doing the assistant's
// job for it. So each answer to "may I run this?" is recorded, and a tool with a long clean record is offered for arming. It is only ever an
// offer — the user arms it (Tools page) or says "not now" — and a tool whose name says it sends, deletes, pays or the like is never offered.
const (
	earnedMinAllows = 8  // allowed this many times in the last 30 days…
	earnedMinTasks  = 3  // …across at least this many separate tasks…
	earnedCleanDays = 90 // …and never refused in this long
	dismissedKey    = "arm_suggest_dismissed"
)

// noteApproval records the answer to a confirmation.
func (e *Engine) noteApproval(ctx context.Context, tool, agent string, taskID int64, allowed bool) {
	_, _ = e.DB.Exec(ctx, `INSERT INTO tool_approvals(tool,agent,task_id,allowed) VALUES($1,$2,$3,$4)`, tool, agent, taskID, allowed)
}

// ArmSuggestion is a tool the user keeps allowing.
type ArmSuggestion struct {
	Tool   string   `json:"tool"`
	Risk   string   `json:"risk"`
	Allows int      `json:"allows"`
	Tasks  int      `json:"tasks"`
	Agents []string `json:"agents"`
}

// ArmSuggestions lists the tools worth offering for arming.
func (e *Engine) ArmSuggestions(ctx context.Context) []ArmSuggestion {
	rows, err := e.DB.Query(ctx, `SELECT tool, count(*), count(DISTINCT NULLIF(task_id,0)), array_agg(DISTINCT agent)
		FROM tool_approvals WHERE allowed AND at > now()-interval '30 days'
		AND tool NOT IN (SELECT tool FROM tool_approvals WHERE NOT allowed AND at > now()-make_interval(days=>$1))
		GROUP BY tool HAVING count(*) >= $2 AND count(DISTINCT NULLIF(task_id,0)) >= $3 ORDER BY count(*) DESC`, earnedCleanDays, earnedMinAllows, earnedMinTasks)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var dismissed []string
	if e.Settings != nil {
		_, _ = e.Settings.Get(ctx, dismissedKey, &dismissed)
	}
	skip := map[string]bool{}
	for _, d := range dismissed {
		skip[d] = true
	}
	var out []ArmSuggestion
	for rows.Next() {
		var s ArmSuggestion
		if rows.Scan(&s.Tool, &s.Allows, &s.Tasks, &s.Agents) != nil || skip[s.Tool] || neverAuto.MatchString(s.Tool) {
			continue
		}
		t, ok := e.Tools.Get(s.Tool)
		if !ok || t.Risk == tools.RiskRead {
			continue
		}
		if st := e.Tools.State(s.Tool); !st.Enabled || st.Armed {
			continue
		}
		s.Risk = t.Risk.String()
		out = append(out, s)
	}
	return out
}

// DismissArmSuggestion remembers "not now" for a tool, so it is not offered again.
func (e *Engine) DismissArmSuggestion(ctx context.Context, tool string) error {
	var dismissed []string
	_, _ = e.Settings.Get(ctx, dismissedKey, &dismissed)
	for _, d := range dismissed {
		if d == tool {
			return nil
		}
	}
	return e.Settings.Set(ctx, dismissedKey, append(dismissed, tool))
}
