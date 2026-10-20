package agent

import (
	"strings"
	"unicode"
)

// infraWords are the words of an agent's tool names and description: the systems it works with, which facts mention all the time (see
// memory.RankForRequest, which keeps facts naming such a system out of the "another named thing" pile).
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
