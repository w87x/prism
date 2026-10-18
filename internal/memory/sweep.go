package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// The near-duplicate sweep. Storing a fact only compares it with its four nearest neighbours and asks a small model, which leans towards
// "unrelated"; so paraphrases ("prefers BDRip" said twice) survive. This pass looks at the whole store instead: it finds every pair of live facts
// that read almost alike (by embedding), asks the MAIN model about each pair once, and retires the weaker of two that say the same thing.
// Similarity alone never merges anything — three "power outage in <village>" facts are 0.89 alike and all different — so the judge is told that a
// different name, place, number or date means "distinct", and every merge is logged so it can be undone.
const (
	sweepMinSim     = 0.84 // below this a pair is not worth the model's time
	sweepNeighbours = 3    // look-alikes considered per fact
	sweepBatch      = 30   // pairs judged per pass
)

const sweepPrompt = `You keep an assistant's long-term memory tidy. Each PAIR below holds two facts that read almost alike. Decide, for every pair:
- "same": both state the same thing in different words, and neither has a detail the other lacks. "keep" is the id of the better-worded one.
- "update": same subject and attribute, but the value changed over time (a count, a version, a place lived in). "keep" is the newer, current one.
- "distinct": anything else. A different person, place, item, name, number, date or version means distinct, and so does a fact that carries information the other does not. Facts about different things that merely share a template are distinct.
When in doubt, answer "distinct": a duplicate left alone costs little, a wrongly merged fact loses information.
Answer JSON only: {"pairs":[{"n":1,"verdict":"same|update|distinct","keep":0,"note":"why, in a few words"}]}`

// SweepItem is one pair that was judged to be a duplicate.
type SweepItem struct {
	Keep     int64   `json:"keep"`
	Drop     int64   `json:"drop"`
	KeepText string  `json:"keep_text"`
	DropText string  `json:"drop_text"`
	Sim      float64 `json:"sim"`
	Verdict  string  `json:"verdict"`
	Note     string  `json:"note,omitempty"`
	Applied  bool    `json:"applied"`
}

type SweepResult struct {
	Pairs    int         `json:"pairs"`    // look-alike pairs judged this pass
	Distinct int         `json:"distinct"` // judged not to be duplicates
	Merged   int         `json:"merged"`
	Left     int         `json:"left"` // look-alike pairs still waiting for a later pass
	Items    []SweepItem `json:"items,omitempty"`
	Dry      bool        `json:"dry,omitempty"`
	Skipped  string      `json:"skipped,omitempty"`
}

func (r SweepResult) Changes() int { return r.Merged }

func (r SweepResult) String() string {
	if r.Skipped != "" {
		return "duplicate sweep: skipped (" + r.Skipped + ")"
	}
	verb := "merged"
	n := r.Merged
	if r.Dry {
		verb, n = "would merge", len(r.Items)
	}
	s := fmt.Sprintf("duplicate sweep: %d look-alike pairs judged — %d %s, %d distinct", r.Pairs, n, verb, r.Distinct)
	if r.Left > 0 {
		s += fmt.Sprintf(" (%d more waiting)", r.Left)
	}
	for _, it := range r.Items {
		s += fmt.Sprintf("\n  #%d ← #%d (%.2f) %s: %q", it.Keep, it.Drop, it.Sim, it.Verdict, it.DropText)
	}
	return s
}

type sweepPair struct {
	a, b int64
	sim  float64
}

type sweepMerge struct {
	Keep  int64 `json:"keep"`
	Drop  int64 `json:"drop"`
	Bank  int64 `json:"bank"`  // the bank the keeper was made visible in
	Added bool  `json:"added"` // whether that visibility is new (and so is removed on undo)
}

type sweepOp struct {
	Merges []sweepMerge `json:"merges"`
}

