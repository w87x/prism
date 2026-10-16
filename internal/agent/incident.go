package agent

import (
	"context"
	"fmt"
	"time"

	"prism/internal/textutil"
)

// Incident mode. While no model can be reached (see llm.Health) background work is paused instead of failing over and over:
// the dispatcher claims nothing, crons are not fired, the user is told once, and a light probe finds out when the models are
// back — then everything resumes by itself and the user is told that too.

// LLMDown reports whether the models are currently unreachable.
func (e *Engine) LLMDown() bool {
	if e.LLM == nil {
		return false
	}
	d, _, _ := e.LLM.Down()
	return d
}

// NoteSkipped counts a background job that was not started because of the incident in progress.
func (e *Engine) NoteSkipped(ctx context.Context) {
	_, _ = e.DB.Exec(ctx, `UPDATE incidents SET skipped=skipped+1 WHERE ended_at IS NULL`)
}

// StartIncidentWatch wires the health callback and starts the probe that notices recovery.
func (e *Engine) StartIncidentWatch(ctx context.Context) {
	if e.LLM == nil {
		return
	}
	_, _ = e.DB.Exec(ctx, `UPDATE incidents SET ended_at=now() WHERE ended_at IS NULL`) // a restart starts from a clean slate
	e.LLM.OnHealth(func(down bool, reason string) {
		bg := context.WithoutCancel(ctx)
		e.Emit("llm.health", map[string]any{"down": down, "reason": reason})
		if down {
			_, _ = e.DB.Exec(bg, `INSERT INTO incidents (reason) VALUES ($1)`, reason)
			var queued int
			_ = e.DB.QueryRow(bg, `SELECT count(*) FROM tasks WHERE status='queued'`).Scan(&queued)
			e.Notify(bg, Notice{Level: "attention", Text: fmt.Sprintf("No model is reachable (%s). I paused background work — scheduled jobs are skipped and %d queued tasks wait — and will carry on by myself as soon as a model answers again. If this is LM Studio: load a model.", textutil.Brief(reason, 160), queued)})
			return
		}
		var skipped int
		_ = e.DB.QueryRow(bg, `UPDATE incidents SET ended_at=now() WHERE ended_at IS NULL RETURNING skipped`).Scan(&skipped)
		e.Notify(bg, Notice{Level: "info", Text: fmt.Sprintf("A model answers again — background work resumed (%d scheduled runs were skipped meanwhile).", skipped)})
		e.wake()
	})
	go func() {
		t := time.NewTicker(45 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if e.LLMDown() {
					_ = e.LLM.Probe(ctx) // a success flips the state through noteResult
				}
			}
		}
	}()
}
