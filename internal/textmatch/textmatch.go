// Package textmatch is a tiny BM25 scorer for ranking short documents
// (tool descriptions, skill summaries, agent traits, bookmarks) with no index.
package textmatch

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Tokens lowercases and splits s into word tokens (letters/digits, any script),
// splitting snake_case and camelCase-ish separators too.
func Tokens(s string) []string {
	f := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }
	raw := strings.FieldsFunc(strings.ToLower(s), f)
	out := raw[:0]
	for _, t := range raw {
		if len(t) > 1 || t[0] >= 0x80 {
			out = append(out, t)
		}
	}
	return out
}

type Hit struct {
	Index int
	Score float64
}

// Rank scores docs against query and returns hits (score>0) best-first, up to limit (0 = all).
func Rank(query string, docs []string, limit int) []Hit {
	q := Tokens(query)
	if len(q) == 0 || len(docs) == 0 {
		return nil
	}
	toks := make([][]string, len(docs))
	df := map[string]int{}
	total := 0
	for i, d := range docs {
		toks[i] = Tokens(d)
		total += len(toks[i])
		seen := map[string]bool{}
		for _, t := range toks[i] {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	avg := float64(total)/float64(len(docs)) + 1e-9
	const k1, b = 1.4, 0.75
	var hits []Hit
	for i, tk := range toks {
		tf := map[string]int{}
		for _, t := range tk {
			tf[t]++
		}
		score := 0.0
		for _, qt := range q {
			f := float64(tf[qt])
			if f == 0 {
				// prefix match gives partial credit ("schedul" ~ "schedule")
				for t, c := range tf {
					if len(qt) >= 4 && (strings.HasPrefix(t, qt) || strings.HasPrefix(qt, t) && len(t) >= 4) {
						f += 0.6 * float64(c)
					}
				}
				if f == 0 {
					continue
				}
			}
			n := float64(df[qt])
			idf := math.Log(1 + (float64(len(docs))-n+0.5)/(n+0.5))
			score += idf * (f * (k1 + 1)) / (f + k1*(1-b+b*float64(len(tk))/avg))
		}
		if score > 0 {
			hits = append(hits, Hit{i, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}
