package app

import (
	"context"
	"fmt"
	"prism/internal/tools/builtin"
	"sync"
	"time"

	"prism/internal/agent"
	"prism/internal/maint"
	"prism/internal/notify"
	"prism/internal/settings"
	"prism/internal/tasks"
)

var notifiedTasks sync.Map // task id → struct{}: unrecoverable failures are reported once

// Notify records a notification and, when configured for its kind, pushes it to macOS/Telegram.
func (a *App) Notify(kind, level, title, text string) { a.notifyRef(kind, level, title, text, "") }

// notifyRef is Notify with an explicit page hint (e.g. "proposal:12" opens that proposal's review).
func (a *App) notifyRef(kind, level, title, text, ref string) {
	if !a.Ready() {
		return
	}
	ctx := context.Background()
	cfg := settings.Load(ctx, a.Settings, settings.KeyNotify, settings.DefaultNotify()).Of(kind)
	if !cfg.Show {
		return
	}
	if ref == "" {
		ref = map[string]string{"cron": "autonomy", "intent": "autonomy", "ask": "chat", "error": "tasks", "proposal": "agents"}[kind]
	}
	it, err := a.Notifs.Add(ctx, notify.Item{Kind: kind, Level: level, Title: title, Text: text, Ref: ref})
	if err != nil {
		return
	}
	a.Hub.Broadcast("notification", it)
	if cfg.External && kind != "ask" { // asks are already delivered by the sinks themselves
		a.Engine.NotifySinks(ctx, agent.Notice{Agent: "PRISM", Text: title + ": " + text, Level: level})
	}
}

// observe turns backend events into notifications.
func (a *App) observe(typ string, data any) {
	switch typ {
	case "ask.request":
		if m, ok := data.(map[string]any); ok {
			who, _ := m["agent"].(string)
			txt, _ := m["text"].(string)
			title := who + " needs you"
			if k, _ := m["kind"].(string); k == "confirm" {
				title = who + " asks for approval"
			}
			id, _ := m["id"].(int64)
			go a.notifyRef("ask", "attention", title, txt, fmt.Sprintf("ask:%d", id))
		}
	case "agent.hired": // a hire made by an agent starts on probation: tell the user so they can confirm or let it go
		if m, ok := data.(map[string]any); ok {
			name, _ := m["name"].(string)
			by, _ := m["by"].(string)
			desc, _ := m["description"].(string)
			id, _ := m["id"].(int64)
			go a.notifyRef("proposal", "attention", by+" hired "+name+" (on probation)", trim(desc, 200), fmt.Sprintf("hire:%d", id))
		}
	case "plugin.pending": // an agent wrote a tool: it does nothing until the user reads the code and approves it
		if m, ok := data.(map[string]any); ok {
			name, _ := m["name"].(string)
			by, _ := m["by"].(string)
			desc, _ := m["description"].(string)
			go a.notifyRef("proposal", "attention", by+" wrote a plugin: "+name, trim(desc, 200), "tools")
		}
	case "ask.done": // answered (in the UI, Telegram, anywhere) or cancelled: its notification is no longer news
		if m, ok := data.(map[string]any); ok {
			if id, _ := m["id"].(int64); id != 0 && a.Ready() {
				go func() {
					if n, _ := a.Notifs.MarkReadRef(context.Background(), fmt.Sprintf("ask:%d", id)); n > 0 {
						a.Hub.Broadcast("notifications.changed", nil)
					}
				}()
			}
		}
	case "evolution.proposal":
		m, ok := data.(map[string]any)
		if !ok {
			return
		}
		who, _ := m["agent"].(string)
		kind, _ := m["kind"].(string)
		why, _ := m["rationale"].(string)
		id, _ := m["id"].(int64)
		ref := fmt.Sprintf("proposal:%d", id)
		if applied, _ := m["applied"].(bool); applied {
			go a.notifyRef("proposal", "info", who+" evolved ("+kind+")", why, "agents")
			return
		}
		go a.notifyRef("proposal", "attention", "Change proposed for "+who+" ("+kind+")", why, ref)
	case "task.update":
		t, ok := data.(tasks.Task)
		if !ok || t.Status != tasks.Failed || t.Depth != 0 {
			return // sub-agent failures are handled by their parent; only top-level ones are final
		}
		if _, dup := notifiedTasks.LoadOrStore(t.ID, struct{}{}); dup {
			return
		}
		go a.Notify("error", "error", t.ToAgent+" failed", trim(t.Title+": "+t.Error, 300))
	}
}

func trim(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// cleanupLoop applies the retention settings hourly.
func (a *App) cleanupLoop(ctx context.Context) {
	run := func() { _, _ = a.Cleanup(ctx) }
	select {
	case <-time.After(30 * time.Second):
		run()
	case <-ctx.Done():
		return
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// Cleanup deletes logs and finished tasks past their TTL and prunes old notifications.
func (a *App) Cleanup(ctx context.Context) (maint.Result, error) {
	rt := settings.Load(ctx, a.Settings, settings.KeyRetention, settings.DefaultRetention())
	res, err := maint.Cleanup(ctx, a.DB.Pool, rt.LogsDays, rt.TasksDays, rt.TaskStatuses...)
	_, _ = a.Notifs.Prune(ctx, 30*24*time.Hour)
	_, _ = builtin.PurgeArtifacts(ctx, a.DB.Pool)
	_, _ = builtin.PruneProcesses(ctx, a.DB.Pool)
	a.Engine.PurgeMergedChats(ctx, 30*24*time.Hour) // merged-away chats stay restorable for a month
	a.Metrics.Prune(ctx)
	_, _, _ = builtin.DecayBookmarks(ctx, a.DB.Pool) // self-gating to once/day; safe on every hourly tick
	return res, err
}
