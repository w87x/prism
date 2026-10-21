package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/llm"
	"prism/internal/textmatch"
)

// A slow local model cannot write a whole team in one call: the plan is a long JSON answer, and a fixed deadline killed it halfway while it
// was still producing tokens ("context deadline exceeded", then the built-in templates). Two things fix that. A call is judged by whether it
// is still producing output (idle limit) rather than by how long it takes in all; and a big plan is split: first a short call divides the
// user's world into DOMAINS and says which tool groups each needs, then every domain is planned by its own, much smaller call that sees only
// that domain's tools.

type pulseKey struct{}

// Pulse tells the call in progress that it is alive (a token arrived). Completers call it on every streamed delta.
func Pulse(ctx context.Context) {
	if f, _ := ctx.Value(pulseKey{}).(func()); f != nil {
		f()
	}
}

// callIdle runs fn with a context that is cancelled when fn stops pulsing for idle, or after max in all.
func callIdle(ctx context.Context, idle, max time.Duration, fn func(context.Context) (string, error)) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	var stalled atomic.Bool
	timer := time.AfterFunc(idle, func() { stalled.Store(true); cancel() })
	defer timer.Stop()
	cctx = context.WithValue(cctx, pulseKey{}, func() { timer.Reset(idle) })
	out, err := fn(cctx)
	if err != nil && stalled.Load() {
		err = fmt.Errorf("the model produced nothing for %s", idle)
	}
	return out, err
}

// Division is one domain of the user's life or work, with the tool groups its agents need.
type Division struct {
	Name   string   `json:"name"`
	Scope  string   `json:"scope"`
	Groups []string `json:"groups"`
	Agents int      `json:"agents"`
}

const divisionPrompt = `You divide a user's world into a few DOMAINS, each of which will get its own small team of specialist AI agents in a personal assistant called PRISM. Read the user's hints and the tool groups available.

Give the domains the user's needs really call for — each a coherent area of life or work (for example Media and downloads, Home and money, Research, Coding), not a single tool and not "everything else". For each domain:
- "name": short and plain.
- "scope": one line — what this domain covers for this user.
- "groups": the names of the tool groups (from the list below, exactly as written) its agents will need; a group may serve several domains.
- "agents": how many agents the domain needs (1 to 3).
{{COUNT}}

TOOL GROUPS (name — number of tools — some of them):
{{GROUPS}}

Existing agents (do not duplicate their work): {{EXISTING}}.
Answer JSON only: {"domains":[{"name":"","scope":"","groups":[],"agents":1}]}`

type toolGroup struct {
	name  string
	tools []ToolInfo
}

func groupOf(t ToolInfo) string {
	if strings.HasPrefix(t.Category, "mcp:") {
		return strings.TrimPrefix(t.Category, "mcp:")
	}
	if t.Category == "" {
		return "general"
	}
	return t.Category
}

func toolGroups(eligible []ToolInfo) []toolGroup {
	by := map[string][]ToolInfo{}
	for _, t := range eligible {
		g := groupOf(t)
		by[g] = append(by[g], t)
	}
	names := make([]string, 0, len(by))
	for n := range by {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]toolGroup, 0, len(names))
	for _, n := range names {
		out = append(out, toolGroup{n, by[n]})
	}
	return out
}

func groupLines(gs []toolGroup) string {
	var sb strings.Builder
	for _, g := range gs {
		var ex []string
		for i, t := range g.tools {
			if i >= 6 {
				break
			}
			ex = append(ex, t.Name)
		}
		fmt.Fprintf(&sb, "- %s — %d — %s\n", g.name, len(g.tools), strings.Join(ex, ", "))
	}
	return sb.String()
}

// shouldDivide: a small toolset or team is planned in one call; a big one is split.
func shouldDivide(cons Constraints, eligible []ToolInfo) bool {
	return len(eligible) > 24 || cons.Count > 6
}

