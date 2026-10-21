package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"prism/internal/llm"
)

// The MCP half of mixed onboarding, in three plain steps, each a small call:
//  1. SORT   — one call that only sorts the connected tools into groups by what they are used for (output: group name + tool names).
//  2. DESCRIBE — one call per group: what the agent for this group is — name, one-line purpose, traits (and, when a group needs more than
//     one agent, how its tools split). The tools are the group's own, so no call has to choose tools.
//  3. WRITE  — the agent's prompt (the existing per-agent soul call).
// A slow local model gets short prompts and short answers, a failed step falls back to something mechanical for that step alone, and every
// tool ends up with exactly one agent.

const sortPrompt = `You sort a user's connected tools into groups by what they are used for. Below are the tools (name — purpose).
Put every tool in exactly one group. Tools of one service, or serving one kind of work, belong together; one server often holds several unrelated services, so do NOT group by server. Give each group a short plain name (for example "Downloads", "Files on the NAS", "Film and TV"). Aim for 2-15 tools per group; a lone tool joins the group it serves best.
Answer JSON only: {"groups":[{"name":"","tools":["tool name"]}]}`

const describePrompt = `You are defining the specialist AI agent(s) for one group of tools in a personal assistant. The agent owns this group end to end.
GROUP: {{GROUP}}
ITS TOOLS (all of them go to the agent{{S}}):
{{TOOLS}}

{{COUNT}}
For each agent give:
- "name": a short, memorable, unique first-name-like name (NOT descriptive like "SearchBot"). It must not be any of: {{EXISTING}}.
- "description": one line — what it is good for, from the tools' real purpose.
- "traits": 4-8 lowercase search keywords.
{{SPLIT}}Answer JSON only: {"agents":[{"name":"","description":"","traits":[]{{TOOLSKEY}}}]}`

const toolsPerAgentDefault = 15

type describedAgent struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Traits      []string `json:"traits"`
	Tools       []string `json:"tools"`
}

type toolSet struct {
	name  string
	tools []ToolInfo
}

func toolLine(t ToolInfo) string {
	d := strings.Join(strings.Fields(t.Desc), " ")
	if r := []rune(d); len(r) > 80 {
		d = string(r[:80]) + "…"
	}
	return t.Name + " — " + d
}

// sortTools is step 1. Unknown names are dropped, a tool named twice stays with its first group, and tools the model forgot go to the group
// of their own family (or their server's); a model that fails or sorts nothing leaves the name-based families to carry on.
func sortTools(ctx context.Context, ask func(ctx context.Context, title, system, user string) (string, error), hints string, eligible []ToolInfo) []toolSet {
	byName := map[string]ToolInfo{}
	var lines []string
	for _, t := range eligible {
		byName[t.Name] = t
		lines = append(lines, toolLine(t))
	}
	families := func() []toolSet {
		var out []toolSet
		for _, g := range toolGroups(eligible) {
			out = append(out, toolSet{g.name, g.tools})
		}
		return out
	}
	if len(eligible) > 150 { // too many to list in one call: the names carry the grouping
		return families()
	}
	out, err := ask(ctx, "Onboarding: sorting your tools into domains", sortPrompt, hints+"\n\nTOOLS:\n"+strings.Join(lines, "\n"))
	if err != nil {
		return families()
	}
	var parsed struct {
		Groups []struct {
			Name  string   `json:"name"`
			Tools []string `json:"tools"`
		} `json:"groups"`
	}
	if json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed) != nil || len(parsed.Groups) == 0 {
		return families()
	}
	placed := map[string]bool{}
	var sets []toolSet
	idx := map[string]int{}
	for _, g := range parsed.Groups {
		name := strings.TrimSpace(g.Name)
		var ts []ToolInfo
		for _, n := range g.Tools {
			if t, ok := byName[strings.TrimSpace(n)]; ok && !placed[t.Name] {
				placed[t.Name] = true
				ts = append(ts, t)
			}
		}
		if name == "" || len(ts) == 0 {
			continue
		}
		if i, dup := idx[strings.ToLower(name)]; dup { // the same group named twice is one group
			sets[i].tools = append(sets[i].tools, ts...)
			continue
		}
		idx[strings.ToLower(name)] = len(sets)
		sets = append(sets, toolSet{name, ts})
	}
	if len(sets) == 0 {
		return families()
	}
	// forgotten tools: with the group that already holds their family, else a group of their own named after the family
	counts := mcpFamilyCounts(eligible)
	famOf := map[string]int{}
	for i, s := range sets {
		for _, t := range s.tools {
			famOf[groupOf(t, counts)] = i
		}
	}
	for _, t := range eligible {
		if placed[t.Name] {
			continue
		}
		fam := groupOf(t, counts)
		if i, ok := famOf[fam]; ok {
			sets[i].tools = append(sets[i].tools, t)
			continue
		}
		famOf[fam] = len(sets)
		sets = append(sets, toolSet{capitalize(fam), []ToolInfo{t}})
	}
	return sets
}