// Sweep runs one pass. dry only reports what it would merge (nothing is written, and the pairs are not marked as seen).
func (s *Service) Sweep(ctx context.Context, dry bool) (SweepResult, error) {
	res := SweepResult{Dry: dry}
	if s.llm.RoleRef(ctx, "chat") == "" && s.llm.RoleRef(ctx, "fast") == "" {
		res.Skipped = "no model configured"
		return res, nil
	}
	pairs, err := s.sweepPairs(ctx)
	if err != nil {
		return res, err
	}
	if len(pairs) == 0 {
		res.Skipped = "no look-alike facts left to judge"
		return res, nil
	}
	if len(pairs) > sweepBatch {
		res.Left = len(pairs) - sweepBatch
		pairs = pairs[:sweepBatch]
	}
	res.Pairs = len(pairs)
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
	}
	if err := s.llm.CompleteJSON(ctx, s.llm.ActionRef(ctx, "memory_sweep", "chat"), sweepPrompt, s.guidance(ctx)+sb.String(), &parsed); err != nil {
		return res, fmt.Errorf("duplicate sweep: %w", err)
	}
	verdict := map[int]string{}
	var op sweepOp
	for _, v := range parsed.Pairs {
		if v.N < 1 || v.N > len(pairs) {
			continue
		}
		p := pairs[v.N-1]
		kind := strings.ToLower(strings.TrimSpace(v.Verdict))
		verdict[v.N] = "distinct"
		if (kind != "same" && kind != "update") || (v.Keep != p.a && v.Keep != p.b) {
			continue
		}
		keep, drop := facts[p.a], facts[p.b]
		if v.Keep == p.b {
			keep, drop = drop, keep
		}
		if drop.pinned || drop.userConf { // never retire what the user vouched for or pinned
			if keep.pinned || keep.userConf {
				verdict[v.N] = "protected"
				continue
			}
			keep, drop = drop, keep
		}
		item := SweepItem{Keep: keep.id, Drop: drop.id, KeepText: keep.text, DropText: drop.text, Sim: p.sim, Verdict: kind, Note: strings.TrimSpace(v.Note)}
		verdict[v.N] = "merged"
		if !dry {
			m, ok := s.sweepMerge(ctx, keep, drop, kind, item.Note)
			if !ok {
				verdict[v.N] = "failed"
				continue
			}
			item.Applied = true
			op.Merges = append(op.Merges, m)
			res.Merged++
		}
		res.Items = append(res.Items, item)
	}
	for n := range pairs {
		if verdict[n+1] == "" || verdict[n+1] == "distinct" {
			res.Distinct++
		}
	}
	if dry {
		return res, nil
	}
	for i, p := range pairs {
		v := verdict[i+1]
		if v == "" {
			continue // the model skipped it: ask again next time
		}
		if v == "failed" {
			continue
		}
		x, y := order(p.a, p.b)
		_, _ = s.db.Exec(ctx, `INSERT INTO memory_sweep_pairs(a,b,verdict) VALUES($1,$2,$3) ON CONFLICT (a,b) DO NOTHING`, x, y, v)
	}
	if len(op.Merges) > 0 {
		s.logOp(ctx, "sweep", fmt.Sprintf("merged %d near-duplicate facts", len(op.Merges)), op)
		s.changed()
	}
	return res, nil
}

// sweepMerge retires drop in favour of keep; when they sit in different banks the keeper becomes visible in the copy's bank too.
func (s *Service) sweepMerge(ctx context.Context, keep, drop crossFact, kind, note string) (sweepMerge, bool) {
	m := sweepMerge{Keep: keep.id, Drop: drop.id, Bank: drop.bank}
	if keep.bank != drop.bank {
		r, err := s.db.Exec(ctx, `INSERT INTO memory_fact_banks(fact_id,bank_id,added_by) VALUES($1,$2,'sweep') ON CONFLICT DO NOTHING`, keep.id, drop.bank)
		if err != nil {
			return m, false
		}
		m.Added = r.RowsAffected() > 0
	}
	r, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1 AND kind='fact' AND NOT pinned AND valid_to IS NULL`, drop.id, keep.id)
	if err != nil || r.RowsAffected() == 0 {
		if m.Added {
			_, _ = s.db.Exec(ctx, `DELETE FROM memory_fact_banks WHERE fact_id=$1 AND bank_id=$2 AND added_by='sweep'`, keep.id, drop.bank)
		}
		return m, false
	}
	detail := fmt.Sprintf("%s of #%d; kept that one", kind, keep.id)
	if note != "" {
		detail += " (" + note + ")"
	}
	s.audit(ctx, "sweep", "merge", drop.id, detail)
	return m, true
}

func (s *Service) undoSweep(ctx context.Context, op sweepOp) (string, error) {
	n := 0
	for _, m := range op.Merges {
		r, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=NULL, superseded_by=NULL WHERE id=$1 AND superseded_by=$2`, m.Drop, m.Keep)
		if err != nil {
			return "", err
		}
		if r.RowsAffected() == 0 {
			continue // changed since: leave it alone
		}
		n++
		if m.Added {
			_, _ = s.db.Exec(ctx, `DELETE FROM memory_fact_banks WHERE fact_id=$1 AND bank_id=$2 AND added_by='sweep'`, m.Keep, m.Bank)
		}
		x, y := order(m.Keep, m.Drop)
		_, _ = s.db.Exec(ctx, `UPDATE memory_sweep_pairs SET verdict='undone' WHERE a=$1 AND b=$2`, x, y) // the user said no: never ask again
		s.audit(ctx, "user", "restore", m.Drop, fmt.Sprintf("sweep merge into #%d undone", m.Keep))
	}
	return fmt.Sprintf("%d facts restored", n), nil
}

