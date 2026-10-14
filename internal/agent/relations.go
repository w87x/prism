package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"prism/internal/settings"
	"prism/internal/tasks"
)

// What a team can borrow from how people work together.
//
//	trust      calls an agent makes to a tool earn going through without asking, gradually, from how its work turned out
//	repair     when the user says a result was bad: say what went wrong, fix what can be fixed, learn a lesson, and check later it held
//	handover   when a task stops, a short note on where it stands, so whoever continues does not start from raw transcripts
//	charter    "how we work here": one short document every agent is given

// ── trust ──

const (
	TrustUnknown    = "unknown"
	TrustTrusted    = "trusted"
	TrustDistrusted = "distrusted"
)

func trustLevelOf(wins, losses int) string {
	switch {
	case wins >= 5 && losses*4 <= wins:
		return TrustTrusted
	case losses >= 2 && losses*2 >= wins:
		return TrustDistrusted
	}
	return TrustUnknown
}

func (e *Engine) noteTrust(ctx context.Context, agent string, used []string, ok bool) {
	for _, t := range used {
		w, l := 1, 0
		if !ok {
			w, l = 0, 1
		}
		_, _ = e.DB.Exec(ctx, `INSERT INTO trust (agent, tool, wins, losses) VALUES ($1,$2,$3,$4) ON CONFLICT (agent, tool) DO UPDATE SET wins=trust.wins+$3, losses=trust.losses+$4, updated_at=now()`, agent, t, w, l)
	}
}

func (e *Engine) noteTrustLoss(ctx context.Context, agent, tool string, n int) {
	_, _ = e.DB.Exec(ctx, `INSERT INTO trust (agent, tool, losses) VALUES ($1,$2,$3) ON CONFLICT (agent, tool) DO UPDATE SET losses=trust.losses+$3, updated_at=now()`, agent, tool, n)
}

func (e *Engine) trustLevel(ctx context.Context, agent, tool string) (level string, wins, losses int) {
	if e.DB.QueryRow(ctx, `SELECT wins, losses FROM trust WHERE agent=$1 AND tool=$2`, agent, tool).Scan(&wins, &losses) != nil {
		return TrustUnknown, 0, 0
	}
	return trustLevelOf(wins, losses), wins, losses
}

type TrustRow struct {
	Agent  string `json:"agent"`
	Tool   string `json:"tool"`
	Wins   int    `json:"wins"`
	Losses int    `json:"losses"`
	Level  string `json:"level"`
}

