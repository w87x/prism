package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Facts can be linked to each other, inside a bank or across banks. Links are undirected (stored with
// a < b) and come from three places: the store path links a new fact to its close neighbours, agents
// link facts on purpose (memory_link), and the user edits them in the Memory page. Retrieval follows
// links one hop (see Find), and links between facts that keep being retrieved together get stronger.
const (
	LinkRelated     = "related"
	LinkSupports    = "supports"
	LinkContradicts = "contradicts"
	LinkEvidence    = "evidence"

	maxAutoLinks   = 3
	crossBankSim   = 0.72 // facts in other banks must be closer than in the own bank to be linked automatically
	maxExpansion   = 3
	expansionDecay = 0.7
)

var linkKinds = map[string]bool{LinkRelated: true, LinkSupports: true, LinkContradicts: true, LinkEvidence: true}

// Linked is a fact seen through a link.
type Linked struct {
	Fact
	LinkKind string  `json:"link_kind"`
	Weight   float64 `json:"link_weight"`
	Note     string  `json:"link_note"`
	By       string  `json:"link_by"`
}

func order(a, b int64) (int64, int64) {
	if a > b {
		return b, a
	}
	return a, b
}

// Link creates or updates the link between two facts.
func (s *Service) Link(ctx context.Context, a, b int64, kind, note, by string, weight float64) error {
	if a == b {
		return errors.New("a fact cannot be linked to itself")
	}
	if kind == "" {
		kind = LinkRelated
	}
	if !linkKinds[kind] {
		return fmt.Errorf("unknown link kind %q (related, supports, contradicts or evidence)", kind)
	}
	if weight <= 0 || weight > 1 {
		weight = 0.6
	}
	a, b = order(a, b)
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM memory_facts WHERE id IN ($1,$2)`, a, b).Scan(&n); err != nil {
		return err
	}
	if n != 2 {
		return errors.New("both facts must exist")
	}
	// a person's or an agent's deliberate link outranks an automatic one
	_, err := s.db.Exec(ctx, `INSERT INTO memory_links(a,b,kind,weight,note,source) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT (a,b) DO UPDATE SET kind=EXCLUDED.kind, note=CASE WHEN EXCLUDED.note<>'' THEN EXCLUDED.note ELSE memory_links.note END,
			source=EXCLUDED.source, weight=GREATEST(memory_links.weight, EXCLUDED.weight)`,
		a, b, kind, float32(weight), strings.TrimSpace(note), by)
	if err == nil {
		s.changed()
	}
	return err
}

func (s *Service) Unlink(ctx context.Context, a, b int64) error {
	a, b = order(a, b)
	_, err := s.db.Exec(ctx, `DELETE FROM memory_links WHERE a=$1 AND b=$2`, a, b)
	s.changed()
	return err
}

// Links lists the facts linked to id, strongest first (retired facts included, so history stays navigable).
func (s *Service) Links(ctx context.Context, id int64) ([]Linked, error) {
	rows, err := s.db.Query(ctx, `SELECT `+factCols+`, l.kind, l.weight, l.note, l.source
		FROM memory_links l
		JOIN memory_facts f ON f.id = CASE WHEN l.a=$1 THEN l.b ELSE l.a END
		JOIN memory_banks b ON b.id=f.bank_id
		WHERE l.a=$1 OR l.b=$1 ORDER BY l.weight DESC, f.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Linked
	for rows.Next() {
		var x Linked
		var rk, cf, w float32
		var taskID *int64
		if err := rows.Scan(&x.ID, &x.BankID, &x.Bank, &x.Text, &x.Tags, &rk, &x.Hits, &cf, &x.Source,
			&x.Supersedes, &x.SupersededBy, &x.ValidFrom, &x.ValidTo, &x.LastUsed, &x.CreatedAt, &x.Embedded, &x.Links, &x.Fact.Kind, &x.Proof, &x.Stale, &x.Origins,
			&taskID, &x.Pinned,
			&x.LinkKind, &w, &x.Note, &x.By); err != nil {
			return nil, err
		}
		x.Rank, x.Confidence, x.Weight = float64(rk), float64(cf), float64(w)
		if taskID != nil {
			x.TaskID = *taskID
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

type neighbour struct {
	id  int64
	sim float64
}

// autoLink connects a freshly stored fact to its closest neighbours: same-bank ones found by Store, plus
// close ones in other banks (a project fact that restates something known about the user).
func (s *Service) autoLink(ctx context.Context, id, bankID int64, text string, vec []float32, same []neighbour) {
	var picks []neighbour
	picks = append(picks, same...)
	if vec != nil {
		if bs, err := s.Banks(ctx); err == nil {
			var others []int64
			for _, b := range bs {
				if b.ID != bankID && b.Status == "active" {
					others = append(others, b.ID)
				}
			}
			if len(others) > 0 {
				picks = append(picks, s.nearIn(ctx, others, vec, crossBankSim, 2)...)
			}
		}
	}
	sort.Slice(picks, func(i, j int) bool { return picks[i].sim > picks[j].sim })
	n := 0
	for _, p := range picks {
		if p.id == id || n >= maxAutoLinks {
			continue
		}
		if s.Link(ctx, id, p.id, LinkRelated, "", "auto", math.Min(1, p.sim)) == nil {
			n++
		}
	}
}

// nearIn returns up to k active facts in the given banks with cosine similarity ≥ min.
func (s *Service) nearIn(ctx context.Context, bankIDs []int64, qv []float32, min float64, k int) []neighbour {
	var out []neighbour
	if s.VectorOn {
		for id, sim := range s.vecSims(ctx, bankIDs, false, qv, k) {
			if sim >= min {
				out = append(out, neighbour{id, sim})
			}
		}
	} else if cands, err := s.candidates(ctx, bankIDs, false, 4000); err == nil {
		for _, c := range cands {
			if c.vec != nil {
				if sim := Dot(qv, c.vec); sim >= min {
					out = append(out, neighbour{c.id, sim})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sim > out[j].sim })
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// expand follows links one hop from the retrieved facts: a fact that is tied to something the query
// matched is often the missing half of the answer. Only banks the caller may read are followed, retired
// facts are skipped, and each hop loses strength (expansionDecay × link weight).
func (s *Service) expand(ctx context.Context, picked map[int64]float64, allowed []int64, limit int) []Fact {
	if len(picked) == 0 || limit <= 0 {
		return nil
	}
	ids := make([]int64, 0, len(picked))
	for id := range picked {
		ids = append(ids, id)
	}
	rows, err := s.db.Query(ctx, `SELECT l.a, l.b, l.weight FROM memory_links l WHERE l.a=ANY($1) OR l.b=ANY($1)`, ids)
	if err != nil {
		return nil
	}
	type edge struct {
		from, to int64
		score    float64
	}
	best := map[int64]edge{}
	for rows.Next() {
		var a, b int64
		var w float32
		if rows.Scan(&a, &b, &w) != nil {
			continue
		}
		for _, p := range [][2]int64{{a, b}, {b, a}} {
			ps, ok := picked[p[0]]
			if !ok {
				continue
			}
			if _, dup := picked[p[1]]; dup {
				continue
			}
			sc := ps * float64(w) * expansionDecay
			if e, seen := best[p[1]]; !seen || sc > e.score {
				best[p[1]] = edge{p[0], p[1], sc}
			}
		}
	}
	rows.Close()
	if len(best) == 0 {
		return nil
	}
	cand := make([]edge, 0, len(best))
	for _, e := range best {
		cand = append(cand, e)
	}
	sort.Slice(cand, func(i, j int) bool { return cand[i].score > cand[j].score })
	want := make([]int64, 0, len(cand))
	for _, e := range cand {
		want = append(want, e.to)
	}
	fr, err := s.db.Query(ctx, `SELECT `+factCols+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.id=ANY($1) AND f.valid_to IS NULL AND f.bank_id=ANY($2)`, want, allowed)
	if err != nil {
		return nil
	}
	defer fr.Close()
	byID := map[int64]Fact{}
	for fr.Next() {
		if f, err := scanFact(fr); err == nil {
			byID[f.ID] = f
		}
	}
	var out []Fact
	for _, e := range cand {
		f, ok := byID[e.to]
		if !ok {
			continue
		}
		via := e.from
		f.Via = &via
		f.Score = math.Round(e.score*1000) / 1000
		out = append(out, f)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// strengthen makes links between facts that were retrieved together a little stronger (Hebbian: facts
// that fire together, wire together), so well-worn associations are followed first.
func (s *Service) strengthen(ctx context.Context, ids []int64) {
	if len(ids) < 2 {
		return
	}
	_, _ = s.db.Exec(ctx, `UPDATE memory_links SET weight=LEAST(weight+0.05,1) WHERE a=ANY($1) AND b=ANY($1)`, ids)
}
