package tasks

import (
	"context"
	"strings"
)

// Meta is what teammates add to a task beyond its run: the acceptance criteria its delegator set ("done means…"), whether the
// lead's check found them met, a sampled re-check of a routine run, and the user's thumbs up or down.
type Meta struct {
	Criteria    string `json:"criteria"`
	CriteriaMet *bool  `json:"criteria_met,omitempty"`
	Gap         string `json:"criteria_gap,omitempty"`
	Rating      int    `json:"rating"`
	RatingNote  string `json:"rating_note,omitempty"`
	Audit       string `json:"audit,omitempty"` // "" | ok | problem
	AuditNote   string `json:"audit_note,omitempty"`
}

func (s *Store) SetCriteria(ctx context.Context, id int64, criteria string) {
	if c := strings.TrimSpace(criteria); c != "" {
		_, _ = s.db.Exec(ctx, `INSERT INTO task_meta (task_id, criteria) VALUES ($1,$2) ON CONFLICT (task_id) DO UPDATE SET criteria=$2`, id, c)
	}
}

func (s *Store) SetCriteriaResult(ctx context.Context, id int64, met bool, gap string) {
	_, _ = s.db.Exec(ctx, `INSERT INTO task_meta (task_id, criteria_met, criteria_gap) VALUES ($1,$2,$3) ON CONFLICT (task_id) DO UPDATE SET criteria_met=$2, criteria_gap=$3`, id, met, gap)
}

func (s *Store) SetAudit(ctx context.Context, id int64, verdict, note string) {
	_, _ = s.db.Exec(ctx, `INSERT INTO task_meta (task_id, audit, audit_note) VALUES ($1,$2,$3) ON CONFLICT (task_id) DO UPDATE SET audit=$2, audit_note=$3`, id, verdict, note)
}

// Rate records the user's verdict on a task's result: 1 (good), -1 (bad) or 0 (clear it).
func (s *Store) Rate(ctx context.Context, id int64, rating int, note string) error {
	if rating > 1 {
		rating = 1
	} else if rating < -1 {
		rating = -1
	}
	_, err := s.db.Exec(ctx, `INSERT INTO task_meta (task_id, rating, rating_note, rated_at) VALUES ($1,$2,$3,now())
		ON CONFLICT (task_id) DO UPDATE SET rating=$2, rating_note=$3, rated_at=now()`, id, rating, strings.TrimSpace(note))
	return err
}

func (s *Store) MetaOf(ctx context.Context, id int64) Meta {
	var m Meta
	_ = s.db.QueryRow(ctx, `SELECT criteria, criteria_met, criteria_gap, rating, rating_note, audit, audit_note FROM task_meta WHERE task_id=$1`, id).
		Scan(&m.Criteria, &m.CriteriaMet, &m.Gap, &m.Rating, &m.RatingNote, &m.Audit, &m.AuditNote)
	return m
}
