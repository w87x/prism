package onboarding

import (
	"fmt"
	"sort"
	"strings"

	"prism/internal/textmatch"
)

// ── "use every available tool, spread across agents" ─────────────────────────────────────────────────────────
//
// Max-tools-per-agent keeps each agent small; it does not by itself make anything use the whole toolbox. With
// CoverAll every tool the planner was offered must end up with at least one agent, so the limit is met by SPREADING
// the tools over more (narrower) agents instead of by dropping them. The tools every agent gets anyway (memory,
// notes, asking colleagues, reporting…) are not in the planner's list and never count against the limit.

// ToolInfo is one tool the planner was offered.
type ToolInfo struct {
	Name, Category, Desc string
}

// uncovered lists the offered tools that no agent has.
func uncovered(items []Draft, eligible []ToolInfo) []ToolInfo {
	have := map[string]bool{}
	for _, d := range items {
		for _, t := range d.Tools {
			have[t] = true
		}
	}
	var out []ToolInfo
	for _, t := range eligible {
		if !have[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// coverageViolation words the gap so a model can act on it.
func coverageViolation(un []ToolInfo, maxTools int) string {
	names := make([]string, 0, len(un))
	for _, t := range un {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	shown := names
	if len(shown) > 40 {
		shown = append(append([]string{}, shown[:40]...), fmt.Sprintf("…and %d more", len(names)-40))
	}
	limit := ""
	if maxTools > 0 {
		limit = fmt.Sprintf(" without any agent exceeding %d tools — add narrower agents where needed", maxTools)
	}
	return fmt.Sprintf("%d tools are not given to any agent: %s. Give each one to the agent whose job it fits%s", len(un), strings.Join(shown, ", "), limit)
}

// cover places, without the model, every tool that is still unassigned: first into the agent with room whose job
// fits it best, and when nobody has room into new agents grouped by the tool's category. With an exact team size
// (Count > 0) or a cap on agents no agent is added, and what cannot be placed is reported.
func (c Constraints) cover(items []Draft, eligible []ToolInfo, taken map[string]bool) ([]Draft, []string) {
	un := uncovered(items, eligible)
	if len(un) == 0 {
		return items, nil
	}
	items = append([]Draft(nil), items...)
	for i := range items {
		items[i].Tools = append([]string(nil), items[i].Tools...)
	}
	sort.SliceStable(un, func(i, j int) bool {
		if un[i].Category != un[j].Category {
			return un[i].Category < un[j].Category
		}
		return un[i].Name < un[j].Name
	})
	room := func(d Draft) bool { return c.MaxTools == 0 || len(d.Tools) < c.MaxTools }
	docs := make([]string, len(items))
	for i := range items {
		docs[i] = items[i].Name + " " + items[i].Group + " " + items[i].Description + " " + strings.Join(items[i].Traits, " ")
	}
	score := func(t ToolInfo, i int) float64 {
		if hits := textmatch.Rank(t.Name+" "+t.Category+" "+t.Desc, []string{docs[i] + " " + strings.Join(items[i].Tools, " ")}, 1); len(hits) > 0 {
			return hits[0].Score
		}
		return 0
	}
	// Place the best matches first: a tool that clearly belongs to an agent must not lose its place to an unrelated
	// tool that merely happened to be handled earlier. A tool no agent fits at all is not forced anywhere yet.
	placed := 0
	rest := append([]ToolInfo(nil), un...)
	for len(rest) > 0 {
		bt, bi, bs := -1, -1, 0.0
		for ti, t := range rest {
			for i := range items {
				if !room(items[i]) {
					continue
				}
				if sc := score(t, i); sc > bs {
					bt, bi, bs = ti, i, sc
				}
			}
		}
		if bt < 0 {
			break
		}
		items[bi].Tools = append(items[bi].Tools, rest[bt].Name)
		rest = append(rest[:bt], rest[bt+1:]...)
		placed++
	}
	spill := map[string][]ToolInfo{}
	for _, t := range rest {
		spill[t.Category] = append(spill[t.Category], t)
	}
	var notes []string
	if placed > 0 {
		notes = append(notes, fmt.Sprintf("gave %d unused tools to the agents they fit", placed))
	}
	if len(spill) == 0 {
		return items, notes
	}
	left := 0
	for _, ts := range spill {
		left += len(ts)
	}
	if c.Count > 0 || (!c.free() && len(items) >= c.agentCap()) {
		// no new agent may be added: use any room that is left rather than drop a tool
		for _, cat := range sortedKeys(spill) {
			for _, t := range spill[cat] {
				best := -1
				for i := range items {
					if room(items[i]) && (best < 0 || len(items[i].Tools) < len(items[best].Tools)) {
						best = i
					}
				}
				if best < 0 {
					continue
				}
				items[best].Tools = append(items[best].Tools, t.Name)
				placed++
				left--
			}
		}
		if left > 0 {
			notes = append(notes, fmt.Sprintf("%d tools could not be placed within the team size you set", left))
		}
		if placed > 0 {
			notes = append([]string{fmt.Sprintf("gave %d unused tools to the agents with room", placed)}, notes...)
		}
		return items, notes
	}
	cats := make([]string, 0, len(spill))
	for k := range spill {
		cats = append(cats, k)
	}
	sort.Strings(cats)
	added := 0
	for _, cat := range cats {
		ts := spill[cat]
		size := c.MaxTools
		if size <= 0 {
			size = len(ts)
		}
		for part, start := 1, 0; start < len(ts); part, start = part+1, start+size {
			if len(items) >= c.agentCap() {
				notes = append(notes, fmt.Sprintf("%d tools could not be placed within the agent limit", left))
				return items, notes
			}
			end := min(start+size, len(ts))
			label := prettyCategory(cat)
			name := label
			if part > 1 {
				name = fmt.Sprintf("%s %d", label, part)
			}
			for taken[strings.ToLower(name)] {
				name += "+"
			}
			taken[strings.ToLower(name)] = true
			var names []string
			for _, t := range ts[start:end] {
				names = append(names, t.Name)
			}
			items = append(items, Draft{Name: name, Group: label, Description: fmt.Sprintf("Everything for %s: %s.", strings.ToLower(label), strings.Join(names[:min(3, len(names))], ", ")),
				Traits: traitsOf(label, names), Tools: names})
			added++
			left -= end - start
		}
	}
	return items, append(notes, fmt.Sprintf("added %d agent(s) for tools nobody had room for", added))
}

func sortedKeys(m map[string][]ToolInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func prettyCategory(cat string) string {
	cat = strings.TrimPrefix(cat, "mcp:")
	cat = strings.NewReplacer("_", " ", "-", " ").Replace(strings.TrimSpace(cat))
	if cat == "" {
		return "General"
	}
	return strings.ToUpper(cat[:1]) + cat[1:]
}

func traitsOf(label string, tools []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.Trim(s, "_- "))
		if len(s) > 2 && !seen[s] && len(out) < 8 {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, w := range strings.Fields(label) {
		add(w)
	}
	for _, t := range tools {
		for _, w := range strings.FieldsFunc(t, func(r rune) bool { return r == '_' || r == '-' }) {
			if w != "mcp" {
				add(w)
			}
		}
	}
	return out
}
