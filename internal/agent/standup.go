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

// Standup. Once a week (Monday morning by default) the team reports to the user like a team does: what got done, what failed or
// was sent back, what the user rated badly, which runbooks were written or went stale, how long the models were out, which
// decisions are due for another look. The numbers come from the database; a model only phrases them. The user's thumbs up/down on
// results (tasks.rate) feed it, and a pattern of thumbs-down for one agent puts a review on Metis's desk.

type Standup struct {
	ID        int64          `json:"id"`
	Since     time.Time      `json:"since"`
	Until     time.Time      `json:"until"`
	Text      string         `json:"text"`
	Stats     map[string]any `json:"stats"`
	CreatedAt time.Time      `json:"created_at"`
}

type agentWeek struct {
	Agent                                string
	Done, Failed, Partial, Cancelled, In int
}

// BuildStandup gathers the week's numbers and writes the digest (without storing or sending it).
func (e *Engine) BuildStandup(ctx context.Context, since, until time.Time) (string, map[string]any) {
	stats := map[string]any{}
	var agents []agentWeek
	if rows, err := e.DB.Query(ctx, `SELECT to_agent,
			count(*) FILTER (WHERE status='done'), count(*) FILTER (WHERE status='failed'), count(*) FILTER (WHERE status='partial'),
			count(*) FILTER (WHERE status='cancelled'), COALESCE(sum(tokens_in+tokens_out),0)/1000
		FROM tasks WHERE created_at>=$1 AND created_at<$2 GROUP BY to_agent ORDER BY count(*) DESC`, since, until); err == nil {
		for rows.Next() {
			var a agentWeek
			if rows.Scan(&a.Agent, &a.Done, &a.Failed, &a.Partial, &a.Cancelled, &a.In) == nil {
				agents = append(agents, a)
			}
		}
		rows.Close()
	}
	stats["agents"] = agents
	var sb strings.Builder
	total := 0
	for _, a := range agents {
		total += a.Done + a.Failed + a.Partial + a.Cancelled
		fmt.Fprintf(&sb, "- %s: %d done", a.Agent, a.Done)
		if a.Failed+a.Partial > 0 {
			fmt.Fprintf(&sb, ", %d failed or unfinished", a.Failed+a.Partial)
		}
		fmt.Fprintf(&sb, " (~%dk tokens)\n", a.In)
	}
	if sb.Len() == 0 {
		sb.WriteString("(no tasks ran)\n")
	}
	facts := "TASKS BY AGENT (" + since.Format("2 Jan") + " – " + until.Format("2 Jan") + "):\n" + sb.String()

	list := func(title, q string, args ...any) {
		rows, err := e.DB.Query(ctx, q, args...)
		if err != nil {
			return
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var l string
			if rows.Scan(&l) == nil {
				lines = append(lines, "- "+l)
			}
		}
		if len(lines) > 0 {
			facts += "\n" + title + ":\n" + strings.Join(lines, "\n") + "\n"
			stats[title] = lines
		}
	}
	list("RATED DOWN BY THE USER", `SELECT '#'||t.id||' '||t.to_agent||': '||left(t.title,70)||COALESCE(NULLIF(' — '||m.rating_note,' — '),'') FROM task_meta m JOIN tasks t ON t.id=m.task_id WHERE m.rating=-1 AND m.rated_at>=$1 AND m.rated_at<$2`, since, until)
	var up int
	_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM task_meta WHERE rating=1 AND rated_at>=$1 AND rated_at<$2`, since, until).Scan(&up)
	if up > 0 {
		facts += fmt.Sprintf("\nRATED UP BY THE USER: %d results\n", up)
		stats["rated_up"] = up
	}
	list("DELEGATED RESULTS THAT MISSED THEIR ACCEPTANCE CRITERIA", `SELECT '#'||t.id||' '||t.to_agent||': '||left(m.criteria_gap,120) FROM task_meta m JOIN tasks t ON t.id=m.task_id WHERE m.criteria_met=false AND t.created_at>=$1 AND t.created_at<$2`, since, until)
	list("ROUTINE RUNS THE MAIN MODEL FOUND PROBLEMS WITH", `SELECT '#'||t.id||' '||t.title||': '||left(m.audit_note,120) FROM task_meta m JOIN tasks t ON t.id=m.task_id WHERE m.audit='problem' AND t.created_at>=$1 AND t.created_at<$2`, since, until)
	list("RUNBOOKS CHANGED", `SELECT title||' — '||status||' (v'||version||', '||wins||' good runs, '||losses||' bad)' FROM runbooks WHERE updated_at>=$1 AND updated_at<$2`, since, until)
	list("MODEL OUTAGES", `SELECT to_char(started_at,'Dy HH24:MI')||' for '||COALESCE(round(extract(epoch FROM (ended_at-started_at))/60)::text||' min','still ongoing')||' ('||skipped||' scheduled runs skipped): '||left(reason,80) FROM incidents WHERE started_at>=$1 AND started_at<$2`, since, until)
	list("DECISIONS MADE", `SELECT title||' — '||left(decision,100) FROM decisions WHERE made_at>=$1 AND made_at<$2 AND status<>'superseded'`, since, until)
	list("DECISIONS DUE FOR ANOTHER LOOK", `SELECT '#'||id||' '||title FROM decisions WHERE status='active' AND review_at IS NOT NULL AND review_at<$1`, until)
	list("PROMISES NOT KEPT (still open)", `SELECT agent||': '||left(title,80) FROM promise_reviews WHERE status='open'`)
	list("REPAIRS AFTER BAD RATINGS (checked a week later)", `SELECT agent||': task #'||task_id||' — '||status||CASE WHEN what<>'' THEN ' ('||left(what,60)||')' ELSE '' END FROM repairs WHERE (checked_at>=$1 AND checked_at<$2) OR (created_at>=$1 AND created_at<$2)`, since, until)
	if pt := e.PausedTools(); len(pt) > 0 {
		var l []string
		for _, r := range pt {
			l = append(l, "- "+r.Tool+": "+textutil.Brief(r.Last, 80))
		}
		facts += "\nTOOLS PAUSED RIGHT NOW AFTER REPEATED FAILURES:\n" + strings.Join(l, "\n") + "\n"
	}
	list("AGENTS WHOSE TRACK RECORD IS STRONGEST OR WEAKEST (fading trail weights)", `SELECT agent||': '||round((weight*power(0.5, extract(epoch FROM now()-updated_at)/86400/14))::numeric,1)||' ('||wins||' ok, '||losses||' bad)' FROM trails WHERE kind='*' AND wins+losses>=3 ORDER BY weight*power(0.5, extract(epoch FROM now()-updated_at)/86400/14) DESC LIMIT 6`)
	list("REVIEWER", `SELECT verdict||' ×'||count(*)||' (e.g. '||min(tool)||')' FROM review_log WHERE at>=$1 AND at<$2 GROUP BY verdict`, since, until)

	text := ""
	if e.LLM != nil && e.LLM.RoleRef(ctx, "chat") != "" || e.LLM != nil && e.LLM.RoleRef(ctx, "fast") != "" {
		cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		out, err := e.LLM.Complete(cctx, e.LLM.ActionRef(cctx, "standup", "fast"),
			"You write the weekly team standup of a personal AI assistant team for its user. Use ONLY the facts given; invent nothing. Short, plain, warm; no headline, no filler. Three short parts in this order, each only if there is something to say: what the team got done; what went wrong or needs the user (failures, bad ratings, missed criteria, problems found, outages, promises not kept); what is worth a look (due decisions, runbook changes). Mention agents by name. At most 160 words. Reply in the user's language if the facts hint at one, otherwise English.",
			facts, false)
		text = strings.TrimSpace(out)
		if err != nil {
			text = ""
		}
	}
	if text == "" {
		text = "Team standup\n\n" + facts
	}
	stats["tasks"] = total
	return text, stats
}

// RunStandup builds, stores and delivers the digest for the period.
func (e *Engine) RunStandup(ctx context.Context, since, until time.Time, deliver bool) (*Standup, error) {
	text, stats := e.BuildStandup(ctx, since, until)
	b, _ := json.Marshal(stats)
	s := &Standup{Since: since, Until: until, Text: text, Stats: stats}
	if err := e.DB.QueryRow(ctx, `INSERT INTO standups (since, until, text, stats) VALUES ($1,$2,$3,$4) RETURNING id, created_at`, since, until, text, b).Scan(&s.ID, &s.CreatedAt); err != nil {
		return nil, err
	}
	if deliver {
		e.Notify(ctx, Notice{Level: "info", Text: "📋 " + text})
	}
	return s, nil
}

func (e *Engine) Standups(ctx context.Context, limit int) ([]Standup, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := e.DB.Query(ctx, `SELECT id, since, until, text, stats, created_at FROM standups ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Standup{}
	for rows.Next() {
		var s Standup
		var raw []byte
		if rows.Scan(&s.ID, &s.Since, &s.Until, &s.Text, &raw, &s.CreatedAt) == nil {
			_ = json.Unmarshal(raw, &s.Stats)
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

func (e *Engine) standupConfig(ctx context.Context) settings.Standup {
	return settings.Load(ctx, e.Settings, settings.KeyStandup, settings.Standup{Weekday: 1, Hour: 9})
}

// StartStandups checks hourly whether this week's standup is due: on the chosen weekday, at or after the chosen hour, and not yet
// written today.
func (e *Engine) StartStandups(ctx context.Context, loc func(context.Context) *time.Location) {
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			e.sweepNature(ctx)
			cfg := e.standupConfig(ctx)
			if cfg.Off || e.LLMDown() {
				continue
			}
			now := time.Now().In(loc(ctx))
			if int(now.Weekday()) != cfg.Weekday || now.Hour() < cfg.Hour {
				continue
			}
			var last time.Time
			_ = e.DB.QueryRow(ctx, `SELECT COALESCE(max(until), 'epoch') FROM standups`).Scan(&last)
			if time.Since(last) < 5*24*time.Hour {
				continue
			}
			since := now.AddDate(0, 0, -7)
			if !last.IsZero() && last.Year() > 1971 && now.Sub(last) < 14*24*time.Hour {
				since = last
			}
			_, _ = e.RunStandup(context.WithoutCancel(ctx), since, now, true)
			_, _ = e.RefreshPersonModel(context.WithoutCancel(ctx))
		}
	}()
}

// ── ratings ──

// RateTask records the user's thumbs up (1) or down (-1) on a task's result; a pattern of downs for one agent asks Metis to look.
func (e *Engine) RateTask(ctx context.Context, id int64, rating int, note string) error {
	t, err := e.Tasks.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := e.Tasks.Rate(ctx, id, rating, note); err != nil {
		return err
	}
	if t.SessionID != nil { // trust follows how the user judged the work: the tools used share the verdict
		used := e.toolsUsed(ctx, *t.SessionID)
		switch {
		case rating > 0:
			e.noteTrust(ctx, t.ToAgent, used, true)
		case rating < 0:
			for _, u := range used {
				e.noteTrustLoss(ctx, t.ToAgent, u, 1)
			}
		}
	}
	if rating < 0 {
		e.startRepair(ctx, t, note)
	}
	if rating >= 0 || strings.EqualFold(t.ToAgent, "Metis") {
		return nil
	}
	var downs int
	_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM task_meta m JOIN tasks t ON t.id=m.task_id WHERE m.rating=-1 AND t.to_agent=$1 AND m.rated_at>now()-interval '14 days'`, t.ToAgent).Scan(&downs)
	if downs < 2 {
		return nil
	}
	var recent int
	_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE to_agent='Metis' AND title=$1 AND created_at>now()-interval '7 days'`, "Improve "+t.ToAgent).Scan(&recent)
	if recent > 0 {
		return nil
	}
	rows, _ := e.DB.Query(ctx, `SELECT t.id, left(t.title,60), m.rating_note FROM task_meta m JOIN tasks t ON t.id=m.task_id WHERE m.rating=-1 AND t.to_agent=$1 AND m.rated_at>now()-interval '14 days' ORDER BY m.rated_at DESC LIMIT 6`, t.ToAgent)
	var sb strings.Builder
	if rows != nil {
		for rows.Next() {
			var tid int64
			var title, n string
			if rows.Scan(&tid, &title, &n) == nil {
				fmt.Fprintf(&sb, "- task #%d “%s”%s\n", tid, title, map[bool]string{true: " — user said: " + n}[n != ""])
			}
		}
		rows.Close()
	}
	_, err = e.Enqueue(ctx, tasks.Task{FromKind: "system", FromName: "ratings", ToAgent: "Metis", Title: "Improve " + t.ToAgent,
		Input: fmt.Sprintf("The user rated %d results from agent %s as bad within two weeks:\n%sRead them with task_transcript(id=…), work out what %s keeps getting wrong, and propose a soul or tool improvement with evolve_propose only if the evidence is clear.", downs, t.ToAgent, sb.String(), t.ToAgent)})
	return err
}
