package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/textutil"
)

// Promise review. An autonomous agent (a cron, a standing intent) often ends its report with something like "I looked for the
// movie; if it is released I will download it" — and nothing ever starts the download: no tool call did it, and nothing is
// watching for the release. After such a run a reviewer reads the report next to what the agent really did, and flags every
// commitment that was claimed or promised without anything behind it. Depending on the setting the user is only told, or the
// agent is also asked once to make it real (do it now, or set up the watch/cron that will).

const promiseReviewName = "promise-review"

const promiseReviewPrompt = `You audit one run of an autonomous agent. You get the TASK it was given, its final REPORT, and the list of TOOL CALLS it actually made (with short results).
Find every COMMITMENT in the report: something the agent says it did, or says it will do ("started the download", "I will notify you when it is out", "if it is released I will download it", "scheduled", "I'll keep watching").
For each commitment decide its status:
- "done": the tool calls show it really happened (a download was started, a message sent, a reminder or watch created…).
- "not_done": it is claimed as done, or promised for now, but no tool call did it.
- "needs_trigger": it depends on something in the future ("when/if X happens, I will Y") and nothing was set up to make that happen — no watch, cron, intent, tracker or sleep call that would carry it out later. An agent cannot act between its runs; only such a call can.
Report only real commitments about actions, not observations ("the movie is not released yet" is not one). Do not flag a promise that was kept, or one that a creating/scheduling tool call (cron_create, intent_create, watch_command, tracker_*, sleep…) covers.
Answer JSON only: {"commitments":[{"text":"the commitment in one sentence","status":"done|not_done|needs_trigger","evidence":"what the tool calls show or lack","fix":"the one concrete thing that would make it real (which tool, or which watch/cron to set up)"}]}`

type promiseItem struct {
	Text     string `json:"text"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
	Fix      string `json:"fix"`
}

// PromiseReviewRow is a recorded review with at least one broken promise.
type PromiseReviewRow struct {
	ID        int64         `json:"id"`
	TaskID    int64         `json:"task_id"`
	Agent     string        `json:"agent"`
	Title     string        `json:"title"`
	Promises  []promiseItem `json:"promises"`
	Status    string        `json:"status"`
	FixTaskID *int64        `json:"fix_task_id,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
}

// reviewsPromises reports whether a finished task is one whose promises are checked.
func reviewsPromises(t tasks.Task) bool {
	return t.Status == tasks.Done && t.Depth == 0 && (t.FromKind == "cron" || t.FromKind == "intent" || t.FromName == promiseReviewName)
}

// toolTrail renders what a task's session really did: each tool call with the start of its result.
func (e *Engine) toolTrail(ctx context.Context, sessionID int64) string {
	msgs, err := e.Sessions.Messages(ctx, sessionID)
	if err != nil {
		return ""
	}
	results := map[string]string{}
	for _, m := range msgs {
		if m.Role == "tool" {
			results[m.ToolCallID] = textutil.Brief(strings.Join(strings.Fields(m.Content), " "), 160)
		}
	}
	var sb strings.Builder
	n := 0
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			if n++; n > 60 {
				return sb.String() + "…(more calls not shown)\n"
			}
			fmt.Fprintf(&sb, "- %s(%s) → %s\n", c.Name, textutil.Brief(strings.Join(strings.Fields(c.Arguments), " "), 140), results[c.ID])
		}
	}
	return sb.String()
}

