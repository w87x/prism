package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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
The TOOL CALLS include those of the colleagues the agent asked (their lines start with their name). A watch or monitor set up by a colleague counts.
Give each commitment a "kind": "watch" when it is about observing or telling later ("I'll keep an eye on the download", "I'll let you know when it is done", "I'll check back tomorrow", "I'll notify you when it is out"), otherwise "action".
Answer JSON only: {"commitments":[{"text":"the commitment in one sentence","kind":"watch|action","status":"done|not_done|needs_trigger","evidence":"what the tool calls show or lack","fix":"the one concrete thing that would make it real (which tool, or which watch/cron to set up)"}]}`

type promiseItem struct {
	Kind     string `json:"kind"`
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

// followUpSignal: a reply that promises to look, tell or act later. Only such replies of the user's own requests are reviewed (a model call
// each), so an ordinary answer costs nothing.
var followUpSignal = regexp.MustCompile(`(?i)(\bi['’]ll\b|\bi will\b|\bwill (?:let you know|notify|tell you|check|monitor|keep|remind|follow|watch|download|report|update)\b|keep an eye|check back|keep (?:watching|monitoring|tracking)|let you know|get back to you|report back|\bwe['’]ll\b|я (?:проверю|сообщу|буду|напомню|прослежу|слежу|дам знать)|сообщу|напомню|прослежу|дам знать|буду следить)`)

// userAsked: a request the user made themselves, at the top of its task tree.
func userAsked(t tasks.Task) bool {
	return t.Depth == 0 && (t.FromKind == "user" || t.FromKind == "telegram")
}

// reviewsPromises reports whether a finished task is one whose promises are checked: an autonomous run (a cron, an intent), or a request
// of the user's own whose answer promises to look, tell or act later.
func reviewsPromises(t tasks.Task) bool {
	if t.Status != tasks.Done || t.Depth != 0 {
		return false
	}
	if t.FromKind == "cron" || t.FromKind == "intent" || t.FromName == promiseReviewName {
		return true
	}
	return userAsked(t) && followUpSignal.MatchString(t.Result)
}

// treeTrail is toolTrail for a whole request: the agent's own calls and those of every colleague it asked, each line marked with who made it.
func (e *Engine) treeTrail(ctx context.Context, t tasks.Task) string {
	var sb strings.Builder
	if t.SessionID != nil {
		sb.WriteString(e.toolTrail(ctx, *t.SessionID))
	}
	rows, err := e.DB.Query(ctx, `SELECT to_agent, session_id FROM tasks WHERE root_id=$1 AND id<>$2 AND session_id IS NOT NULL ORDER BY id LIMIT 40`, t.RootID, t.ID)
	if err != nil {
		return sb.String()
	}
	defer rows.Close()
	for rows.Next() {
		var agent string
		var sid int64
		if rows.Scan(&agent, &sid) != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(e.toolTrail(ctx, sid)), "\n") {
			if strings.TrimSpace(line) != "" {
				sb.WriteString("- [" + agent + "] " + strings.TrimPrefix(line, "- ") + "\n")
			}
		}
		if sb.Len() > 9000 {
			break
		}
	}
	return sb.String()
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
	if t.FromKind == "cron" { // the built-in maintenance reports (dream, consolidation) summarise; their small mismatches are noise
		var system bool
		if e.DB.QueryRow(ctx, `SELECT system FROM crons WHERE name=$1 LIMIT 1`, t.FromName).Scan(&system) == nil && system {
			return
		}
	}
	cfg := settings.Load(ctx, e.Settings, settings.KeyAutonomy, settings.DefaultAutonomy())
	mode := cfg.PromiseReviewMode()
	if mode == "off" || e.LLM == nil || e.LLM.RoleRef(ctx, "fast") == "" {
		return
	}
	trail := e.toolTrail(ctx, *t.SessionID)
	if userAsked(t) {
		trail = e.treeTrail(ctx, t)
	}
	if strings.TrimSpace(trail) == "" {
		trail = "(the agent made no tool calls at all)\n"
	}
	var out struct {
		Commitments []promiseItem `json:"commitments"`
	}
	user := fmt.Sprintf("TASK:\n%s\n\nREPORT:\n%s\n\nTOOL CALLS:\n%s", textutil.Brief(t.Input, 1500), textutil.Brief(t.Result, 3000), trail)
	rctx, cancel := context.WithTimeout(ctx, llmCallTimeout)
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
	broken = e.dropCovered(ctx, t, broken)
	if len(broken) == 0 {
		return
	}
	pj, _ := json.Marshal(broken)
	var id int64
	if err := e.DB.QueryRow(ctx, `INSERT INTO promise_reviews(task_id,agent,title,promises) VALUES($1,$2,$3,$4) RETURNING id`, t.ID, t.ToAgent, t.Title, pj).Scan(&id); err != nil {
		return
	}
	e.Emit("promise.update", map[string]any{"id": id})
	// "I'll keep an eye on it": the user's own request promised to watch or tell later, and nothing will. Setting up a watch only observes and
	// tells, so it is done at once whatever the mode (short of "off"); the colleague that holds the monitor tools makes it real.
	if userAsked(t) {
		var watch []promiseItem
		for _, b := range broken {
			if b.Kind == "watch" {
				watch = append(watch, b)
			}
		}
		if len(watch) > 0 {
			if wt, err := e.setUpWatch(ctx, id, t, watch); err == nil {
				e.NotifyHousekeeping(ctx, "autonomy", "A promise to follow up is being made real", Notice{Agent: t.ToAgent, Level: "info", Text: fmt.Sprintf("“%s” promised to follow up (%s) but nothing was set to. %s is setting up the watch now (task #%d); you will be told when it happens.", textutil.Brief(t.Title, 60), textutil.Brief(watch[0].Text, 90), wt.ToAgent, wt.ID)}, false)
				return
			}
		}
	}
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
			e.NotifyHousekeeping(ctx, "autonomy", t.ToAgent+": a promise was not kept", Notice{Agent: t.ToAgent, Level: "attention", Text: fmt.Sprintf("“%s” said it would do something it did not do:\n%s\nI asked %s to make it happen now.%s", textutil.Brief(t.Title, 60), strings.Join(lines, "\n"), t.ToAgent, repeat)}, false)
			return
		}
	}
	e.NotifyHousekeeping(ctx, "autonomy", t.ToAgent+": promises not carried out", Notice{Agent: t.ToAgent, Level: "attention", Text: fmt.Sprintf("“%s” made promises nothing carried out:\n%s\nOpen Today → Needs your attention to have it made real or dismissed.%s", textutil.Brief(t.Title, 60), strings.Join(lines, "\n"), repeat)}, false)
}

// watcherAgent is who sets up a watch: Sentinel, the staff member that holds the monitor tools.
func (e *Engine) watcherAgent(ctx context.Context, fallback string) string {
	if p, err := e.Profiles.Get(ctx, "Sentinel"); err == nil && p.Enabled {
		return p.Name
	}
	return fallback
}

// setUpWatch has a colleague start the monitor that carries out a promise to look or tell later.
func (e *Engine) setUpWatch(ctx context.Context, reviewID int64, orig tasks.Task, watch []promiseItem) (*tasks.Task, error) {
	var sb strings.Builder
	sb.WriteString("The user made a request and the answer promised to follow up — but nothing is following up, and nobody can act between runs. Make the promise real: start the monitor that watches for it and tells the user when it happens.\n\nThe request was:\n")
	sb.WriteString(textutil.Brief(orig.Input, 1200))
	sb.WriteString("\n\nThe answer said:\n")
	sb.WriteString(textutil.Brief(orig.Result, 2000))
	sb.WriteString("\n\nWhat was promised:\n")
	for _, p := range watch {
		fmt.Fprintf(&sb, "- %s", p.Text)
		if p.Fix != "" {
			sb.WriteString(" — " + p.Fix)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nHow: find what is to be watched in the answer above (a download's task id or name, a release, a page) and a READ-ONLY tool that shows its state (tool_search finds the list or status tool of the service, for example the download list); then monitor_start with kind tool, the ids from the answer, an expect pattern for done, announce=true so the user sees progress, and a time budget that fits (a download: up to 24 hours). Watch every item that was promised, one monitor each, or one that covers a list. When several things are watched, do not start one monitor per item if one tool call shows them all. Reply with one line saying what is now being watched.")
	to := e.watcherAgent(ctx, orig.ToAgent)
	t, err := e.Enqueue(ctx, tasks.Task{FromKind: "system", FromName: promiseReviewName, ToAgent: to, Title: "Watch what was promised: " + textutil.Brief(orig.Title, 50), Input: sb.String()})
	if err != nil {
		return nil, err
	}
	_, _ = e.DB.Exec(ctx, `UPDATE promise_reviews SET status='fixing', fix_task_id=$2 WHERE id=$1`, reviewID, t.ID)
	e.Emit("promise.update", map[string]any{"id": reviewID})
	return &t, nil
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

// dropCovered removes the promises to "tell you when it happens" that a schedule or a watch already carries out: a job that promised to
// download an episode once it is out, with a schedule of that very name checking for it, kept its promise by making the schedule.
func (e *Engine) dropCovered(ctx context.Context, t tasks.Task, items []promiseItem) []promiseItem {
	type carrier struct{ name, body map[string]bool }
	var carriers []carrier
	if rows, err := e.DB.Query(ctx, `SELECT name, prompt FROM crons WHERE enabled UNION ALL SELECT description, predicate::text FROM intents WHERE status='active'`); err == nil {
		for rows.Next() {
			var n, b string
			if rows.Scan(&n, &b) == nil {
				carriers = append(carriers, carrier{tokenWords(n), tokenWords(n + " " + b)})
			}
		}
		rows.Close()
	}
	title := tokenWords(t.Title)
	var out []promiseItem
	for _, it := range items {
		if it.Kind != "watch" && it.Status != "needs_trigger" {
			out = append(out, it)
			continue
		}
		covered := false
		for _, c := range carriers {
			// the schedule is for this very job (its name is the task's title), or its text speaks of the promised thing
			if similarity(title, c.name) >= 0.6 || coverage(tokenWords(it.Text), c.body) >= 0.5 {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, it)
		}
	}
	return out
}

// coverage is the share of a's words that b also has.
func coverage(a, b map[string]bool) float64 {
	if len(a) == 0 {
		return 0
	}
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return float64(n) / float64(len(a))
}
