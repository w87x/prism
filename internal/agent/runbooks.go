package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"prism/internal/settings"
	"prism/internal/tasks"
)

// Runbooks. A routine job (a cron, a standing intent) runs on the small model to keep it cheap, and small models are the ones
// that drift. A real team solves that with a procedure: the senior does the job a couple of times, writes down how, and the
// junior follows it — and goes back to the senior when it no longer works. Here:
//
//	learning  the job runs on the main model; after two successful runs the main model distils a runbook from the second one
//	active    the small model runs the job with the runbook in front of it; failures and escalations count against it
//	stale     two failures in a row: the job goes back to the main model, whose next success rewrites the runbook
//
// A sampled audit watches the active ones: now and then the main model silently re-checks a small-model run.

type Runbook struct {
	ID         int64     `json:"id"`
	Key        string    `json:"key"`
	Agent      string    `json:"agent"`
	Title      string    `json:"title"`
	Steps      string    `json:"steps"`
	Status     string    `json:"status"`
	Version    int       `json:"version"`
	LearnWins  int       `json:"learn_wins"`
	Wins       int       `json:"wins"`
	Losses     int       `json:"losses"`
	FailStreak int       `json:"fail_streak"`
	Note       string    `json:"note"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const runbookCols = `id,key,agent,title,steps,status,version,learn_wins,wins,losses,fail_streak,last_note,updated_at`

func scanRunbook(row interface{ Scan(...any) error }) (Runbook, error) {
	var r Runbook
	err := row.Scan(&r.ID, &r.Key, &r.Agent, &r.Title, &r.Steps, &r.Status, &r.Version, &r.LearnWins, &r.Wins, &r.Losses, &r.FailStreak, &r.Note, &r.UpdatedAt)
	return r, err
}

// runbookKey names a routine job: where it came from and what it is called.
func runbookKey(t tasks.Task) string { return t.FromKind + ":" + t.FromName }

func (e *Engine) runbooksOn(ctx context.Context) bool {
	return !settings.Load(ctx, e.Settings, settings.KeyAutonomy, settings.DefaultAutonomy()).RunbooksOff
}

func (e *Engine) runbookOf(ctx context.Context, key string) (Runbook, bool) {
	r, err := scanRunbook(e.DB.QueryRow(ctx, `SELECT `+runbookCols+` FROM runbooks WHERE key=$1`, key))
	return r, err == nil
}

// Runbooks lists them all, for the settings page.
func (e *Engine) Runbooks(ctx context.Context) ([]Runbook, error) {
	rows, err := e.DB.Query(ctx, `SELECT `+runbookCols+` FROM runbooks ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Runbook{}
	for rows.Next() {
		if r, err := scanRunbook(rows); err == nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// SaveRunbook lets the user edit a runbook's steps (it becomes active) or switch it off.
func (e *Engine) SaveRunbook(ctx context.Context, id int64, steps, status string) error {
	steps = strings.TrimSpace(steps)
	switch status {
	case "active":
		if steps == "" {
			return errors.New("an active runbook needs steps")
		}
	case "off", "learning", "stale":
	default:
		return errors.New("unknown status")
	}
	_, err := e.DB.Exec(ctx, `UPDATE runbooks SET steps=$2, status=$3, version=version+1, fail_streak=0, learn_wins=CASE WHEN $3='learning' THEN 0 ELSE learn_wins END, updated_at=now() WHERE id=$1`, id, steps, status)
	return err
}

func (e *Engine) DeleteRunbook(ctx context.Context, id int64) error {
	_, err := e.DB.Exec(ctx, `DELETE FROM runbooks WHERE id=$1`, id)
	return err
}

// runbookGate says how a routine job should run now: whether the small model may be used, and the runbook text to give it.
// state is "" when runbooks do not apply (switched off, or the job does not run on the small model anyway).
func (e *Engine) runbookGate(ctx context.Context, t tasks.Task) (state string, rb Runbook) {
	if !routineTask(t) || !e.runbooksOn(ctx) || e.routineBaseRef(ctx, t) != "role:fast" {
		return "", rb
	}
	if r, ok := e.runbookOf(ctx, runbookKey(t)); ok {
		if r.Status == "off" {
			return "", r
		}
		return r.Status, r
	}
	return "learning", rb
}

const runbookNoteHead = "\n\n[Runbook for this job — written by the senior model from earlier successful runs. Follow it; if a step does not work as written or the situation is not covered, call escalate instead of improvising.]\n"

// afterRoutine is called once a routine task has finished: it keeps the runbook's books and samples runs for audit.
func (e *Engine) afterRoutine(ctx context.Context, t tasks.Task, state string, rb Runbook, onMain bool) {
	if state == "" {
		return
	}
	key := runbookKey(t)
	escalateMu.Lock()
	escalations := escalateUse[t.ID]
	escalateMu.Unlock()
	success := t.Status == tasks.Done && strings.TrimSpace(t.Result) != "" && !strings.HasPrefix(strings.TrimSpace(t.Result), "[partial")
	if _, declined := notCapable(t.Result); declined {
		success = false
	}
	switch state {
	case "learning", "stale":
		if !success || !onMain {
			return
		}
		_, _ = e.DB.Exec(ctx, `INSERT INTO runbooks (key,agent,title,status,learn_wins) VALUES ($1,$2,$3,'learning',1)
			ON CONFLICT (key) DO UPDATE SET learn_wins=runbooks.learn_wins+1, updated_at=now()`, key, t.ToAgent, t.Title)
		need := 2
		if state == "stale" {
			need = 1
		}
		r, _ := e.runbookOf(ctx, key)
		if r.LearnWins >= need {
			go e.writeRunbook(context.WithoutCancel(ctx), t, r)
		}
	case "active":
		if onMain {
			return // an earlier-than-expected fallback; the books are for small-model runs
		}
		if success && escalations == 0 {
			_, _ = e.DB.Exec(ctx, `UPDATE runbooks SET wins=wins+1, fail_streak=0, updated_at=now() WHERE key=$1`, key)
		} else {
			note := "the run failed"
			if success {
				note = fmt.Sprintf("the small model had to escalate %d time(s)", escalations)
			} else if t.Error != "" {
				note = brief(t.Error, 160)
			}
			e.runbookLoss(ctx, key, note)
		}
		if success {
			e.maybeAudit(ctx, t, rb)
		}
	}
	escalateMu.Lock()
	delete(escalateUse, t.ID)
	escalateMu.Unlock()
}

// runbookLoss counts a failure; the second in a row retires the runbook so the main model takes the job back.
func (e *Engine) runbookLoss(ctx context.Context, key, note string) {
	var streak int
	if e.DB.QueryRow(ctx, `UPDATE runbooks SET losses=losses+1, fail_streak=fail_streak+1, last_note=$2, updated_at=now() WHERE key=$1 RETURNING fail_streak`, key, note).Scan(&streak) != nil {
		return
	}
	if streak >= 2 {
		_, _ = e.DB.Exec(ctx, `UPDATE runbooks SET status='stale', learn_wins=0, updated_at=now() WHERE key=$1`, key)
		e.Emit("runbook.stale", map[string]any{"key": key, "note": note})
	}
}

const runbookPrompt = `You are the senior model writing a runbook for a small, cheap model that will repeat a routine job on its own. You get the job's instruction, what a successful run actually did (its tool calls and their results) and its final reply.
Write the runbook as short numbered steps the small model can follow without understanding the bigger picture:
- the exact tools to call, in order, with the arguments that worked (URLs, queries, selectors, parameters), using placeholders only where something genuinely changes between runs
- how to decide whether the condition the user cares about is met, with the concrete test
- what to reply when it IS met (the shape of the message) and when it is NOT (exactly NO_REPLY)
- what is known to go wrong on this job and what to do about it
- when to call escalate (anything the steps do not cover)
Under 250 words. No preamble. If an earlier runbook and the reason it failed are given, fix that cause.`

// writeRunbook has the main model distil the run that just succeeded into a runbook and activates it.
func (e *Engine) writeRunbook(ctx context.Context, t tasks.Task, old Runbook) {
	if t.SessionID == nil || e.LLM == nil {
		return
	}
	in := fmt.Sprintf("THE JOB:\n%s\n\nWHAT A SUCCESSFUL RUN DID:\n%s\n\nITS FINAL REPLY:\n%s", brief(t.Input, 1500), orNone(e.toolTrail(ctx, *t.SessionID)), brief(t.Result, 800))
	if old.Status == "stale" && old.Steps != "" {
		in += fmt.Sprintf("\n\nEARLIER RUNBOOK (it stopped working: %s):\n%s", orNone(old.Note), old.Steps)
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := e.LLM.Complete(cctx, e.LLM.ActionRef(cctx, "runbook", "chat"), runbookPrompt, in, false)
	steps := strings.TrimSpace(out)
	if err != nil || len(steps) < 40 {
		return
	}
	if len(steps) > 2400 {
		steps = steps[:2400]
	}
	_, _ = e.DB.Exec(ctx, `UPDATE runbooks SET steps=$2, status='active', version=version+1, fail_streak=0, learn_wins=0, wins=0, updated_at=now() WHERE key=$1`, runbookKey(t), steps)
	e.Emit("runbook.written", map[string]any{"key": runbookKey(t), "title": t.Title})
}

// ── sampled audit ──

const auditPrompt = `You are the senior model quietly re-checking one run of a routine job done by a small model. You get the job, the runbook it followed, what it actually did (tool calls and results) and its final reply.
Say whether the reply is right: the steps were really carried out (no claim the trail does not support), the condition was judged correctly, and the reply has the right shape (news only, or NO_REPLY).
Answer JSON only: {"ok":true|false,"problem":"what is wrong, one sentence (empty when ok)"}`

// maybeAudit re-checks a small-model run now and then: always the first two after a runbook goes live, then one in N.
func (e *Engine) maybeAudit(ctx context.Context, t tasks.Task, rb Runbook) {
	n := settings.Load(ctx, e.Settings, settings.KeyAutonomy, settings.DefaultAutonomy()).AuditEvery()
	if n == 0 || t.SessionID == nil || e.LLM == nil {
		return
	}
	if rb.Wins >= 2 && rand.Intn(n) != 0 {
		return
	}
	go func() {
		bg := context.WithoutCancel(ctx)
		cctx, cancel := context.WithTimeout(bg, 5*time.Minute)
		defer cancel()
		var v struct {
			OK      *bool  `json:"ok"`
			Problem string `json:"problem"`
		}
		in := fmt.Sprintf("THE JOB:\n%s\n\nRUNBOOK:\n%s\n\nWHAT IT DID:\n%s\n\nFINAL REPLY:\n%s", brief(t.Input, 1200), orNone(rb.Steps), orNone(e.toolTrail(bg, *t.SessionID)), brief(t.Result, 800))
		if err := e.LLM.CompleteJSON(cctx, e.LLM.ActionRef(cctx, "audit", "chat"), auditPrompt, in, &v); err != nil || v.OK == nil {
			return
		}
		if *v.OK {
			e.Tasks.SetAudit(bg, t.ID, "ok", "")
			return
		}
		e.Tasks.SetAudit(bg, t.ID, "problem", strings.TrimSpace(v.Problem))
		e.runbookLoss(bg, runbookKey(t), "audit: "+strings.TrimSpace(v.Problem))
	}()
}