func (e *Engine) TrustTable(ctx context.Context) []TrustRow {
	rows, err := e.DB.Query(ctx, `SELECT agent, tool, wins, losses FROM trust WHERE wins+losses>=2 ORDER BY (wins+losses) DESC LIMIT 120`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []TrustRow{}
	for rows.Next() {
		var r TrustRow
		if rows.Scan(&r.Agent, &r.Tool, &r.Wins, &r.Losses) == nil {
			r.Level = trustLevelOf(r.Wins, r.Losses)
			out = append(out, r)
		}
	}
	return out
}

func (e *Engine) ResetTrust(ctx context.Context, agent, tool string) {
	_, _ = e.DB.Exec(ctx, `DELETE FROM trust WHERE agent=$1 AND tool=$2`, agent, tool)
}

// ── apology and repair ──

const complaintPrompt = `The user rated an AI agent's result as bad. From the task and what the user said, write ONE lesson the agent can follow next time, and the words that would appear in a similar future request.
Answer JSON only: {"rule":"When <kind of job>, <what to do or avoid> (one sentence, at most 40 words)","triggers":["two to four lower-case words from the kind of request"]}
If the complaint is too vague to learn anything specific, answer {"rule":""}.`

// startRepair is what a thumbs-down sets in motion: the agent says what went wrong and fixes what it can, a lesson is kept, and a
// check a week later finds out whether it held.
func (e *Engine) startRepair(ctx context.Context, t tasks.Task, note string) {
	var open int
	_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM repairs WHERE task_id=$1 AND status='open'`, t.ID).Scan(&open)
	if open > 0 || t.FromKind == "system" && t.FromName == "repair" {
		return
	}
	note = strings.TrimSpace(note)
	input := fmt.Sprintf("The user rated the result of an earlier task as bad%s.\n\nTHE TASK (#%d “%s”):\n%s\n\nYOUR RESULT:\n%s\n\nIn two or three plain sentences tell the user what went wrong and what you will do differently — an honest account, not an excuse. If the result can be put right with your tools, do it now and say what you changed; if it cannot, say exactly what is missing. Your reply is sent to the user as it stands, so write only that message.",
		map[bool]string{true: ": " + note}[note != ""], t.ID, t.Title, brief(t.Input, 900), brief(t.Result, 900))
	rt, err := e.Enqueue(ctx, tasks.Task{FromKind: "system", FromName: "repair", ToAgent: t.ToAgent, Title: "Repair: " + brief(t.Title, 60), Input: input})
	var rid *int64
	if err == nil {
		rid = &rt.ID
	}
	_, _ = e.DB.Exec(ctx, `INSERT INTO repairs (task_id, agent, what, repair_task, due_at) VALUES ($1,$2,$3,$4, now()+interval '7 days')`, t.ID, t.ToAgent, note, rid)
	if note != "" {
		go e.antibodyFromComplaint(context.WithoutCancel(ctx), t, note)
	}
}

func (e *Engine) antibodyFromComplaint(ctx context.Context, t tasks.Task, note string) {
	if e.LLM == nil || e.LLM.RoleRef(ctx, "fast") == "" {
		return
	}
	var v struct {
		Rule     string   `json:"rule"`
		Triggers []string `json:"triggers"`
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := e.LLM.CompleteJSON(cctx, e.LLM.ActionRef(cctx, "recover", "fast"), complaintPrompt, fmt.Sprintf("TASK: %s\n%s\n\nTHE USER SAID: %s", t.Title, brief(t.Input, 600), note), &v); err != nil || strings.TrimSpace(v.Rule) == "" {
		return
	}
	_ = e.AddAntibody(ctx, t.ToAgent, v.Triggers, v.Rule, "the user's complaint about task #"+fmt.Sprint(t.ID))
}

// sweepRepairs settles repairs that are due: it held when the agent drew no further complaint and no similar task failed since.
func (e *Engine) sweepRepairs(ctx context.Context) {
	rows, err := e.DB.Query(ctx, `SELECT r.id, r.agent, r.task_id, r.created_at FROM repairs r WHERE r.status='open' AND r.due_at<now()`)
	if err != nil {
		return
	}
	type row struct {
		id, task int64
		agent    string
		since    time.Time
	}
	var due []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.agent, &r.task, &r.since) == nil {
			due = append(due, r)
		}
	}
	rows.Close()
	for _, r := range due {
		var bad int
		_ = e.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM task_meta m JOIN tasks t ON t.id=m.task_id WHERE t.to_agent=$1 AND m.rating=-1 AND m.rated_at>$2 AND t.id<>$3)
			+ (SELECT count(*) FROM tasks t WHERE t.to_agent=$1 AND t.status='failed' AND t.created_at>$2 AND t.from_name<>'repair')`, r.agent, r.since, r.task).Scan(&bad)
		st := "held"
		if bad > 0 {
			st = "failed"
		}
		_, _ = e.DB.Exec(ctx, `UPDATE repairs SET status=$2, checked_at=now() WHERE id=$1`, r.id, st)
	}
}

// sweepNature is the periodic housekeeping: expired lessons go, due repairs are judged.
func (e *Engine) sweepNature(ctx context.Context) {
	_, _ = e.DB.Exec(ctx, `DELETE FROM antibodies WHERE COALESCE(last_hit, created_at) < now() - make_interval(secs => $1)`, antibodyTTL.Seconds())
	e.sweepRepairs(ctx)
	e.checkEvolutionWatches(ctx)
}

// ── handover ──

const handoverPrompt = `An AI agent's task stopped before it was finished. Write a handover note for whoever continues it (a colleague or the same agent later), from the task and what the agent actually did.
Use exactly these four short lines, plain words, under 110 words in all:
State: where things stand now
Done: what is already achieved or found (names, ids, paths, numbers)
Watch out: what failed, what to avoid repeating, what is still unknown
Cares about: what the user wants out of this`

func (e *Engine) writeHandover(ctx context.Context, t tasks.Task) {
	if t.SessionID == nil || e.LLM == nil || e.LLM.RoleRef(ctx, "fast") == "" {
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "TASK: %s\nINSTRUCTION: %s\nENDED: %s %s %s\n\nWHAT IT DID:\n%s\nITS LAST WORDS:\n%s", t.Title, brief(t.Input, 900), t.Status, t.Error, t.Question, orNone(e.toolTrail(ctx, *t.SessionID)), brief(t.Result, 700))
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := e.LLM.Complete(cctx, e.LLM.ActionRef(cctx, "handover", "fast"), handoverPrompt, sb.String(), false)
	if note := strings.TrimSpace(out); err == nil && len(note) > 20 {
		e.Tasks.SetHandover(ctx, t.ID, note)
	}
}

// handoverNote renders a task's handover for an instruction that continues it ("" when there is none).
func (e *Engine) handoverNote(ctx context.Context, id int64) string {
	if h := e.Tasks.MetaOf(ctx, id).Handover; h != "" {
		return "\n\nHandover note from the earlier attempt:\n" + h
	}
	return ""
}

// ── charter ──

// Charter is "how we work here", given to every agent.
func (e *Engine) charterText(ctx context.Context) string {
	t := strings.TrimSpace(settings.Load(ctx, e.Settings, settings.KeyCharter, settings.Charter{}).Text)
	if r := []rune(t); len(r) > 1600 {
		t = string(r[:1600])
	}
	return t
}
