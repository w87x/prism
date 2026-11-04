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
	out, err := s.llm.Complete(ctx, s.llm.ActionRef(ctx, "memory_dedupe", "fast"),
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
	merge := func(a, b cand) bool {
		keep, drop := a, b
		if drop.rank > keep.rank || (drop.rank == keep.rank && drop.conf > keep.conf) {
			keep, drop = drop, keep
		}
		tag, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1 AND valid_to IS NULL`, drop.id, keep.id)
		if err != nil || tag.RowsAffected() != 1 {
			return false
		}
		s.reinforce(ctx, keep.id, StoreReq{Confidence: drop.conf})
		retired[drop.id] = true
		res.Merged++
		return true
	}
	for i := range cands {
		a := cands[i]
		if retired[a.id] {
			continue
		}
		var near []cand
		for j := i + 1; j < len(cands); j++ {
			b := cands[j]
			if retired[b.id] {
				continue
			}
			if normFactText(a.text) == normFactText(b.text) {
				merge(a, b)
				if retired[a.id] {
					break
				}
				continue
			}
			if similarity(a, b) >= relatedSim {
				near = append(near, b)
			}
		}
		if retired[a.id] {
			continue
		}
		for start := 0; start < len(near); start += 8 {
			end := min(start+8, len(near))
			relations, err := s.classifyRelations(ctx, a.text, near[start:end])
			if err != nil {
				continue
			}
			for _, b := range near[start:end] {
				if retired[b.id] || relations[b.id] != "same" {
					continue
				}
				merge(a, b)
				if retired[a.id] {
					break
				}
			}
			if retired[a.id] {
				break
			}
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

// classifyRelations asks about at most eight candidates. Unknown, missing, repeated
// contradictory or invalid IDs never authorize a merge.
func (s *Service) classifyRelations(ctx context.Context, text string, candidates []cand) (map[int64]string, error) {
	valid := map[int64]bool{}
	var lines []string
	for _, c := range candidates {
		valid[c.id] = true
		lines = append(lines, fmt.Sprintf("%d: %s", c.id, c.text))
	}
	out, err := s.llm.Complete(ctx, s.llm.ActionRef(ctx, "memory_dedupe", "fast"), `You maintain memory facts. Compare NEW with each OLD independently. Label OLD as same only if both state exactly the same information; if either has any additional detail, do not call them same. Otherwise choose update, contradiction, or unrelated. Answer JSON only: {"relations":[{"id":123,"relation":"same|update|contradiction|unrelated"}]}`, "NEW: "+text+"\nOLD:\n"+strings.Join(lines, "\n"), true)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Relations []struct {
			ID       int64  `json:"id"`
			Relation string `json:"relation"`
		} `json:"relations"`
	}
	if err := json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed); err != nil {
		return nil, err
	}
	result := map[int64]string{}
	conflict := map[int64]bool{}
	seen := map[int64]bool{}
	for _, r := range parsed.Relations {
		if !valid[r.ID] {
			continue
		}
		if seen[r.ID] {
			conflict[r.ID] = true
			continue
		}
		seen[r.ID] = true
		rel := strings.ToLower(strings.TrimSpace(r.Relation))
		switch rel {
		case "same", "update", "contradiction", "unrelated":
		default:
			continue
		}
		result[r.ID] = rel
	}
	for id := range conflict {
		delete(result, id)
	}
	return result, nil
}
