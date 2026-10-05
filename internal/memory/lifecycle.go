package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync/atomic"
	"time"
)

// ── use receipts: retrieval alone must not make a fact "more important" ─────────────────────────────────────
//
// Find used to bump hits/rank/last_used on every fact it returned. The per-turn auto-recall returns the same
// handful of facts on every turn of a long chat, so those facts climbed to the maximum rank just by being
// retrieved — a popularity loop that crowds out everything else. A fact is now reinforced at most once per
// scope (a task, or a chat-day): the first time it is put to use in that piece of work.

// FindScope names the unit of work a retrieval belongs to ("" = unscoped: every call reinforces, as before).
func FindScope(kind string, id int64) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", kind, id)
}

// useScope picks the scope for a retrieval made on behalf of a task (preferred) or, failing that, a session
// for the current day.
func useScope(taskID, sessionID int64) string {
	if taskID != 0 {
		return FindScope("task", taskID)
	}
	return DayScope("session", sessionID)
}

// UseScope is useScope for callers outside the package.
func UseScope(taskID, sessionID int64) string { return useScope(taskID, sessionID) }

// DayScope is a scope for long-lived conversations that have no task of their own: one per chat per day.
func DayScope(kind string, id int64) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d:%s", kind, id, time.Now().UTC().Format("2006-01-02"))
}