// planDivisions asks for the domains and repairs the answer mechanically: unknown tool groups are dropped, every group ends up in some
// domain, and a fixed team size is divided between the domains.
func planDivisions(ctx context.Context, ask func(ctx context.Context, system, user string) (string, error), cons Constraints, hints string, existing []string, eligible []ToolInfo) ([]Division, error) {
	gs := toolGroups(eligible)
	known := map[string]bool{}
	for _, g := range gs {
		known[g.name] = true
	}
	count := "Give as many agents in all as the needs call for."
	switch {
	case cons.Count > 0:
		count = fmt.Sprintf("The agents of all domains together must be exactly %d.", cons.Count)
	case !cons.free():
		count = fmt.Sprintf("The agents of all domains together must be at most %d.", cons.agentCap())
	}
	system := strings.NewReplacer("{{COUNT}}", count, "{{GROUPS}}", groupLines(gs), "{{EXISTING}}", strings.Join(existing, ", ")).Replace(divisionPrompt)
	out, err := ask(ctx, system, hints)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Domains []Division `json:"domains"`
	}
	if err := json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed); err != nil {
		return nil, fmt.Errorf("the division was not valid JSON: %v", err)
	}
	var divs []Division
	seen := map[string]bool{}
	for _, d := range parsed.Domains {
		d.Name = strings.TrimSpace(d.Name)
		if d.Name == "" || seen[strings.ToLower(d.Name)] {
			continue
		}
		seen[strings.ToLower(d.Name)] = true
		var keep []string
		for _, g := range d.Groups {
			if known[g] {
				keep = append(keep, g)
			}
		}
		d.Groups = keep
		d.Agents = max(1, min(3, d.Agents))
		divs = append(divs, d)
	}
	if len(divs) < 2 {
		return nil, fmt.Errorf("the model found %d domains: planning in one piece instead", len(divs))
	}
	// every tool group goes to a domain: the one whose scope reads closest, else the first
	placed := map[string]bool{}
	for _, d := range divs {
		for _, g := range d.Groups {
			placed[g] = true
		}
	}
	docs := make([]string, len(divs))
	for i, d := range divs {
		docs[i] = d.Name + " " + d.Scope
	}
	for _, g := range gs {
		if placed[g.name] {
			continue
		}
		best := 0
		if hits := textmatch.Rank(g.name+" "+groupLines([]toolGroup{g}), docs, 1); len(hits) > 0 {
			best = hits[0].Index
		}
		divs[best].Groups = append(divs[best].Groups, g.name)
	}
	// a fixed team size is divided between the domains; a cap trims the largest
	total := 0
	for _, d := range divs {
		total += d.Agents
	}
	target := cons.Count
	if target == 0 && !cons.free() && total > cons.agentCap() {
		target = cons.agentCap()
	}
	for target > 0 && total != target {
		i := 0
		for j := range divs {
			if (total > target && divs[j].Agents > divs[i].Agents) || (total < target && divs[j].Agents < divs[i].Agents) {
				i = j
			}
		}
		if total > target {
			if divs[i].Agents <= 1 {
				divs = divs[:len(divs)-1] // more domains than agents: the last domain goes
				total = 0
				for _, d := range divs {
					total += d.Agents
				}
				continue
			}
			divs[i].Agents--
			total--
		} else {
			divs[i].Agents++
			total++
		}
	}
	return divs, nil
}

// planByDomains plans every domain with its own call and merges the agents. Domains are planned a couple at a time: a local model
// serves them in turn, a hosted one in parallel.
func planByDomains(ctx context.Context, divs []Division, cons Constraints, hints string, existing []string, eligible []ToolInfo,
	ask func(ctx context.Context, system, user string) (string, error), parse func(string) ([]Draft, error), progress func(Progress)) []Draft {
	byGroup := map[string][]ToolInfo{}
	for _, g := range toolGroups(eligible) {
		byGroup[g.name] = g.tools
	}
	results := make([][]Draft, len(divs))
	var mu sync.Mutex
	var started int
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for i := range divs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			d := divs[i]
			mu.Lock()
			started++
			progress(Progress{Stage: "planning", Note: fmt.Sprintf("Planning the %s team (%d of %d)…", d.Name, started, len(divs)), Total: len(divs)})
			mu.Unlock()
			var lines []string
			mcp := map[string][]string{}
			for _, g := range d.Groups {
				for _, t := range byGroup[g] {
					if t.Category != "" && strings.HasPrefix(t.Category, "mcp:") {
						mcp[g] = append(mcp[g], fmt.Sprintf("%s — %s", t.Name, t.Desc))
					} else {
						lines = append(lines, fmt.Sprintf("%s — %s", t.Name, t.Desc))
					}
				}
			}
			lines = append(lines, mcpToolLines(mcp)...)
			c := cons
			c.Count, c.MaxAgents = d.Agents, 0
			system := c.render(strings.Join(existing, ", "), strings.Join(lines, "\n"))
			user := hints + fmt.Sprintf("\n\nYou are planning ONLY the %q domain — %s — and exactly %d agent(s) for it. Other domains are planned separately: do not plan agents for them, and give every agent a distinct name.", d.Name, d.Scope, d.Agents)
			out, err := ask(ctx, system, user)
			if err != nil {
				return
			}
			if ds, err := parse(out); err == nil {
				for k := range ds {
					ds[k].Group = d.Name // a division is the group its agents live in
				}
				results[i] = ds
			}
		}(i)
	}
	wg.Wait()
	var all []Draft
	for _, r := range results {
		all = append(all, r...)
	}
	return all
}