// ReviewPromises checks one finished autonomous task and acts on what it finds. It never blocks the caller.
func (e *Engine) ReviewPromises(ctx context.Context, t tasks.Task) {
	if !reviewsPromises(t) || t.SessionID == nil || len(strings.TrimSpace(t.Result)) < 20 || e.DB == nil {
		return
	}
	cfg := settings.Load(ctx, e.Settings, settings.KeyAutonomy, settings.DefaultAutonomy())
	mode := cfg.PromiseReviewMode()
	if mode == "off" || e.LLM == nil || e.LLM.RoleRef(ctx, "fast") == "" {
		return
	}
	trail := e.toolTrail(ctx, *t.SessionID)
	if strings.TrimSpace(trail) == "" {
		trail = "(the agent made no tool calls at all)\n"
	}
	var out struct {
		Commitments []promiseItem `json:"commitments"`
	}
	user := fmt.Sprintf("TASK:\n%s\n\nREPORT:\n%s\n\nTOOL CALLS:\n%s", textutil.Brief(t.Input, 1500), textutil.Brief(t.Result, 3000), trail)
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := e.LLM.CompleteJSON(rctx, e.LLM.ActionRef(rctx, "promise_review", "fast"), promiseReviewPrompt, user, &out); err != nil {
		return
	}
	var broken []promiseItem
	for _, c := range out.Commitments {
		if (c.Status == "not_done" || c.Status == "needs_trigger") && strings.TrimSpace(c.Text) != "" {
			broken = append(broken, c)
		}
	}
	// the review of a follow-up closes the review it was made for: kept → done; broken again → superseded by this new one
	if t.FromName == promiseReviewName {
		to := "done"
		if len(broken) > 0 {
			to = "superseded"
		}
		_, _ = e.DB.Exec(ctx, `UPDATE promise_reviews SET status=$2 WHERE fix_task_id=$1 AND status='fixing'`, t.ID, to)
		e.Emit("promise.update", nil)
	}
	if len(broken) == 0 {
		return
	}
	pj, _ := json.Marshal(broken)
	var id int64
	if err := e.DB.QueryRow(ctx, `INSERT INTO promise_reviews(task_id,agent,title,promises) VALUES($1,$2,$3,$4) RETURNING id`, t.ID, t.ToAgent, t.Title, pj).Scan(&id); err != nil {
		return
	}
	e.Emit("promise.update", map[string]any{"id": id})
	// the same job over and over: the cause is usually the model behind it, not bad luck
	var earlier int
	_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM promise_reviews WHERE agent=$1 AND title=$2 AND id<>$3 AND created_at > now()-interval '7 days'`, t.ToAgent, t.Title, id).Scan(&earlier)
	repeat := ""
	if earlier >= 2 {
		repeat = fmt.Sprintf("\nThis is the %s time in a week that %s's report has not matched what it did: its model may not be calling tools reliably (it writes the call as text). Consider giving it another model on the Agents page.", ordinal(earlier+1), t.ToAgent)
	}
	lines := make([]string, 0, len(broken))
	for _, b := range broken {
		lines = append(lines, "• "+b.Text)
	}
	if mode == "fix" && t.FromName != promiseReviewName { // one correction per run: the follow-up's own review only reports
		if _, err := e.FixPromise(ctx, id); err == nil {
			e.Notify(ctx, Notice{Agent: t.ToAgent, Level: "attention", Text: fmt.Sprintf("“%s” said it would do something it did not do:\n%s\nI asked %s to make it happen now.%s", textutil.Brief(t.Title, 60), strings.Join(lines, "\n"), t.ToAgent, repeat)})
			return
		}
	}
	e.Notify(ctx, Notice{Agent: t.ToAgent, Level: "attention", Text: fmt.Sprintf("“%s” made promises nothing carried out:\n%s\nOpen Today → Needs your attention to have it made real or dismissed.%s", textutil.Brief(t.Title, 60), strings.Join(lines, "\n"), repeat)})
}

// FixPromise asks the agent whose run left promises unkept to make them real: do the action now if its condition already holds,
// or set up the watch / cron / intent that will do it when the time comes.
func (e *Engine) FixPromise(ctx context.Context, id int64) (*tasks.Task, error) {
	var r PromiseReviewRow
	var pj []byte
	if err := e.DB.QueryRow(ctx, `SELECT id,task_id,agent,title,promises,status FROM promise_reviews WHERE id=$1`, id).Scan(&r.ID, &r.TaskID, &r.Agent, &r.Title, &pj, &r.Status); err != nil {
		return nil, fmt.Errorf("promise review #%d does not exist", id)
	}
	if r.Status == "fixing" {
		return nil, fmt.Errorf("promise review #%d is already being fixed", id)
	}
	_ = json.Unmarshal(pj, &r.Promises)
	orig, err := e.Tasks.Get(ctx, r.TaskID)
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	sb.WriteString("Your earlier run of this task said it would do, or had done, things that did not happen — nothing in your tool calls carried them out, and nothing will carry them out later on its own.\n\nThe task was:\n")
	sb.WriteString(textutil.Brief(orig.Input, 1200))
	sb.WriteString("\n\nYour report said:\n")
	sb.WriteString(textutil.Brief(orig.Result, 1500))
	sb.WriteString("\n\nWhat is missing:\n")
	for _, p := range r.Promises {
		fmt.Fprintf(&sb, "- %s (%s)", p.Text, strings.ReplaceAll(p.Status, "_", " "))
		if p.Fix != "" {
			sb.WriteString(" — " + p.Fix)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nMake it real now, with your tools. If the condition is already met, do the action (for example start the download) and confirm it started. If it depends on something in the future, set up the standing watch, cron or intent that will do it when it happens (or sleep and check again) — a sentence promising it later is not enough. Finish by stating exactly what you did or set up; if you cannot, say so plainly.")
	t, err := e.Enqueue(ctx, tasks.Task{FromKind: "system", FromName: promiseReviewName, ToAgent: r.Agent, Title: "Keep a promise: " + textutil.Brief(r.Title, 60), Input: sb.String()})
	if err != nil {
		return nil, err
	}
	_, _ = e.DB.Exec(ctx, `UPDATE promise_reviews SET status='fixing', fix_task_id=$2 WHERE id=$1`, id, t.ID)
	e.Emit("promise.update", map[string]any{"id": id})
	return &t, nil
}

// PromiseReviews lists the reviews waiting for the user (status open).
func (e *Engine) PromiseReviews(ctx context.Context, status string) ([]PromiseReviewRow, error) {
	rows, err := e.DB.Query(ctx, `SELECT id,task_id,agent,title,promises,status,fix_task_id,created_at FROM promise_reviews WHERE ($1='' OR status=$1) ORDER BY id DESC LIMIT 100`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PromiseReviewRow{}
	for rows.Next() {
		var r PromiseReviewRow
		var pj []byte
		if rows.Scan(&r.ID, &r.TaskID, &r.Agent, &r.Title, &pj, &r.Status, &r.FixTaskID, &r.CreatedAt) == nil {
			_ = json.Unmarshal(pj, &r.Promises)
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// DismissPromise closes a review the user does not want acted on.
func (e *Engine) DismissPromise(ctx context.Context, id int64) error {
	_, err := e.DB.Exec(ctx, `UPDATE promise_reviews SET status='dismissed' WHERE id=$1`, id)
	if err == nil {
		e.Emit("promise.update", map[string]any{"id": id})
	}
	return err
}

func ordinal(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return fmt.Sprintf("%dth", n)
	case n%10 == 1:
		return fmt.Sprintf("%dst", n)
	case n%10 == 2:
		return fmt.Sprintf("%dnd", n)
	case n%10 == 3:
		return fmt.Sprintf("%drd", n)
	}
	return fmt.Sprintf("%dth", n)
}
