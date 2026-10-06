package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Cross-bank analysis. Deep analysis (analyze.go) reads one bank at a time, so it can never notice that the user bank
// says "lives in Munich" while a project bank says "moved to Berlin", that two banks hold the same fact in different
// words, or that two areas share a pattern. This pass looks for those across banks: it takes the facts that arrived
// since its last run, finds the closest facts in OTHER banks (by embedding), and asks the model to judge each pair —
// contradiction (flagged for review like any other), duplicate (the keeper becomes visible in the other bank and the
// copy is retired), corroboration (a supports link) — and to name a few connections that only show across the banks.
const (
	DefaultCrossMin   = 5    // new facts needed before it runs on its own
	crossFresh        = 80   // fresh facts examined per pass
	crossNeighbours   = 3    // closest facts in other banks per fresh fact
	crossMinSim       = 0.62 // below this a pair is not worth the model's time
	crossDuplicateSim = 0.8  // below this "duplicate" is not believed
	crossMaxPairs     = 40
	crossRanKey       = 90 // the memory_synth row that remembers when this pass last ran
)

const crossPrompt = `You are the cross-checking mind of an assistant's long-term memory, which is split into banks (the user, projects, topics). Each PAIR below holds two facts from DIFFERENT banks that are about nearly the same thing.

Judge every pair:
- "contradiction": they cannot both be true now (not merely different aspects). Say why in "note".
- "duplicate": they say the same thing in different words; "keep" is the id of the better-worded one.
- "corroborates": they independently agree, or one clearly supports the other.
- "unrelated": similar wording but nothing to conclude. (Use this freely.)
Then, only if the pairs together show something that appears only across banks, add at most 3 "connections": one self-contained third-person sentence each, citing fact ids from at least two different banks. Never invent; cite only the ids you were given.
Answer JSON only: {"pairs":[{"n":1,"verdict":"contradiction|duplicate|corroborates|unrelated","keep":0,"note":""}],"connections":[{"text":"...","evidence":[ids],"confidence":0.0}]}`

type CrossResult struct {
	Fresh          int    `json:"fresh"`
	Pairs          int    `json:"pairs"`
	Contradictions int    `json:"contradictions"`
	Duplicates     int    `json:"duplicates"`
	Corroborations int    `json:"corroborations"`
	Connections    int    `json:"connections"`
	Skipped        string `json:"skipped,omitempty"`
}

func (r CrossResult) Changes() int {
	return r.Contradictions + r.Duplicates + r.Corroborations + r.Connections
}

func (r CrossResult) String() string {
	if r.Skipped != "" {
		return "cross-bank analysis: skipped (" + r.Skipped + ")"
	}
	return fmt.Sprintf("cross-bank analysis: %d pairs from %d new facts — %d contradictions, %d duplicates merged, %d corroborations, %d connections",
		r.Pairs, r.Fresh, r.Contradictions, r.Duplicates, r.Corroborations, r.Connections)
}

type crossPair struct {
	a, b         int64
	sim          float64
	aBank, bBank int64
}

type crossFact struct {
	id, bank  int64
	text      string
	bankLabel string
	pinned    bool
	userConf  bool
}