// allocate decides how many agents each group gets: one per group, more for a big group (no agent over the tool limit), and a fixed team
// size or cap is met by merging the smallest groups or giving the biggest ones extra agents.
func allocate(sets []toolSet, cons Constraints) ([]toolSet, []int) {
	per := cons.MaxTools
	if per <= 0 {
		per = toolsPerAgentDefault
	}
	sets = append([]toolSet(nil), sets...)
	sort.SliceStable(sets, func(i, j int) bool { return len(sets[i].tools) > len(sets[j].tools) })
	limit := cons.Count // the most agents there may be (a fixed size is also a cap here; it is filled up below)
	if limit == 0 && !cons.free() {
		limit = cons.agentCap()
	}
	for limit > 0 && len(sets) > limit { // more groups than agents: the two smallest become one
		a, b := sets[len(sets)-2], sets[len(sets)-1]
		merged := toolSet{a.name + " and " + b.name, append(append([]ToolInfo(nil), a.tools...), b.tools...)}
		sets = append(sets[:len(sets)-2], merged)
		sort.SliceStable(sets, func(i, j int) bool { return len(sets[i].tools) > len(sets[j].tools) })
	}
	counts := make([]int, len(sets))
	total := 0
	for i, s := range sets {
		counts[i] = max(1, (len(s.tools)+per-1)/per)
		total += counts[i]
	}
	for limit > 0 && total > limit { // too many agents for the limit: the group with the most loses one
		i := 0
		for j := range counts {
			if counts[j] > counts[i] {
				i = j
			}
		}
		if counts[i] <= 1 {
			break
		}
		counts[i]--
		total--
	}
	for cons.Count > 0 && total < cons.Count { // a fixed size is met: extra agents go to the groups with the most tools per agent
		i, best := -1, 0
		for j := range counts {
			if counts[j] < len(sets[j].tools) && len(sets[j].tools)/counts[j] >= best {
				i, best = j, len(sets[j].tools)/counts[j]
			}
		}
		if i < 0 {
			break
		}
		counts[i]++
		total++
	}
	return sets, counts
}

