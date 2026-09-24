package memory

import (
	"context"
	"math"
	"slices"
)

// A fact learned from the web starts out unverified. When the same fact turns up on another independent site
// (a different registrable domain) its confidence rises, treating each site as an independent witness that is
// right with probability sourceReliability: 1 site 0.40, 2 sites 0.64, 3 sites 0.78. Two agreeing sites
// therefore lift a fact over the 0.5 line that separates "unverified" from usable (it may then serve as
// evidence for a conclusion). A trusted confirmation (the user, or an agent that had read nothing untrusted)
// lifts it at once. Repetition from one site counts once.
const (
	sourceReliability = 0.4
	maxWebConfidence  = 0.85
)

// corroborated is the confidence of a fact reported by n independent sites.
func corroborated(n int) float64 {
	if n <= 0 {
		return 0
	}
	return math.Min(maxWebConfidence, 1-math.Pow(1-sourceReliability, float64(n)))
}

// reinforce is called when a fact that is already known is stored again: it bumps its rank and, for
// web-learned facts, applies the corroboration rules. It reports whether confidence changed.
func (s *Service) reinforce(ctx context.Context, id int64, r StoreReq) bool {
	_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET rank=LEAST(rank+0.15,5), last_used=now() WHERE id=$1`, id)
	var conf float32
	var origins []string
	if err := s.db.QueryRow(ctx, `SELECT confidence, origins FROM memory_facts WHERE id=$1`, id).Scan(&conf, &origins); err != nil {
		return false
	}
	if conf >= 0.5 && len(origins) == 0 {
		return false // a trusted fact has nothing to prove
	}
	nc := float64(conf)
	switch {
	case r.Confidence >= 0.5 && conf < 0.5: // confirmed by a trusted source
		nc = r.Confidence
	case r.Origin != "" && !slices.Contains(origins, r.Origin):
		origins = append(origins, r.Origin)
		nc = math.Max(nc, corroborated(len(origins)))
	default:
		return false
	}
	if origins == nil {
		origins = []string{}
	}
	_, err := s.db.Exec(ctx, `UPDATE memory_facts SET confidence=$2::real, origins=$3,
		tags = CASE WHEN $2::real>=0.5 THEN array_remove(tags,'unverified') ELSE tags END WHERE id=$1`, id, float32(nc), origins)
	if err == nil {
		s.changed()
	}
	return err == nil
}
