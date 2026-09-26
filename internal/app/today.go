package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"prism/internal/memory"
	"prism/internal/tasks"
)

// Today is the everyday hub: what needs attention, what's in progress, what just got produced, what's
// coming up, and which projects have gone quiet. It is pure aggregation over data the rest of the app
// already owns (tasks, briefings, proposals, memory project banks) — nothing here is new state.
type Today struct {
	NeedsAttention []TodayItem       `json:"needs_attention"`
	WorkingOn      []TodayWorking    `json:"working_on"`
	Produced       []TodayItem       `json:"produced"`
	Commitments    []TodayCommitment `json:"commitments"`
	Projects       []TodayProject    `json:"projects"`
}

// TodayItem is one thing to look at: a waiting task, an unread briefing, a hire/plugin/soul proposal
// awaiting a decision, or a finished piece of work. Ref matches the existing notification-ref convention
// (see internal/app/notify.go) so the UI can reuse its page-opening logic.
type TodayItem struct {
	Kind  string    `json:"kind"` // waiting_input | briefing | hire | plugin | proposal | task
	Title string    `json:"title"`
	Sub   string    `json:"sub,omitempty"`
	Ref   string    `json:"ref"`
	At    time.Time `json:"at"`
}

type TodayWorking struct {
	TaskID  int64     `json:"task_id"`
	Agent   string    `json:"agent"`
	Title   string    `json:"title"`
	Started time.Time `json:"started"`
}

type TodayCommitment struct {
	Kind  string    `json:"kind"` // cron | intent
	Title string    `json:"title"`
	Due   time.Time `json:"due"`
}

// TodayProject is an active project memory bank with no recent activity — a candidate for "what needs a
// next step". IdleDays is approximate (day granularity is enough for this).
type TodayProject struct {
	BankID     int64     `json:"bank_id"`
	Bank       string    `json:"bank"`
	Facts      int       `json:"facts"`
	LastActive time.Time `json:"last_active"`
	IdleDays   int       `json:"idle_days"`
}

