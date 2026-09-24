package app

import (
	"context"
	"errors"
	"time"

	"prism/internal/maint"
	"prism/internal/settings"
	"prism/internal/tasks"
)

// ResetData starts PRISM over from the onboarding dialog. scope "data" drops everything PRISM produced but
// keeps API keys and integrations; "all" drops those too. Running work is cancelled first; the built-in
// agents and schedules are re-created and onboarding opens again.
func (a *App) ResetData(ctx context.Context, scope string) error {
	if a.DB == nil {
		return errors.New("not connected to a database")
	}
	for _, st := range []string{tasks.Running, tasks.Queued, tasks.WaitingInput} {
		ts, _ := a.Tasks.List(ctx, tasks.Filter{Status: st, Limit: 500})
		for _, t := range ts {
			_ = a.Engine.CancelTask(ctx, t.ID)
		}
	}
	time.Sleep(300 * time.Millisecond) // let cancelled runs unwind before their tables are emptied
	if err := maint.Reset(ctx, a.DB.Pool, scope, a.Cfg.DataDir); err != nil {
		return err
	}
	if err := a.Profiles.Seed(ctx); err != nil {
		return err
	}
	if a.Ext.Sched != nil {
		if err := a.Ext.Sched.SeedDefaults(ctx); err != nil {
			return err
		}
	}
	_ = a.Tools.Load(ctx)
	_ = a.Settings.Set(ctx, settings.KeyOnboarding, settings.Onboarding{Done: false})
	for _, ev := range []string{"agents.update", "memory.update", "task.update", "kb.update", "trackers.update", "briefing.new", "notifications.changed"} {
		a.Emit(ev, nil)
	}
	a.Emit("status", a.Status(ctx))
	return nil
}