// sweepPairs: pairs of live trusted facts that read alike and have not been judged yet, most alike first.
func (s *Service) sweepPairs(ctx context.Context) ([]sweepPair, error) {
	var out []sweepPair
	if s.VectorOn {
		rows, err := s.db.Query(ctx, `SELECT f.id, n.id, n.sim FROM memory_facts f
			CROSS JOIN LATERAL (
				SELECT o.id, 1 - (o.vec <=> f.vec) AS sim FROM memory_facts o JOIN memory_banks ob ON ob.id=o.bank_id AND ob.status='active'
				WHERE o.kind='fact' AND o.valid_to IS NULL AND o.status<>'proposed' AND o.vec IS NOT NULL
				  AND vector_dims(o.vec)=vector_dims(f.vec) AND o.id>f.id
				ORDER BY o.vec <=> f.vec LIMIT $1) n
			JOIN memory_banks fb ON fb.id=f.bank_id AND fb.status='active'
			WHERE f.kind='fact' AND f.valid_to IS NULL AND f.status<>'proposed' AND f.vec IS NOT NULL
			  AND n.sim >= $2
			  AND NOT EXISTS (SELECT 1 FROM memory_sweep_pairs sp WHERE sp.a=f.id AND sp.b=n.id)
			ORDER BY n.sim DESC`, sweepNeighbours, sweepMinSim)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var p sweepPair
			if rows.Scan(&p.a, &p.b, &p.sim) == nil {
				out = append(out, p)
			}
		}
		return out, rows.Err()
	}
	bs, err := s.Banks(ctx)
	if err != nil {
		return nil, err
	}
	var bankIDs []int64
	for _, b := range bs {
		if b.Status == "active" {
			bankIDs = append(bankIDs, b.ID)
		}
	}
	cands, err := s.candidates(ctx, bankIDs, false, 4000)
	if err != nil {
		return nil, err
	}
	seen := map[[2]int64]bool{}
	rows, err := s.db.Query(ctx, `SELECT a,b FROM memory_sweep_pairs`)
	if err == nil {
		for rows.Next() {
			var a, b int64
			if rows.Scan(&a, &b) == nil {
				seen[[2]int64{a, b}] = true
			}
		}
		rows.Close()
	}
	for i, f := range cands {
		if f.vec == nil {
			continue
		}
		var best []sweepPair
		for _, o := range cands[i+1:] {
			if o.vec == nil {
				continue
			}
			if sim := Dot(f.vec, o.vec); sim >= sweepMinSim {
				x, y := order(f.id, o.id)
				if !seen[[2]int64{x, y}] {
					best = append(best, sweepPair{a: x, b: y, sim: sim})
				}
			}
		}
		sort.Slice(best, func(i, j int) bool { return best[i].sim > best[j].sim })
		if len(best) > sweepNeighbours {
			best = best[:sweepNeighbours]
		}
		out = append(out, best...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sim > out[j].sim })
	return out, nil
}