// describeGroup is step 2 for one group.
func describeGroup(ctx context.Context, ask func(ctx context.Context, title, system, user string) (string, error), hints string, set toolSet, n int, taken map[string]bool, existing []string) []Draft {
	var lines []string
	for _, t := range set.tools {
		lines = append(lines, toolLine(t))
	}
	count, split, toolsKey, s := "Define exactly one agent.", "", "", ""
	if n > 1 {
		count = fmt.Sprintf("Define exactly %d agents.", n)
		split = `- "tools": which of the group's tools this agent holds. Split them between the agents by use, every tool to exactly one agent.` + "\n"
		toolsKey = `,"tools":[]`
		s = "s"
	}
	system := strings.NewReplacer("{{GROUP}}", set.name, "{{TOOLS}}", strings.Join(lines, "\n"), "{{COUNT}}", count, "{{SPLIT}}", split, "{{TOOLSKEY}}", toolsKey,
		"{{S}}", s, "{{EXISTING}}", strings.Join(existing, ", ")).Replace(describePrompt)
	var got []describedAgent
	if out, err := ask(ctx, "Onboarding: describing "+set.name, system, hints); err == nil {
		var parsed struct {
			Agents []describedAgent `json:"agents"`
		}
		if json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed) == nil {
			got = parsed.Agents
		}
	}
	// keep what is usable: a name that is new, up to n of them
	var agents []describedAgent
	for _, a := range got {
		a.Name = strings.TrimSpace(a.Name)
		if a.Name == "" || taken[strings.ToLower(a.Name)] || len(agents) >= n {
			continue
		}
		taken[strings.ToLower(a.Name)] = true
		agents = append(agents, a)
	}
	for len(agents) < n { // the model gave fewer than asked: mechanical ones fill in
		name := capitalize(strings.Fields(set.name)[0])
		for i := 2; taken[strings.ToLower(name)]; i++ {
			name = fmt.Sprintf("%s%d", capitalize(strings.Fields(set.name)[0]), i)
		}
		taken[strings.ToLower(name)] = true
		agents = append(agents, describedAgent{Name: name, Description: "Works with " + set.name + ".", Traits: []string{strings.ToLower(set.name)}})
	}
	// tools: one agent gets the whole group; several split it as the model said, and what it did not place goes round the agents
	assigned := make([][]string, n)
	if n == 1 {
		for _, t := range set.tools {
			assigned[0] = append(assigned[0], t.Name)
		}
	} else {
		owner := map[string]int{}
		for i, a := range agents {
			for _, tn := range a.Tools {
				if _, dup := owner[tn]; !dup {
					owner[tn] = i
				}
			}
		}
		next := 0
		for _, t := range set.tools {
			i, ok := owner[t.Name]
			if !ok {
				i = next % n
				next++
			}
			assigned[i] = append(assigned[i], t.Name)
		}
	}
	var out []Draft
	for i, a := range agents {
		out = append(out, Draft{Name: a.Name, Group: set.name, Description: strings.TrimSpace(a.Description), Traits: a.Traits, Tools: assigned[i]})
	}
	return out
}

// planMCPPipeline runs steps 1 and 2 and returns the agents (without prompts) for the connected MCP tools.
func planMCPPipeline(ctx context.Context, ask func(ctx context.Context, title, system, user string) (string, error), cons Constraints, hints string, existing []string,
	eligible []ToolInfo, progress func(Progress)) []Draft {
	progress(Progress{Stage: "planning", Note: "Sorting your tools into domains…", Total: len(eligible)})
	sets := sortTools(ctx, ask, hints, eligible)
	sets, counts := allocate(sets, cons)
	taken := map[string]bool{}
	for _, n := range existing {
		taken[strings.ToLower(n)] = true
	}
	results := make([][]Draft, len(sets))
	var mu sync.Mutex // names are claimed one group at a time
	var started int
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for i := range sets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			mu.Lock()
			started++
			progress(Progress{Stage: "planning", Note: fmt.Sprintf("Describing the %s agent (%d of %d)…", sets[i].name, started, len(sets)), Total: len(sets)})
			snapshot := map[string]bool{}
			for k := range taken {
				snapshot[k] = true
			}
			mu.Unlock()
			ds := describeGroup(ctx, ask, hints, sets[i], counts[i], snapshot, existing)
			mu.Lock()
			for k := range ds { // a name another group took meanwhile gets a number
				for taken[strings.ToLower(ds[k].Name)] {
					ds[k].Name += "2"
				}
				taken[strings.ToLower(ds[k].Name)] = true
			}
			mu.Unlock()
			results[i] = ds
		}(i)
	}
	wg.Wait()
	var all []Draft
	for _, r := range results {
		all = append(all, r...)
	}
	return all
}