// AnalyzeAcross runs the pass. force ignores the "enough new facts" threshold.
func (s *Service) AnalyzeAcross(ctx context.Context, force bool, minNew int) (CrossResult, error) {
	var res CrossResult
	if s.llm.RoleRef(ctx, "chat") == "" && s.llm.RoleRef(ctx, "fast") == "" {
		res.Skipped = "no model configured"
		return res, nil
	}
	if minNew <= 0 {
		minNew = DefaultCrossMin
	}
	var banks int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM memory_banks WHERE status='active'`).Scan(&banks)
	if banks < 2 {
		res.Skipped = "fewer than two banks"
		return res, nil
	}
	fresh, err := s.crossFreshIDs(ctx)
	if err != nil {
		return res, err
	}
	res.Fresh = len(fresh)
	if len(fresh) == 0 || (!force && len(fresh) < minNew) {
		res.Skipped = fmt.Sprintf("only %d new facts", len(fresh))
		return res, nil
	}
	pairs, err := s.crossPairs(ctx, fresh)
	if err != nil {
		return res, err
	}
	// a contradiction that is already on record needs no second opinion
	kept := pairs[:0]
	for _, p := range pairs {
		var have bool
		x, y := order(p.a, p.b)
		_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_links WHERE a=$1 AND b=$2 AND kind=$3)`, x, y, LinkContradicts).Scan(&have)
		if !have {
			kept = append(kept, p)
		}
	}
	pairs = kept
	if len(pairs) > crossMaxPairs {
		pairs = pairs[:crossMaxPairs]
	}
	res.Pairs = len(pairs)
	if len(pairs) == 0 {
		s.markCrossRan(ctx)
		res.Skipped = "no close facts in other banks"
		return res, nil
	}
	ids := map[int64]bool{}
	for _, p := range pairs {
		ids[p.a], ids[p.b] = true, true
	}
	facts, err := s.crossFacts(ctx, ids)
	if err != nil {
		return res, err
	}
	var sb strings.Builder
	for i, p := range pairs {
		a, b := facts[p.a], facts[p.b]
		fmt.Fprintf(&sb, "PAIR %d (similarity %.2f)\n  %d [%s]: %s\n  %d [%s]: %s\n", i+1, p.sim, a.id, a.bankLabel, a.text, b.id, b.bankLabel, b.text)
	}
	var parsed struct {
		Pairs []struct {
			N       int    `json:"n"`
			Verdict string `json:"verdict"`
			Keep    int64  `json:"keep"`
			Note    string `json:"note"`
		} `json:"pairs"`
		Connections []struct {
			Text       string  `json:"text"`
			Evidence   []int64 `json:"evidence"`
			Confidence float64 `json:"confidence"`
		} `json:"connections"`
	}
	role := "role:chat"
	if s.llm.RoleRef(ctx, "chat") == "" {
		role = "role:fast"
	}
	if err := s.llm.CompleteJSON(ctx, role, crossPrompt, s.guidance(ctx)+sb.String(), &parsed); err != nil {
		return res, fmt.Errorf("cross-bank analysis: %w", err)
	}
	for _, v := range parsed.Pairs {
		if v.N < 1 || v.N > len(pairs) {
			continue
		}
		p := pairs[v.N-1]
		a, b := facts[p.a], facts[p.b]
		note := strings.TrimSpace(v.Note)
		switch strings.ToLower(strings.TrimSpace(v.Verdict)) {
		case "contradiction":
			if s.Link(ctx, p.a, p.b, LinkContradicts, note, "auto", 0.7) == nil {
				res.Contradictions++
			}
		case "corroborates":
			if s.Link(ctx, p.a, p.b, LinkSupports, note, "auto", 0.6) == nil {
				res.Corroborations++
			}
		case "duplicate":
			if p.sim < crossDuplicateSim || (v.Keep != p.a && v.Keep != p.b) {
				continue
			}
			keep, drop := a, b
			if v.Keep == p.b {
				keep, drop = b, a
			}
			if drop.pinned || drop.userConf { // never retire what the user vouched for or pinned; keep the better one instead
				if keep.pinned || keep.userConf {
					_ = s.Link(ctx, p.a, p.b, LinkRelated, "same fact in two banks", "auto", 0.8)
					continue
				}
				keep, drop = drop, keep
			}
			if s.mergeAcross(ctx, keep, drop) {
				res.Duplicates++
			}
		}
	}
	if len(parsed.Connections) > 3 {
		parsed.Connections = parsed.Connections[:3]
	}
	for _, c := range parsed.Connections {
		text := strings.TrimSpace(c.Text)
		if len(text) > 400 {
			text = text[:400]
		}
		var ev []int64
		banksSeen := map[int64]bool{}
		for _, id := range c.Evidence {
			if f, ok := facts[id]; ok {
				ev = append(ev, id)
				banksSeen[f.bank] = true
			}
		}
		if text == "" || len(ev) < 2 || len(banksSeen) < 2 {
			continue // a connection must rest on facts from at least two banks
		}
		if similarConclusion(text, s.knownConnections(ctx)) != nil {
			continue
		}
		bankOf := map[int64]int64{}
		for id, f := range facts {
			bankOf[id] = f.bank
		}
		home := s.homeBank(ctx, 2, ev, bankOf)
		if _, err := s.insertDerived(ctx, home, text, ev, c.Confidence, nil, "analysis", []string{"insight", "connection"}, maxConclusionConf); err == nil {
			res.Connections++
		}
	}
	s.markCrossRan(ctx)
	if res.Changes() > 0 {
		s.changed()
	}
	return res, nil
}

// AnalyzeAcrossDue is the scheduled entry: it only runs once enough new facts have arrived.
func (s *Service) AnalyzeAcrossDue(ctx context.Context, minNew int) (CrossResult, error) {
	return s.AnalyzeAcross(ctx, false, minNew)
}

func (s *Service) markCrossRan(ctx context.Context) {
	_, _ = s.db.Exec(ctx, `INSERT INTO memory_synth(level,ran_at) VALUES($1,now()) ON CONFLICT (level) DO UPDATE SET ran_at=now()`, crossRanKey)
}

