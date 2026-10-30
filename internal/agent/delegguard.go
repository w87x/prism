package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"prism/internal/tools"
)

// Delegation guard. Asking a colleague costs a whole run, and agents ask again and again: one agent put eighteen near-identical "help for
// Oneiros" requests to the same colleague. A request that is almost the same as one made lately by the same agent to the same colleague is
// refused with a pointer to the earlier task (its answer is one task_status away); and one task may delegate only so many times.
const (
	dupSimilarity       = 0.85
	dupDoneWindow       = 5 * time.Minute // an answered request may be asked again after this (things change)
	dupLookback         = 2 * time.Hour   // one still queued or running is a duplicate for as long as it runs
	maxDelegationsEach  = 20              // per task
	maxAsksPerColleague = 3               // per task: the same colleague asked a fourth time is a sign the work should have been one batched request
)

func tokenWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 3 {
			out[w] = true
		}
	}
	return out
}

func similarity(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

func (e *Engine) delegationGuard(ctx context.Context, env *tools.Env, to, instruction string) error {
	if e.DB == nil {
		return nil
	}
	if env.TaskID != 0 {
		var n int
		if e.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE parent_id=$1`, env.TaskID).Scan(&n) == nil && n >= maxDelegationsEach {
			return fmt.Errorf("this task has already delegated %d times: that is enough. Use what the colleagues returned (task_status shows their results) and finish", n)
		}
	}
	if env.TaskID != 0 {
		var n int
		if e.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE parent_id=$1 AND lower(to_agent)=lower($2)`, env.TaskID, to).Scan(&n) == nil && n >= maxAsksPerColleague {
			return fmt.Errorf("you have already asked %s %d times in this task. Each ask is a whole run: put everything you still need into ONE request (a list of all the items), or use what %s already returned (task_status shows the results)", to, n, to)
		}
	}
	rows, err := e.DB.Query(ctx, `SELECT id, status, input, finished_at FROM tasks WHERE from_kind='agent' AND lower(from_name)=lower($1) AND lower(to_agent)=lower($2)
		AND created_at > now() - make_interval(secs => $3) ORDER BY id DESC LIMIT 30`, env.Agent, to, dupLookback.Seconds())
	if err != nil {
		return nil
	}
	defer rows.Close()
	want := tokenWords(instruction)
	for rows.Next() {
		var id int64
		var status, input string
		var fin *time.Time
		if rows.Scan(&id, &status, &input, &fin) != nil || similarity(want, tokenWords(input)) < dupSimilarity {
			continue
		}
		switch status {
		case "queued", "running", "waiting_input":
			return fmt.Errorf("%s is already working on almost exactly this (task #%d). Wait for it: task_status(%d) shows the result when it is done", to, id, id)
		case "done":
			if fin != nil && time.Since(*fin) < dupDoneWindow {
				return fmt.Errorf("you asked %s almost exactly this %d minutes ago and it was answered (task #%d): read the answer with task_status(%d). If something changed since, say what changed in the new request", to, int(time.Since(*fin).Minutes()), id, id)
			}
		}
	}
	return nil
}
