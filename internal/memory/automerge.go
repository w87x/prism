package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"prism/internal/llm"
)

// Distillation sometimes names a project/domain bank slightly differently each time it comes up (GLM,
// GLM-4.6, GLM-5.3 for what is really one topic) — the extraction prompt now lists existing banks to
// discourage that, but this is the safety net: periodically ask the fast model which active banks of one
// kind are really the same topic, and merge them. Every merge goes through MergeBanks, so it is logged to
// memory_ops and can be undone from the Memory page exactly like a manual merge.
const mergePrompt = `You manage the memory banks of a personal assistant (each is project:<Name> or domain:<Name>). Below are the banks of ONE kind, each with its id, name and fact count. Group any that are clearly the SAME real-world topic or task under one canonical name — for example different versions or variants of one product or model family ("GLM", "GLM-4.6", "GLM-5.3") are one topic, not several, and should be one group. Do NOT group banks that are genuinely different topics just because the names look similar.

Only include groups of 2 or more banks; leave out banks that stand on their own — most banks should be left out. Give each group a short canonical name (reuse one of its members' names when it already reads well).
Answer JSON only: {"groups":[{"name":"...","ids":[1,2,3]}]}`

// MergeGroup is a proposed (or applied) group of banks that are the same topic.
type MergeGroup struct {
	Name string  `json:"name"`
	IDs  []int64 `json:"ids"`
}

// SuggestBankMerges asks the fast model which active banks of one kind (project or domain) are the same
// topic. Invented or cross-kind ids, and groups under 2 banks, are dropped; a bank appears in at most one group.
func (s *Service) SuggestBankMerges(ctx context.Context, kind string) ([]MergeGroup, error) {
	if kind != KindProject && kind != KindDomain {
		return nil, fmt.Errorf("unknown bank kind %q", kind)
	}
	if s.llm.RoleRef(ctx, "fast") == "" {
		return nil, nil
	}
	bs, err := s.Banks(ctx)
	if err != nil {
		return nil, err
	}
	valid := map[int64]bool{}
	var sb strings.Builder
	n := 0
	for _, b := range bs {
		if b.Kind != kind || b.Status != "active" {
			continue
		}
		valid[b.ID] = true
		fmt.Fprintf(&sb, "%d: %s (%d facts)", b.ID, b.Name, b.Facts)
		if b.Description != "" {
			fmt.Fprintf(&sb, " — %s", b.Description)
		}
		sb.WriteByte('\n')
		n++
	}
	if n < 2 {
		return nil, nil
	}
	out, err := s.llm.Complete(ctx, "role:fast", mergePrompt, sb.String(), true)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Groups []MergeGroup `json:"groups"`
	}
	if err := json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed); err != nil {
		return nil, fmt.Errorf("bank merge suggestion: unparsable model output: %w", err)
	}
	used := map[int64]bool{}
	var groups []MergeGroup
	for _, g := range parsed.Groups {
		var ids []int64
		for _, id := range g.IDs {
			if valid[id] && !used[id] {
				used[id] = true
				ids = append(ids, id)
			}
		}
		if len(ids) < 2 {
			for _, id := range ids {
				delete(used, id)
			}
			continue
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		groups = append(groups, MergeGroup{Name: strings.TrimSpace(g.Name), IDs: ids})
	}
	return groups, nil
}

// AutoMergeBanks applies SuggestBankMerges for both project and domain banks: each group is merged into
// whichever of its banks has the most facts (ties: the oldest). Safe to call often — nothing to merge is
// a no-op, and there is nothing left to merge once a round succeeds.
func (s *Service) AutoMergeBanks(ctx context.Context) ([]MergeResult, error) {
	var out []MergeResult
	for _, kind := range []string{KindProject, KindDomain} {
		groups, err := s.SuggestBankMerges(ctx, kind)
		if err != nil {
			return out, err
		}
		if len(groups) == 0 {
			continue
		}
		bs, err := s.Banks(ctx)
		if err != nil {
			return out, err
		}
		byID := map[int64]Bank{}
		for _, b := range bs {
			byID[b.ID] = b
		}
		for _, g := range groups {
			into := g.IDs[0]
			for _, id := range g.IDs[1:] {
				if b, ok := byID[id]; ok && b.Facts > byID[into].Facts {
					into = id
				}
			}
			r, err := s.MergeBanks(ctx, g.IDs, into, "")
			if err != nil {
				continue // a bank in the group may have changed kind or gone since we listed it; skip, try again next round
			}
			out = append(out, *r)
		}
	}
	return out, nil
}

// nameKey folds a bank name to letters and digits so "Futurama Torrent", "futurama-torrent" and
// "FuturamaTorrent" compare equal.
func nameKey(s string) string {
	var b []rune
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b = append(b, r)
		}
	}
	return string(b)
}

func digitsOf(s string) string {
	var b []rune
	for _, r := range s {
		if unicode.IsDigit(r) {
			b = append(b, r)
		}
	}
	return string(b)
}

func editDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}

// sameBankName reports whether two names of one kind are the same name up to spelling: equal once
// punctuation and case are ignored, or one typo apart (two for long names). Names with different digits
// ("GLM-4.6" vs "GLM-5.3") are different topics however close they look.
func sameBankName(a, b string) bool {
	ka, kb := nameKey(a), nameKey(b)
	if ka == "" || kb == "" {
		return false
	}
	if ka == kb {
		return true
	}
	if digitsOf(ka) != digitsOf(kb) {
		return false
	}
	limit := 0
	switch n := min(len([]rune(ka)), len([]rune(kb))); {
	case n >= 12:
		limit = 2
	case n >= 5:
		limit = 1
	}
	return limit > 0 && editDistance([]rune(ka), []rune(kb)) <= limit
}

// DedupeBanks merges project/domain banks whose names are the same up to spelling (a typo, a plural-free
// variant, different punctuation). No model involved, so it is cheap enough to run on every pass; merges
// go through MergeBanks and can be undone like any other.
func (s *Service) DedupeBanks(ctx context.Context) ([]MergeResult, error) {
	bs, err := s.Banks(ctx)
	if err != nil {
		return nil, err
	}
	var out []MergeResult
	done := map[int64]bool{}
	for i, a := range bs {
		if done[a.ID] || !mergeable(a.Kind) || a.Status != "active" {
			continue
		}
		group := []Bank{a}
		for _, b := range bs[i+1:] {
			if !done[b.ID] && b.Kind == a.Kind && b.Status == "active" && sameBankName(a.Name, b.Name) {
				group = append(group, b)
			}
		}
		if len(group) < 2 {
			continue
		}
		into := group[0]
		var ids []int64
		for _, b := range group {
			done[b.ID] = true
			if b.Facts > into.Facts {
				into = b
			}
		}
		for _, b := range group {
			if b.ID != into.ID {
				ids = append(ids, b.ID)
			}
		}
		r, err := s.MergeBanks(ctx, ids, into.ID, "")
		if err != nil {
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}
