package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/tools"
)

// Decision log. Choices that shape later work — "we use Plex, not Jellyfin", "the nightly job runs at 03:00 because the NAS sleeps
// after", "prices are checked on the mobile site" — are written down with their reason and a date to look at them again, so a
// later change of mind starts from why it was decided, not from a guess. Facts live in memory; this is for choices.

type Decision struct {
	ID        int64      `json:"id"`
	Title     string     `json:"title"`
	Decision  string     `json:"decision"`
	Rationale string     `json:"rationale"`
	By        string     `json:"by"`
	TaskID    *int64     `json:"task_id,omitempty"`
	Status    string     `json:"status"`
	Supersede *int64     `json:"supersedes,omitempty"`
	MadeAt    time.Time  `json:"made_at"`
	ReviewAt  *time.Time `json:"review_at,omitempty"`
	Due       bool       `json:"due"`
}

const decisionCols = `id,title,decision,rationale,by_agent,task_id,status,supersedes,made_at,review_at, (status='active' AND review_at IS NOT NULL AND review_at<now())`

func (e *Engine) scanDecisions(ctx context.Context, where string, args ...any) ([]Decision, error) {
	rows, err := e.DB.Query(ctx, `SELECT `+decisionCols+` FROM decisions `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Decision{}
	for rows.Next() {
		var d Decision
		if rows.Scan(&d.ID, &d.Title, &d.Decision, &d.Rationale, &d.By, &d.TaskID, &d.Status, &d.Supersede, &d.MadeAt, &d.ReviewAt, &d.Due) == nil {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// Decisions lists the log, newest first; status "" means all, "due" those whose review date has passed.
func (e *Engine) Decisions(ctx context.Context, status string) ([]Decision, error) {
	switch status {
	case "":
		return e.scanDecisions(ctx, `ORDER BY made_at DESC LIMIT 200`)
	case "due":
		return e.scanDecisions(ctx, `WHERE status='active' AND review_at IS NOT NULL AND review_at<now() ORDER BY review_at`)
	}
	return e.scanDecisions(ctx, `WHERE status=$1 ORDER BY made_at DESC LIMIT 200`, status)
}

// RecordDecision stores one decision; supersedes (an earlier decision's id) marks that one as replaced.
func (e *Engine) RecordDecision(ctx context.Context, d Decision) (int64, error) {
	d.Title, d.Decision, d.Rationale = strings.TrimSpace(d.Title), strings.TrimSpace(d.Decision), strings.TrimSpace(d.Rationale)
	if d.Title == "" || d.Decision == "" {
		return 0, errors.New("a decision needs a title and the decision itself")
	}
	if len(d.Decision) > 1200 || len(d.Rationale) > 1200 {
		return 0, errors.New("keep the decision and the reason each under 1200 characters")
	}
	var id int64
	if err := e.DB.QueryRow(ctx, `INSERT INTO decisions (title,decision,rationale,by_agent,task_id,supersedes,review_at) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		d.Title, d.Decision, d.Rationale, d.By, d.TaskID, d.Supersede, d.ReviewAt).Scan(&id); err != nil {
		return 0, err
	}
	if d.Supersede != nil {
		_, _ = e.DB.Exec(ctx, `UPDATE decisions SET status='superseded' WHERE id=$1 AND id<>$2`, *d.Supersede, id)
	}
	return id, nil
}

func (e *Engine) SetDecisionStatus(ctx context.Context, id int64, status string) error {
	if status != "active" && status != "closed" && status != "superseded" {
		return errors.New("unknown status")
	}
	_, err := e.DB.Exec(ctx, `UPDATE decisions SET status=$2 WHERE id=$1`, id, status)
	return err
}

// ExtendDecision pushes the review date out (the user looked at it and it still stands).
func (e *Engine) ExtendDecision(ctx context.Context, id int64, days int) error {
	if days <= 0 {
		days = 90
	}
	_, err := e.DB.Exec(ctx, `UPDATE decisions SET review_at=now()+make_interval(days => $2) WHERE id=$1`, id, days)
	return err
}

func (e *Engine) DeleteDecision(ctx context.Context, id int64) error {
	_, err := e.DB.Exec(ctx, `DELETE FROM decisions WHERE id=$1`, id)
	return err
}

func (e *Engine) toolDecisions() *tools.Tool {
	return &tools.Tool{
		Name: "decisions", Category: "agents", Risk: tools.RiskRead,
		Description: "The team's decision log. action=record: write down a choice that will shape later work — what was decided, WHY (the reason matters most), and when to look at it again (review_in_days, default 90) — e.g. which service to use, a schedule and its reason, a way of doing something the user approved. " +
			"Not for facts (memory_store) and not for every small step. If it replaces an earlier decision pass supersedes=<id>. action=search: look through past decisions (query optional; lists the active ones when empty) BEFORE changing a settled approach, and say why when you do. action=close: a decision no longer applies.",
		Params: tools.Obj("action", tools.Enum("action", "record | search | close", "record", "search", "close"),
			tools.Str("title", "record: short name of the decision"), tools.Str("decision", "record: what was decided"),
			tools.Str("why", "record: the reason it was decided this way"), tools.Int("review_in_days", "record: when to look at it again (default 90, 0 = never)"),
			tools.Int("supersedes", "record: id of the earlier decision this replaces"), tools.Str("query", "search: words to look for"), tools.Int("id", "close: the decision id")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				Action, Title, Decision, Why, Query string
				ReviewInDays                        *int `json:"review_in_days"`
				Supersedes                          int64
				ID                                  int64
			}](raw)
			if err != nil {
				return "", err
			}
			switch a.Action {
			case "record":
				d := Decision{Title: a.Title, Decision: a.Decision, Rationale: a.Why, By: env.Agent}
				if env.TaskID != 0 {
					d.TaskID = &env.TaskID
				}
				days := 90
				if a.ReviewInDays != nil {
					days = *a.ReviewInDays
				}
				if days > 0 {
					t := time.Now().AddDate(0, 0, days)
					d.ReviewAt = &t
				}
				if a.Supersedes > 0 {
					d.Supersede = &a.Supersedes
				}
				id, err := e.RecordDecision(ctx, d)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Decision #%d recorded.", id), nil
			case "close":
				if a.ID == 0 {
					return "", errors.New("id is required")
				}
				return "Closed.", e.SetDecisionStatus(ctx, a.ID, "closed")
			}
			var ds []Decision
			if q := strings.TrimSpace(a.Query); q != "" {
				ds, err = e.scanDecisions(ctx, `WHERE status<>'superseded' AND (title ILIKE $1 OR decision ILIKE $1 OR rationale ILIKE $1) ORDER BY made_at DESC LIMIT 12`, "%"+q+"%")
			} else {
				ds, err = e.scanDecisions(ctx, `WHERE status='active' ORDER BY made_at DESC LIMIT 12`)
			}
			if err != nil {
				return "", err
			}
			if len(ds) == 0 {
				return "No matching decisions.", nil
			}
			var sb strings.Builder
			for _, d := range ds {
				fmt.Fprintf(&sb, "#%d [%s] %s (%s, %s): %s — because: %s", d.ID, d.Status, d.Title, d.By, d.MadeAt.Format("2 Jan 2006"), d.Decision, orNone(d.Rationale))
				if d.Due {
					sb.WriteString(" [review due]")
				}
				sb.WriteString("\n")
			}
			return sb.String(), nil
		},
	}
}
