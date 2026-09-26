package app

import (
	"context"
	"fmt"
	"time"

	"prism/internal/agent"
	"prism/internal/settings"
)

// stallLoop tells the user when a running task has shown no sign of life for a while (a hung model server, a tool
// that never returns, a sleeping machine). It only notifies — stopping is the user's call — and says so once per run.
func (a *App) stallLoop(ctx context.Context) {
	told := map[int64]time.Time{}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cfg := settings.Load(ctx, a.Settings, settings.KeyGuardrails, settings.DefaultGuardrails())
		if cfg.StallOff {
			continue
		}
		mins := cfg.StallMin
		if mins <= 0 {
			mins = 20
		}
		live := map[int64]bool{}
		for _, r := range a.Engine.StalledRuns(time.Duration(mins) * time.Minute) {
			live[r.ID] = true
			if _, ok := told[r.ID]; ok {
				continue
			}
			told[r.ID] = time.Now()
			idle := time.Since(time.UnixMilli(r.LastActive)).Round(time.Minute)
			what := r.Task
			if what == "" {
				what = r.Title
			}
			a.Engine.Notify(ctx, agent.Notice{Agent: r.Agent, Level: "warning",
				Text: fmt.Sprintf("“%s” has shown no activity for %s — no model call, tool call or output. It may be stuck (a hung model server or tool). Open Tasks to stop it, or leave it if it is a long wait.", brief(what, 70), idle)})
		}
		for id := range told { // forget runs that ended or woke up
			if !live[id] {
				delete(told, id)
			}
		}
	}
}

func brief(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
