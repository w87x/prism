package agent

import (
	"strings"
	"unicode"

	"prism/internal/memory"
)

// Auto-recall asks memory for what matches the task, and "matches" is mostly shared words: a task to get a torrent of one show recalled a
// standing alert for a different game, because both talk about torrents and Jackett and that fact is well used. A fact about ANOTHER named
// thing is not what the task needs, so when the task names a specific thing (a title such as "Rick and Morty", a version such as "S01"),
// facts that name a different specific thing and none of the task's are pushed down the list (not dropped: the budget keeps the best few,
// and memory_find still finds them). Derived conclusions are left out of auto-recall altogether: they are summaries, searched on demand.

var titleConnectors = map[string]bool{"of": true, "and": true, "the": true, "in": true, "a": true, "to": true, "for": true, "on": true, "de": true, "vs": true}

// namedThings returns the specific things a text names, lower-cased: runs of capitalised words (joined by small connectors) with at least
// two capitalised words, and tokens that mix letters and digits (S01, GLM-4.6). The first word of a sentence is not taken as a name by itself.
func namedThings(text string) map[string]bool {
	out := map[string]bool{}
	words := strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(",;()[]\"“”«»", r) })
	var run []string
	caps := 0
	flush := func() {
		for len(run) > 0 && titleConnectors[strings.ToLower(run[len(run)-1])] {
			run = run[:len(run)-1]
		}
		if caps >= 2 && len(run) > 0 {
			out[strings.ToLower(strings.Join(run, " "))] = true
		}
		run, caps = nil, 0
	}
	sentenceStart := true // only the very first word is exempt; later sentences are rare in a fact
	for _, w := range words {
		w = strings.TrimRight(w, ".:!?")
		if w == "" {
			continue
		}
		r := []rune(w)
		hasDigit, hasLetter := false, false
		for _, c := range r {
			hasDigit = hasDigit || unicode.IsDigit(c)
			hasLetter = hasLetter || unicode.IsLetter(c)
		}
		if hasDigit && hasLetter && len(r) >= 2 {
			out[strings.ToLower(w)] = true
		}
		isCap := unicode.IsUpper(r[0]) && !sentenceStart
		switch {
		case isCap:
			run, caps = append(run, w), caps+1
		case len(run) > 0 && titleConnectors[strings.ToLower(w)]:
			run = append(run, w)
		default:
			flush()
			if sentenceStart && unicode.IsUpper(r[0]) { // a sentence's first word starts a run only if a capitalised word follows
				run, caps = []string{w}, 0
			}
		}
		sentenceStart = false
	}
	flush()
	return out
}

// shares reports whether any named thing of a also appears (as a phrase, or by sharing a whole word of 4+ letters) in b.
func sharesName(a, b map[string]bool) bool {
	for x := range a {
		if b[x] {
			return true
		}
		for y := range b {
			for _, wx := range strings.Fields(x) {
				if len(wx) >= 4 && !titleConnectors[wx] {
					for _, wy := range strings.Fields(y) {
						if wx == wy {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// rankForTask removes conclusions and moves facts about another named thing behind the rest, keeping the order otherwise.
func rankForTask(request string, facts []memory.Fact, infra map[string]bool) []memory.Fact {
	want := namedThings(request)
	var keep, behind []memory.Fact
	for _, f := range facts {
		if f.Kind == "conclusion" {
			continue
		}
		if len(want) > 0 {
			have := namedThings(f.Text)
			for n := range have { // the services and tools the agent works with (Synology, Jackett…) are not "another thing"
				for _, w := range strings.Fields(n) {
					if infra[w] {
						delete(have, n)
						break
					}
				}
			}
			if len(have) > 0 && !sharesName(have, want) {
				behind = append(behind, f)
				continue
			}
		}
		keep = append(keep, f)
	}
	return append(keep, behind...)
}

// infraWords are the words of an agent's tool names and description: the systems it works with, which facts mention all the time.
func infraWords(p *Profile) map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if len(w) >= 4 {
				out[w] = true
			}
		}
	}
	if p != nil {
		for _, t := range p.Tools {
			add(t)
		}
		add(p.Description)
	}
	return out
}
