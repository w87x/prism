package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"prism/internal/llm"
)

// SplitFact breaks a compound fact ("downloaded X and Y, and bought a new NAS") into separate atomic ones —
// the extraction prompt aims for one claim per fact, but a fact hand-typed by the user, or one that picked
// up a second clause via an edit, can still end up compound. The original is superseded by the first piece,
// exactly like any other correction, so its history/links are never silently dropped.
func (s *Service) SplitFact(ctx context.Context, id int64) ([]Fact, error) {
	f, err := s.GetFact(ctx, id)
	if err != nil {
		return nil, err
	}
	if f.ValidTo != nil {
		return nil, errors.New("fact is already retired")
	}
	if s.llm.RoleRef(ctx, "fast") == "" {
		return nil, errors.New("no model configured")
	}
	out, err := s.llm.Complete(ctx, "role:fast",
		`Break the given fact into separate atomic facts, one independent claim per item, each still a self-contained sentence in third person. `+
			`If it is already a single atomic claim, return it unchanged as the only item.
Answer JSON only: {"facts":["...","..."]}`,
		f.Text, true)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Facts []string `json:"facts"`
	}
	if err := json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed); err != nil {
		return nil, fmt.Errorf("split: %w", err)
	}
	var texts []string
	for _, t := range parsed.Facts {
		if t = strings.TrimSpace(t); t != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) < 2 {
		return nil, errors.New("this fact is already atomic — nothing to split")
	}
	var newFacts []Fact
	var newIDs []int64
	for _, t := range texts {
		r, err := s.Store(ctx, StoreReq{Bank: f.Bank, Text: t, Tags: f.Tags, Source: f.Source, Confidence: f.Confidence, ValueRatio: f.ValueRatio, TaskID: f.TaskID})
		if err != nil {
			continue
		}
		newFacts = append(newFacts, r.Fact)
		newIDs = append(newIDs, r.Fact.ID)
	}
	if len(newFacts) == 0 {
		return nil, errors.New("split produced no facts")
	}
	if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1 AND valid_to IS NULL`, id, newIDs[0]); err != nil {
		return nil, err
	}
	s.changed()
	return newFacts, nil
}

type DedupeFactsResult struct {
	Checked int `json:"checked"`
	Merged  int `json:"merged"`
}

// DedupeFacts sweeps one bank's live facts for near-duplicates that slipped past Store's ingest-time check
// (e.g. two facts phrased differently enough that they weren't compared at the time, or one bank absorbing
// another's facts wholesale via MergeBanks, which only collapses EXACT text matches). Reuses the same
// same/update/contradiction/unrelated classifier Store uses for a new fact against its neighbours, applied
// here pairwise within the bank; only "same" pairs are merged — an "update" or "contradiction" is a real
// correction over time, not a duplicate, and is left alone.
func (s *Service) DedupeFacts(ctx context.Context, bankSpec string) (DedupeFactsResult, error) {
	var res DedupeFactsResult
	if s.llm.RoleRef(ctx, "fast") == "" {
		return res, errors.New("no model configured")
	}
	bank, err := s.BankBySpec(ctx, bankSpec, "", false)
	if err != nil {
		return res, err
	}
	cands, err := s.candidates(ctx, []int64{bank.ID}, false, 300)
	if err != nil {
		return res, err
	}
	res.Checked = len(cands)
	retired := map[int64]bool{}
	for i := range cands {
		a := cands[i]
		if retired[a.id] {
			continue
		}
		for j := i + 1; j < len(cands); j++ {
			b := cands[j]
			if retired[b.id] {
				continue
			}
			sim := similarity(a, b)
			same := normFactText(a.text) == normFactText(b.text)
			if !same && sim < relatedSim {
				continue
			}
			if !same {
				rel, err := s.classifyRelation(ctx, a.text, b.text)
				if err != nil || rel != "same" {
					continue
				}
			}
			keep, drop := a, b
			if drop.rank > keep.rank || (drop.rank == keep.rank && drop.conf > keep.conf) {
				keep, drop = drop, keep
			}
			if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1 AND valid_to IS NULL`, drop.id, keep.id); err != nil {
				continue
			}
			s.reinforce(ctx, keep.id, StoreReq{Confidence: drop.conf})
			retired[drop.id] = true
			res.Merged++
		}
	}
	if res.Merged > 0 {
		s.changed()
	}
	return res, nil
}

func similarity(a, b cand) float64 {
	if a.vec != nil && b.vec != nil {
		return Dot(a.vec, b.vec)
	}
	sim := jaccard(a.text, b.text)
	if sim < 0.5 {
		sim *= 0.8
	}
	return sim
}

// classifyRelation is Store's near-duplicate classifier, factored out so DedupeFacts can ask the same
// question about two already-stored facts instead of a new one against its neighbours.
func (s *Service) classifyRelation(ctx context.Context, newText, oldText string) (string, error) {
	out, err := s.llm.Complete(ctx, "role:fast", `You maintain a memory of facts. Given NEW and OLD, decide OLD's relation to NEW:
"same" (NEW adds nothing), "update" (NEW replaces or corrects OLD), "contradiction" (both cannot be true) or "unrelated".
Answer JSON only: {"relation":"same|update|contradiction|unrelated"}`,
		"NEW: "+newText+"\nOLD: "+oldText, true)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Relation string `json:"relation"`
	}
	if err := json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed); err != nil {
		return "", err
	}
	return parsed.Relation, nil
}
