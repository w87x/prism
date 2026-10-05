package memory

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Evidence is one piece of support for, or testimony against, a fact. The ledger only grows: a fact's
// confidence and "multi-source confirmed" status are computed from it, so they can always be explained.
type Evidence struct {
	ID          int64     `json:"id"`
	FactID      int64     `json:"fact_id"`
	SourceRef   string    `json:"source_ref"`
	Group       string    `json:"group"`
	Supports    bool      `json:"supports"`
	Reliability float64   `json:"reliability"`
	Note        string    `json:"note,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}

// AddEvidence appends to the ledger. An identical (fact, ref, direction) is not recorded twice.
func (s *Service) AddEvidence(ctx context.Context, e Evidence) error {
	e.SourceRef = strings.TrimSpace(e.SourceRef)
	if e.FactID == 0 || e.SourceRef == "" {
		return errors.New("evidence needs a fact and a source")
	}
	if e.Reliability <= 0 || e.Reliability > 1 {
		e.Reliability = sourceReliability
	}
	_, err := s.db.Exec(ctx, `INSERT INTO memory_evidence(fact_id,source_ref,source_group,supports,reliability,note)
		SELECT $1,$2,$3,$4,$5,$6 WHERE NOT EXISTS (SELECT 1 FROM memory_evidence WHERE fact_id=$1 AND source_ref=$2 AND supports=$4)`,
		e.FactID, e.SourceRef, strings.TrimSpace(e.Group), e.Supports, float32(e.Reliability), brief(e.Note, 300))
	if err != nil {
		return err
	}
	s.refreshConfirmation(ctx, e.FactID)
	return nil
}

// Evidence lists a fact's ledger, newest first.
func (s *Service) Evidence(ctx context.Context, factID int64) ([]Evidence, error) {
	rows, err := s.db.Query(ctx, `SELECT id,fact_id,source_ref,source_group,supports,reliability,note,observed_at
		FROM memory_evidence WHERE fact_id=$1 ORDER BY observed_at DESC, id DESC`, factID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Evidence{}
	for rows.Next() {
		var e Evidence
		var rel float32
		if err := rows.Scan(&e.ID, &e.FactID, &e.SourceRef, &e.Group, &e.Supports, &rel, &e.Note, &e.ObservedAt); err != nil {
			return nil, err
		}
		e.Reliability = float64(rel)
		out = append(out, e)
	}
	return out, rows.Err()
}

// independentGroups counts the distinct source groups of supporting (or refuting) evidence. Evidence of unknown
// origin never counts as independent: it is one correlated group at best, and does not count here at all.
func (s *Service) independentGroups(ctx context.Context, factID int64, supports bool) int {
	var n int
	_ = s.db.QueryRow(ctx, `SELECT count(DISTINCT source_group) FROM memory_evidence WHERE fact_id=$1 AND supports=$2 AND source_group<>''`, factID, supports).Scan(&n)
	return n
}

// refreshConfirmation marks a fact multi_source_confirmed once two independent groups support it and no
// independent group refutes it. A user's own confirmation is never downgraded by this.
func (s *Service) refreshConfirmation(ctx context.Context, factID int64) {
	sup, ref := s.independentGroups(ctx, factID, true), s.independentGroups(ctx, factID, false)
	if sup >= 2 && ref == 0 {
		_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET confirmation='multi_source_confirmed' WHERE id=$1 AND confirmation='unconfirmed'`, factID)
	} else if ref > 0 {
		_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET confirmation='unconfirmed' WHERE id=$1 AND confirmation='multi_source_confirmed'`, factID)
	}
}
