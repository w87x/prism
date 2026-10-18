package memory

import (
	"context"
	"fmt"
)

// A conclusion is "stale" when a premise under it was retired. Reflect only revisits the conclusions it drew itself and only when its model
// chooses to, so analysis insights and syntheses (and many reflection ones) stayed flagged for ever. HealConclusions settles them without a
// model, by plain rules on the conclusion's direct evidence:
//   - a premise that was replaced by a newer fact is swapped for its successor (the evidence still stands, in its current wording);
//   - a conclusion left with fewer than two live premises, or with more dead premises than live ones, is retired;
//   - otherwise the dead premises are dropped from its evidence, which clears the flag (the audit trail keeps which ones).
//
// Premises deeper in the chain are not looked at: when a lower conclusion is retired here, the one above it has a dead direct premise on the
// next pass.
type HealResult struct {
	Swapped int `json:"swapped"`
	Dropped int `json:"dropped"`
	Retired int `json:"retired"`
}

func (r HealResult) Changes() int { return r.Swapped + r.Dropped + r.Retired }

func (r HealResult) String() string {
	return fmt.Sprintf("stale conclusions: %d premises swapped for their successors, %d dead premises dropped, %d conclusions retired", r.Swapped, r.Dropped, r.Retired)
}

func (s *Service) HealConclusions(ctx context.Context) (HealResult, error) {
	var res HealResult
	rows, err := s.db.Query(ctx, `SELECT c.id FROM memory_facts c WHERE c.kind='conclusion' AND c.valid_to IS NULL AND NOT c.pinned AND c.confirmation<>'user_confirmed'
		AND EXISTS (SELECT 1 FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=c.id THEN e.b ELSE e.a END
			WHERE (e.a=c.id OR e.b=c.id) AND e.kind='evidence' AND x.valid_to IS NOT NULL AND `+levelExpr("x")+` < `+levelExpr("c")+`)`)
	if err != nil {
		return res, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		prem, err := s.premises(ctx, id)
		if err != nil {
			return res, err
		}
		live, dead := 0, []premise{}
		for _, p := range prem {
			if !p.dead {
				live++
				continue
			}
			if succ := s.successor(ctx, p.id); succ != 0 && succ != id {
				already := false
				for _, q := range prem {
					if q.id == succ {
						already = true
					}
				}
				if !already && s.Link(ctx, id, succ, LinkEvidence, "successor of a retired premise", "auto", 0.8) == nil {
					live++
				}
				res.Swapped++
			}
			dead = append(dead, p)
		}
		var gone []int64
		for _, p := range dead {
			x, y := order(id, p.id)
			if _, err := s.db.Exec(ctx, `DELETE FROM memory_links WHERE a=$1 AND b=$2 AND kind='evidence'`, x, y); err == nil {
				gone = append(gone, p.id)
			}
		}
		if live < 2 || len(dead) > live {
			if r, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now() WHERE id=$1 AND valid_to IS NULL`, id); err == nil && r.RowsAffected() > 0 {
				res.Retired++
				s.audit(ctx, "system", "retire", id, fmt.Sprintf("its evidence was retired (premises %v); %d live left", gone, live))
			}
			continue
		}
		res.Dropped += len(gone)
		s.audit(ctx, "system", "heal", id, fmt.Sprintf("retired premises %v dropped from its evidence; %d live remain", gone, live))
	}
	if res.Changes() > 0 {
		s.changed()
	}
	return res, nil
}

type premise struct {
	id   int64
	dead bool
}

// premises are the direct evidence of a conclusion that sit at a lower level than it.
func (s *Service) premises(ctx context.Context, id int64) ([]premise, error) {
	rows, err := s.db.Query(ctx, `SELECT x.id, x.valid_to IS NOT NULL FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=$1 THEN e.b ELSE e.a END
		JOIN memory_facts c ON c.id=$1 WHERE (e.a=$1 OR e.b=$1) AND e.kind='evidence' AND `+levelExpr("x")+` < `+levelExpr("c"), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []premise
	for rows.Next() {
		var p premise
		if rows.Scan(&p.id, &p.dead) == nil {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// successor follows superseded_by to the fact that replaced this one, if that is alive.
func (s *Service) successor(ctx context.Context, id int64) int64 {
	cur := id
	for i := 0; i < 5; i++ {
		var next *int64
		var gone bool
		if s.db.QueryRow(ctx, `SELECT superseded_by, valid_to IS NOT NULL FROM memory_facts WHERE id=$1`, cur).Scan(&next, &gone) != nil || next == nil {
			return 0
		}
		cur = *next
		var live bool
		if s.db.QueryRow(ctx, `SELECT valid_to IS NULL AND status<>'proposed' FROM memory_facts WHERE id=$1`, cur).Scan(&live) == nil && live {
			return cur
		}
	}
	return 0
}