// GetToday builds the hub. Every section is best-effort: a subsystem that is not configured (no scheduler,
// no plugins) or a query that fails simply contributes nothing to that section rather than failing the page.
func (a *App) GetToday(ctx context.Context) Today {
	// Every slice starts non-nil: Go marshals a nil slice as JSON null, not [], and the page reads
	// d.<section>.length unconditionally once loaded — a null there would throw, not just render empty.
	t := Today{
		NeedsAttention: []TodayItem{},
		WorkingOn:      []TodayWorking{},
		Produced:       []TodayItem{},
		Commitments:    []TodayCommitment{},
		Projects:       []TodayProject{},
	}

	if waiting, err := a.Tasks.List(ctx, tasks.Filter{Status: tasks.WaitingInput, Limit: 50}); err == nil {
		for _, w := range waiting {
			t.NeedsAttention = append(t.NeedsAttention, TodayItem{Kind: "waiting_input", Title: w.Title, Sub: w.Question, Ref: fmt.Sprintf("task:%d", w.ID), At: w.CreatedAt})
		}
	}
	if partial, err := a.Tasks.List(ctx, tasks.Filter{Status: tasks.Partial, Limit: 50}); err == nil {
		for _, w := range partial {
			if w.AcknowledgedAt != nil {
				continue
			}
			at := w.CreatedAt
			if w.FinishedAt != nil {
				at = *w.FinishedAt
			}
			t.NeedsAttention = append(t.NeedsAttention, TodayItem{Kind: "partial", Title: w.Title, Sub: "stopped before finishing (" + w.Error + ")", Ref: fmt.Sprintf("task:%d", w.ID), At: at})
		}
	}
	if failed, err := a.Tasks.List(ctx, tasks.Filter{Status: tasks.Failed, Limit: 30}); err == nil {
		for _, w := range failed {
			// only the user's own requests that failed recently: a failed delegated sub-task is its parent's business
			if w.AcknowledgedAt != nil || w.Depth != 0 || w.FromKind != "user" || w.FinishedAt == nil || time.Since(*w.FinishedAt) > 72*time.Hour {
				continue
			}
			t.NeedsAttention = append(t.NeedsAttention, TodayItem{Kind: "failed", Title: w.Title, Sub: "failed: " + trim(w.Error, 120), Ref: fmt.Sprintf("task:%d", w.ID), At: *w.FinishedAt})
		}
	}
	if a.Ext.Ingest != nil {
		for _, w := range a.Ext.Ingest.Waiting() {
			t.NeedsAttention = append(t.NeedsAttention, TodayItem{Kind: "ingest", Title: "Which one are you in “" + w.Name + "”?", Sub: fmt.Sprintf("a %s with %d participants — say who you are and PRISM learns from it", w.Kind, w.Participants), Ref: "ingest:" + w.Name, At: time.Now()})
		}
	}
	if a.Ext.Sched != nil {
		if briefs, err := a.Ext.Sched.Briefings(ctx, ""); err == nil {
			for _, b := range briefs {
				// unread ones, and ones that ask a question nobody answered yet; reading (or answering) clears the rest
				if b.Status == "dismissed" || (b.Status != "new" && !(b.Reply == "" && strings.Contains(b.Body, "?"))) {
					continue
				}
				t.NeedsAttention = append(t.NeedsAttention, TodayItem{Kind: "briefing", Title: b.Title, Sub: trim(b.Body, 140), Ref: fmt.Sprintf("briefing:%d", b.ID), At: b.CreatedAt})
			}
		}
	}
	if profiles, err := a.Profiles.List(ctx); err == nil {
		for _, p := range profiles {
			if p.Probation {
				t.NeedsAttention = append(t.NeedsAttention, TodayItem{Kind: "hire", Title: "Confirm hiring " + p.Name, Sub: p.Description, Ref: fmt.Sprintf("hire:%d", p.ID), At: p.CreatedAt})
			}
		}
	}
	if props, err := a.Profiles.Proposals(ctx, "pending"); err == nil {
		for _, p := range props {
			t.NeedsAttention = append(t.NeedsAttention, TodayItem{Kind: "proposal", Title: "Change proposed for " + p.Agent, Sub: p.Rationale, Ref: fmt.Sprintf("proposal:%d", p.ID), At: p.CreatedAt})
		}
	}
	if a.Ext.Plugins != nil {
		if pls, err := a.Ext.Plugins.List(ctx, false); err == nil {
			for _, p := range pls {
				if p.Status == "pending" {
					t.NeedsAttention = append(t.NeedsAttention, TodayItem{Kind: "plugin", Title: "Review new tool: " + p.Name, Sub: p.Description, Ref: "tools", At: p.CreatedAt})
				}
			}
		}
	}

	for _, r := range a.Engine.ActiveRuns() {
		if r.TaskID == 0 {
			continue // a bare chat run, not a task someone is waiting on
		}
		title := r.Task
		if title == "" {
			title = r.Title
		}
		t.WorkingOn = append(t.WorkingOn, TodayWorking{TaskID: r.TaskID, Agent: r.Agent, Title: title, Started: time.UnixMilli(r.Started)})
	}

	since := time.Now().Add(-24 * time.Hour)
	if done, err := a.Tasks.List(ctx, tasks.Filter{Status: tasks.Done, Limit: 40}); err == nil {
		for _, d := range done {
			if d.FinishedAt == nil || d.FinishedAt.Before(since) || d.Depth != 0 {
				continue // only top-level results — a delegated sub-task's own "done" is noise here
			}
			t.Produced = append(t.Produced, TodayItem{Kind: "task", Title: d.Title, Sub: trim(d.Result, 140), Ref: fmt.Sprintf("task:%d", d.ID), At: *d.FinishedAt})
		}
	}
	if a.Ext.Sched != nil {
		if briefs, err := a.Ext.Sched.Briefings(ctx, "delivered"); err == nil {
			for _, b := range briefs {
				if b.CreatedAt.Before(since) {
					continue
				}
				t.Produced = append(t.Produced, TodayItem{Kind: "briefing", Title: b.Title, Sub: trim(b.Body, 140), Ref: fmt.Sprintf("briefing:%d", b.ID), At: b.CreatedAt})
			}
		}
	}

	if a.Ext.Sched != nil {
		now := time.Now()
		if crons, err := a.Ext.Sched.Crons(ctx); err == nil {
			for _, c := range crons {
				if !c.Enabled || c.NextRun == nil || c.NextRun.After(now.Add(48*time.Hour)) {
					continue
				}
				t.Commitments = append(t.Commitments, TodayCommitment{Kind: "cron", Title: c.Name, Due: *c.NextRun})
			}
		}
		if intents, err := a.Ext.Sched.Intents(ctx, "active"); err == nil {
			for _, i := range intents {
				if i.NextDue.After(now.Add(48 * time.Hour)) {
					continue
				}
				t.Commitments = append(t.Commitments, TodayCommitment{Kind: "intent", Title: i.Description, Due: i.NextDue})
			}
		}
	}

	if a.Memory != nil {
		rows, err := a.DB.Pool.Query(ctx, `SELECT b.id, b.name, count(f.id), MAX(COALESCE(f.last_used, f.created_at))
			FROM memory_banks b LEFT JOIN memory_facts f ON f.bank_id = b.id AND f.valid_to IS NULL
			WHERE b.kind = $1 AND b.status = 'active' GROUP BY b.id, b.name ORDER BY 4 ASC NULLS FIRST LIMIT 20`, memory.KindProject)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var p TodayProject
				var last *time.Time
				if err := rows.Scan(&p.BankID, &p.Bank, &p.Facts, &last); err != nil {
					continue
				}
				if last != nil {
					p.LastActive = *last
					p.IdleDays = int(time.Since(*last).Hours() / 24)
				}
				if p.IdleDays >= 3 { // fresh projects are not "awaiting a next step" yet
					t.Projects = append(t.Projects, p)
				}
			}
		}
	}

	return t
}
