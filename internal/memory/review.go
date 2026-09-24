package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Review surfaces memory that needs a human decision, so it does not just quietly rot or quietly get
// auto-archived: unresolved contradictions between two active facts, conclusions whose evidence was
// retired (Stale, see reflect.go) and never revised, and facts about to be auto-archived by the next
// Prune run (same criteria as Prune itself — see raw.go — so this is a preview/veto point, not a
// second opinion).
type FactRef struct {
	ID         int64   `json:"id"`
	Bank       string  `json:"bank"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
	Rank       float64 `json:"rank"`
}

type Contradiction struct {
	A         FactRef   `json:"a"`
	B         FactRef   `json:"b"`
	Note      string    `json:"note"`
	Weight    float64   `json:"weight"`
	CreatedAt time.Time `json:"created_at"`
}

type Review struct {
	Contradictions   []Contradiction `json:"contradictions"`
	StaleConclusions []Fact          `json:"stale_conclusions"`
	PruneCandidates  []Fact          `json:"prune_candidates"`
	Unverified       []Fact          `json:"unverified"` // web-learned facts nobody has confirmed yet
	Open             []Fact          `json:"open"`       // hypotheses and questions from deep analysis
}

func (s *Service) Review(ctx context.Context, limit int) (*Review, error) {
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	out := &Review{Contradictions: []Contradiction{}, StaleConclusions: []Fact{}, PruneCandidates: []Fact{}, Unverified: []Fact{}, Open: []Fact{}}

	crows, err := s.db.Query(ctx, `SELECT
			a.id, ba.kind||CASE WHEN ba.kind='user' THEN '' ELSE ':'||ba.name END, a.text, a.confidence, a.rank,
			bf.id, bb.kind||CASE WHEN bb.kind='user' THEN '' ELSE ':'||bb.name END, bf.text, bf.confidence, bf.rank,
			l.note, l.weight, l.created_at
		FROM memory_links l
		JOIN memory_facts a ON a.id=l.a JOIN memory_banks ba ON ba.id=a.bank_id
		JOIN memory_facts bf ON bf.id=l.b JOIN memory_banks bb ON bb.id=bf.bank_id
		WHERE l.kind=$1 AND a.valid_to IS NULL AND bf.valid_to IS NULL
		ORDER BY l.created_at DESC LIMIT $2`, LinkContradicts, limit)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var c Contradiction
		var aConf, aRank, bConf, bRank, w float32
		if err := crows.Scan(&c.A.ID, &c.A.Bank, &c.A.Text, &aConf, &aRank, &c.B.ID, &c.B.Bank, &c.B.Text, &bConf, &bRank, &c.Note, &w, &c.CreatedAt); err != nil {
			crows.Close()
			return nil, err
		}
		c.A.Confidence, c.A.Rank, c.B.Confidence, c.B.Rank, c.Weight = float64(aConf), float64(aRank), float64(bConf), float64(bRank), float64(w)
		out.Contradictions = append(out.Contradictions, c)
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, err
	}

	// same staleness test as the Stale column in factCols: a conclusion whose evidence has since been retired.
	srows, err := s.db.Query(ctx, `SELECT `+factCols+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.kind='conclusion' AND f.valid_to IS NULL
		AND EXISTS(SELECT 1 FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=f.id THEN e.b ELSE e.a END
			WHERE (e.a=f.id OR e.b=f.id) AND e.kind='evidence' AND x.kind='fact' AND x.valid_to IS NOT NULL)
		ORDER BY f.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		f, err := scanFact(srows)
		if err != nil {
			srows.Close()
			return nil, err
		}
		out.StaleConclusions = append(out.StaleConclusions, f)
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, err
	}

	// same criteria Prune itself uses (raw.go) — this is a look-before-it-happens list, not a different rule.
	for _, q := range []struct {
		dst  *[]Fact
		cond string
	}{
		{&out.Unverified, `f.kind='fact' AND f.confidence<0.5`},
		{&out.Open, `f.kind='conclusion' AND f.source='analysis' AND f.tags && ARRAY['hypothesis','question']`},
	} {
		rows, err := s.db.Query(ctx, `SELECT `+factCols+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
			WHERE f.valid_to IS NULL AND `+q.cond+` ORDER BY f.rank DESC, f.id DESC LIMIT $1`, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			f, err := scanFact(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			*q.dst = append(*q.dst, f)
		}
		rows.Close()
	}

	prows, err := s.db.Query(ctx, `SELECT `+factCols+` FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id
		WHERE f.valid_to IS NULL AND NOT f.pinned AND f.kind='fact' AND f.rank<0.25 AND COALESCE(f.last_used,f.created_at) < now()-interval '90 days'
		ORDER BY f.rank ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		f, err := scanFact(prows)
		if err != nil {
			prows.Close()
			return nil, err
		}
		out.PruneCandidates = append(out.PruneCandidates, f)
	}
	prows.Close()
	return out, prows.Err()
}

// ConfirmFact is the user vouching for an unverified (web-learned) fact: it becomes trusted and may serve
// as evidence from now on.
func (s *Service) ConfirmFact(ctx context.Context, id int64) error {
	r, err := s.db.Exec(ctx, `UPDATE memory_facts SET confidence=0.95, tags=array_remove(tags,'unverified')
		WHERE id=$1 AND kind='fact' AND valid_to IS NULL`, id)
	if err != nil {
		return err
	}
	if r.RowsAffected() == 0 {
		return fmt.Errorf("fact #%d is not an active fact", id)
	}
	s.changed()
	return nil
}

// ResolveInsight closes an open hypothesis or question from deep analysis. "confirm" turns a hypothesis into
// a trusted fact, "answer" turns the user's reply to a question into one, "reject" just retires it. Whatever
// the verdict the insight is retired, so analysis does not raise it again unchanged.
func (s *Service) ResolveInsight(ctx context.Context, id int64, verdict, answer string) (*Fact, error) {
	f, err := s.GetFact(ctx, id)
	if err != nil || f.Kind != "conclusion" || f.ValidTo != nil || insightType(f.Tags) == "" {
		return nil, fmt.Errorf("#%d is not an open insight", id)
	}
	var text string
	switch verdict {
	case "confirm":
		text = f.Text
	case "answer":
		if strings.TrimSpace(answer) == "" {
			return nil, errors.New("write the answer")
		}
		text = strings.TrimSpace(f.Text + " " + strings.TrimSpace(answer))
	case "reject":
	default:
		return nil, fmt.Errorf("unknown verdict %q (confirm, answer or reject)", verdict)
	}
	var made *Fact
	if text != "" {
		var b Bank
		if err := s.db.QueryRow(ctx, `SELECT id,kind,name,owner FROM memory_banks WHERE id=$1`, f.BankID).Scan(&b.ID, &b.Kind, &b.Name, &b.Owner); err != nil {
			return nil, err
		}
		res, err := s.Store(ctx, StoreReq{Bank: b.Label(), Text: text, Source: "user (confirmed)", Confidence: 0.95})
		if err != nil {
			return nil, err
		}
		made = &res.Fact
	}
	if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now() WHERE id=$1 AND valid_to IS NULL`, id); err != nil {
		return nil, err
	}
	s.changed()
	return made, nil
}
