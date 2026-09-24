package scheduler

import (
	"context"
	"sort"
	"strings"
	"time"
)

// AuditEntry is one thing autonomy did on its own: a cron firing a task, a standing intent triggering, a
// watch reporting progress, or a dream briefing. Everything here already lives durably in tasks/intents/
// briefings — this is a read-only timeline over those three sources, not a new log of its own, so it can
// never drift from what actually happened.
type AuditEntry struct {
	Kind    string    `json:"kind"` // cron | intent | watch | dream
	Time    time.Time `json:"time"`
	Title   string    `json:"title"`
	Agent   string    `json:"agent"`
	Status  string    `json:"status"`
	Summary string    `json:"summary"`
	TaskID  int64     `json:"task_id,omitempty"`
}

// Audit returns the most recent autonomous activity, newest first.
func (s *Service) Audit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 300 {
		limit = 80
	}
	var out []AuditEntry

	trows, err := s.DB.Query(ctx, `SELECT id, from_kind, title, to_agent, status, COALESCE(NULLIF(result,''), error), created_at
		FROM tasks WHERE from_kind IN ('cron','intent') ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	for trows.Next() {
		var e AuditEntry
		if err := trows.Scan(&e.TaskID, &e.Kind, &e.Title, &e.Agent, &e.Status, &e.Summary, &e.Time); err != nil {
			trows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return nil, err
	}

	// watches report straight to Notify without a task row (see check(), tickIntents) — only their firings
	// (not every check) belong on an audit timeline.
	irows, err := s.DB.Query(ctx, `SELECT description, owner, progress, fired_at FROM intents
		WHERE type='watch' AND fired_at IS NOT NULL ORDER BY fired_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	for irows.Next() {
		var e AuditEntry
		if err := irows.Scan(&e.Title, &e.Agent, &e.Summary, &e.Time); err != nil {
			irows.Close()
			return nil, err
		}
		e.Kind = "watch"
		e.Status = "fired"
		out = append(out, e)
	}
	irows.Close()
	if err := irows.Err(); err != nil {
		return nil, err
	}

	brows, err := s.DB.Query(ctx, `SELECT agent, title, body, status, created_at FROM briefings ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	for brows.Next() {
		var e AuditEntry
		var body string
		if err := brows.Scan(&e.Agent, &e.Title, &body, &e.Status, &e.Time); err != nil {
			brows.Close()
			return nil, err
		}
		e.Kind = "dream"
		e.Summary = strings.TrimSpace(body)
		if len(e.Summary) > 400 {
			e.Summary = e.Summary[:400] + "…"
		}
		out = append(out, e)
	}
	brows.Close()
	if err := brows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