// reinforceUse records the use of the picked facts and bumps those not yet reinforced in this scope; it returns
// the ids it actually reinforced.
func (s *Service) reinforceUse(ctx context.Context, pick []int64, scope string) []int64 {
	fresh := pick
	if scope != "" {
		fresh = fresh[:0:0]
		rows, err := s.db.Query(ctx, `INSERT INTO memory_use_receipts(fact_id,scope) SELECT unnest($1::bigint[]), $2
			ON CONFLICT DO NOTHING RETURNING fact_id`, pick, scope)
		if err != nil {
			return nil
		}
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				fresh = append(fresh, id)
			}
		}
		rows.Close()
	}
	if len(fresh) > 0 {
		_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET hits=hits+1, last_used=now(), rank=LEAST(rank+0.05,5) WHERE id=ANY($1)`, fresh)
	}
	return fresh
}

// ── expiry: volatile facts retire themselves ─────────────────────────────────────────────────────────────────
//
// "The RTX 5090 is $2,199 at Newegg" is true today and misleading in a month. A fact can carry an expiry
// (StoreReq.TTLDays); once it passes, the fact is retired exactly like a superseded one (valid_to is set to the
// expiry moment, so time-travel views stay right) — it is never deleted.

// clampTTL keeps a model-suggested lifetime sane: nothing shorter than a day, nothing longer than a year
// (a fact that lasts longer than that should not have an expiry at all).
func clampTTL(d int) int {
	switch {
	case d <= 0:
		return 0
	case d > 365:
		return 0
	}
	return d
}

const sweepEvery = time.Minute

var lastSweep atomic.Int64

// ExpireDue retires every fact whose expiry has passed; it reports how many.
func (s *Service) ExpireDue(ctx context.Context) int {
	tag, err := s.db.Exec(ctx, `WITH x AS (UPDATE memory_facts SET valid_to=expires_at, status='expired' WHERE expires_at IS NOT NULL AND expires_at<=now() AND valid_to IS NULL RETURNING id)
		INSERT INTO memory_audit(actor,action,fact_id,detail) SELECT 'system','expire',id,'lifetime ended' FROM x`)
	if err != nil {
		return 0
	}
	_, _ = s.db.Exec(ctx, `DELETE FROM memory_use_receipts WHERE at < now() - interval '30 days'`)
	n := int(tag.RowsAffected())
	if n > 0 {
		s.changed()
	}
	return n
}

// sweepExpired is ExpireDue at most once a minute, cheap enough to run at the start of every retrieval so an
// expired fact is never served even when no maintenance pass has run since.
func (s *Service) sweepExpired(ctx context.Context) {
	now := time.Now().UnixNano()
	last := lastSweep.Load()
	if now-last < int64(sweepEvery) || !lastSweep.CompareAndSwap(last, now) {
		return
	}
	s.ExpireDue(ctx)
}

// ── diversity: a packet of five paraphrases is one fact and four wasted slots ───────────────────────────────

const mmrLambda = 0.75 // 1 = pure relevance, 0 = pure novelty

// mmrSelect picks k of the scored candidates (sorted best first) by maximal marginal relevance: each next pick
// maximises  λ·score − (1−λ)·(similarity to what is already chosen). Similarity is the embedding cosine when
// both facts have one in memory, else word overlap. The very best candidate is always first.
func mmrSelect(cands []cand, sc []scoredIdx, k int) []scoredIdx {
	if len(sc) <= 1 || k <= 0 {
		return sc
	}
	pool := sc[:min(len(sc), k*4)]
	top := pool[0].score
	if top <= 0 {
		return sc
	}
	sim := func(a, b int) float64 {
		va, vb := cands[pool[a].i].vec, cands[pool[b].i].vec
		if len(va) > 0 && len(va) == len(vb) {
			return math.Max(0, Dot(va, vb))
		}
		return jaccard(cands[pool[a].i].text, cands[pool[b].i].text)
	}
	chosen := []int{0}
	left := map[int]bool{}
	for i := 1; i < len(pool); i++ {
		left[i] = true
	}
	for len(chosen) < k && len(left) > 0 {
		best, bestV := -1, math.Inf(-1)
		for i := range left {
			worst := 0.0
			for _, c := range chosen {
				worst = math.Max(worst, sim(i, c))
			}
			if v := mmrLambda*pool[i].score/top - (1-mmrLambda)*worst; v > bestV || (v == bestV && i < best) {
				best, bestV = i, v
			}
		}
		chosen = append(chosen, best)
		delete(left, best)
	}
	sort.Ints(chosen) // keep the original relevance order among the chosen
	out := make([]scoredIdx, 0, len(chosen))
	for _, i := range chosen {
		out = append(out, pool[i])
	}
	return out
}

// ── source health: is what a derived document was written from still true? ──────────────────────────────────

// SourceHealth says, for the facts a derived view (a knowledge page, a model) was built from, which have since
// been retired or have lapsed, which conclusions are stale, and until when the rest can be trusted. A view is
// stale as soon as ANY source is, so it never silently outlives its evidence.
type SourceHealth struct {
	Missing    []int64    `json:"missing,omitempty"`   // deleted outright
	Retired    []int64    `json:"retired,omitempty"`   // corrected, retracted, expired or archived
	Stale      []int64    `json:"stale,omitempty"`     // conclusions with a retired premise somewhere beneath
	Contested  []int64    `json:"contested,omitempty"` // a live fact contradicts them
	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

// Broken reports whether the view must not be served as current.
func (h SourceHealth) Broken() bool { return len(h.Missing)+len(h.Retired)+len(h.Stale) > 0 }

func (s *Service) SourceHealth(ctx context.Context, ids []int64) SourceHealth {
	var h SourceHealth
	if len(ids) == 0 {
		return h
	}
	rows, err := s.db.Query(ctx, `SELECT f.id, f.valid_to IS NOT NULL, f.expires_at, `+staleConclusionExpr("f")+`,
			EXISTS(SELECT 1 FROM memory_links ck JOIN memory_facts co ON co.id=CASE WHEN ck.a=f.id THEN ck.b ELSE ck.a END
				WHERE (ck.a=f.id OR ck.b=f.id) AND ck.kind='contradicts' AND co.valid_to IS NULL AND co.status<>'proposed')
		FROM memory_facts f WHERE f.id=ANY($1)`, ids)
	if err != nil {
		return h
	}
	defer rows.Close()
	have := map[int64]bool{}
	for rows.Next() {
		var id int64
		var retired, stale, contested bool
		var exp *time.Time
		if rows.Scan(&id, &retired, &exp, &stale, &contested) != nil {
			continue
		}
		have[id] = true
		switch {
		case retired:
			h.Retired = append(h.Retired, id)
		case stale:
			h.Stale = append(h.Stale, id)
		}
		if contested && !retired {
			h.Contested = append(h.Contested, id)
		}
		if exp != nil && !retired && (h.ValidUntil == nil || exp.Before(*h.ValidUntil)) {
			e := *exp
			h.ValidUntil = &e
		}
	}
	for _, id := range ids {
		if !have[id] {
			h.Missing = append(h.Missing, id)
		}
	}
	return h
}