// crossFreshIDs: trusted facts stored since the last cross-bank pass.
func (s *Service) crossFreshIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT f.id FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id AND b.status='active'
		WHERE f.kind='fact' AND f.valid_to IS NULL AND f.status<>'proposed' AND f.confidence>=0.5
		AND f.created_at > COALESCE((SELECT ran_at FROM memory_synth WHERE level=$1),'epoch')
		ORDER BY f.created_at DESC LIMIT $2`, crossRanKey, crossFresh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// crossPairs finds, for each fresh fact, its closest facts in banks it does not belong to.
func (s *Service) crossPairs(ctx context.Context, fresh []int64) ([]crossPair, error) {
	seen := map[[2]int64]bool{}
	var out []crossPair
	add := func(a, b, ab, bb int64, sim float64) {
		if sim < crossMinSim || a == b || ab == bb {
			return
		}
		x, y := order(a, b)
		if seen[[2]int64{x, y}] {
			return
		}
		seen[[2]int64{x, y}] = true
		out = append(out, crossPair{a: a, b: b, sim: sim, aBank: ab, bBank: bb})
	}
	if s.VectorOn {
		rows, err := s.db.Query(ctx, `SELECT f.id, f.bank_id, n.id, n.bank_id, n.sim FROM memory_facts f
			CROSS JOIN LATERAL (
				SELECT o.id, o.bank_id, 1 - (o.vec <=> f.vec) AS sim FROM memory_facts o JOIN memory_banks ob ON ob.id=o.bank_id AND ob.status='active'
				WHERE o.kind='fact' AND o.valid_to IS NULL AND o.status<>'proposed' AND o.confidence>=0.5 AND o.vec IS NOT NULL
				  AND vector_dims(o.vec)=vector_dims(f.vec) AND o.bank_id<>f.bank_id
				  AND NOT EXISTS (SELECT 1 FROM memory_fact_banks mb WHERE mb.fact_id=o.id AND mb.bank_id=f.bank_id)
				  AND NOT EXISTS (SELECT 1 FROM memory_fact_banks mb WHERE mb.fact_id=f.id AND mb.bank_id=o.bank_id)
				ORDER BY o.vec <=> f.vec LIMIT $2) n
			WHERE f.id=ANY($1) AND f.vec IS NOT NULL`, fresh, crossNeighbours)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var a, ab, b, bb int64
			var sim float64
			if rows.Scan(&a, &ab, &b, &bb, &sim) == nil {
				add(a, b, ab, bb, sim)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else {
		var bankIDs []int64
		bs, err := s.Banks(ctx)
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			if b.Status == "active" {
				bankIDs = append(bankIDs, b.ID)
			}
		}
		cands, err := s.candidates(ctx, bankIDs, false, 4000)
		if err != nil {
			return nil, err
		}
		isFresh := map[int64]bool{}
		for _, id := range fresh {
			isFresh[id] = true
		}
		for _, f := range cands {
			if !isFresh[f.id] || f.vec == nil {
				continue
			}
			var best []crossPair
			for _, o := range cands {
				if o.vec == nil || o.bankID == f.bankID || o.conf < 0.5 {
					continue
				}
				best = append(best, crossPair{a: f.id, b: o.id, sim: Dot(f.vec, o.vec), aBank: f.bankID, bBank: o.bankID})
			}
			sort.Slice(best, func(i, j int) bool { return best[i].sim > best[j].sim })
			if len(best) > crossNeighbours {
				best = best[:crossNeighbours]
			}
			for _, p := range best {
				add(p.a, p.b, p.aBank, p.bBank, p.sim)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sim > out[j].sim })
	return out, nil
}

func (s *Service) crossFacts(ctx context.Context, ids map[int64]bool) (map[int64]crossFact, error) {
	list := make([]int64, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	rows, err := s.db.Query(ctx, `SELECT f.id, f.bank_id, f.text, f.pinned, f.confirmation='user_confirmed', b.kind, b.name
		FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id WHERE f.id=ANY($1)`, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]crossFact{}
	for rows.Next() {
		var f crossFact
		var kind, name string
		if rows.Scan(&f.id, &f.bank, &f.text, &f.pinned, &f.userConf, &kind, &name) == nil {
			f.bankLabel = kind
			if name != "" {
				f.bankLabel = kind + ":" + name
			}
			out[f.id] = f
		}
	}
	return out, rows.Err()
}

// mergeAcross keeps one fact and lets the other go: the keeper becomes visible in the copy's bank too, so nothing
// that could once be recalled from there is lost.
func (s *Service) mergeAcross(ctx context.Context, keep, drop crossFact) bool {
	if _, err := s.db.Exec(ctx, `INSERT INTO memory_fact_banks(fact_id,bank_id,added_by) VALUES($1,$2,'cross-analysis') ON CONFLICT DO NOTHING`, keep.id, drop.bank); err != nil {
		return false
	}
	r, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1 AND kind='fact' AND NOT pinned AND valid_to IS NULL`, drop.id, keep.id)
	if err != nil || r.RowsAffected() == 0 {
		return false
	}
	s.audit(ctx, "cross-analysis", "merge", drop.id, fmt.Sprintf("duplicate of #%d in another bank; kept that one, now also visible here", keep.id))
	return true
}

// knownConnections are the cross-bank insights already on record, so the same one is not written twice.
func (s *Service) knownConnections(ctx context.Context) []conclusionRow {
	rows, err := s.db.Query(ctx, `SELECT id,text FROM memory_facts WHERE kind='conclusion' AND valid_to IS NULL AND 'connection'=ANY(tags)`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []conclusionRow
	for rows.Next() {
		var c conclusionRow
		if rows.Scan(&c.ID, &c.Text) == nil {
			out = append(out, c)
		}
	}
	return out
}
