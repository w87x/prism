package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"prism/internal/agent"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/testutil"
	"prism/internal/tools"
)

// MCP tools are registered as Deferred; the team planner used to skip every deferred tool, so it could
// never propose giving a specialist an MCP tool. They must be offered to it (under their own heading).
func TestProposeOffersMCPToolsToThePlanner(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	reg := tools.NewRegistry(d.Pool)
	reg.Register(&tools.Tool{Name: "mcp__github__create_issue", Description: "[MCP github] Create an issue in a repository. More text.", Category: "mcp:github",
		Risk: tools.RiskExec, Deferred: true, Params: tools.Obj(""),
		Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "", nil }})
	var plannerPrompt string
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		for _, m := range req["messages"].([]any) {
			if c, _ := m.(map[string]any)["content"].(string); strings.Contains(c, "mcp__github__create_issue") {
				plannerPrompt = c
			}
		}
		return testutil.Reply{Content: `{"agents":[]}`}
	}
	_, _, _ = Propose(context.Background(), r, reg, nil, "I track GitHub issues", Constraints{Count: 3}, "", nil)
	if !strings.Contains(plannerPrompt, "MCP tools") || !strings.Contains(plannerPrompt, "mcp__github__create_issue — [MCP github] Create an issue in a repository") {
		t.Fatalf("the planner was not offered the MCP tool: %q", plannerPrompt)
	}
}

// TemplatesWithMCP is the no-model "Use built-in templates" path's only MCP awareness: a connected server's
// tools should fold into a genuinely matching template (git tools -> Keeper) rather than always spawning a
// new one, but a server nothing fits (a music streamer, say) must still get its own draft instead of being
// forced into an unrelated template.
func TestTemplatesWithMCP(t *testing.T) {
	reg := tools.NewRegistry(nil)
	mk := func(name, category, desc string) *tools.Tool {
		return &tools.Tool{Name: name, Category: category, Description: desc, Deferred: true, Risk: tools.RiskExec, Params: tools.Obj(""),
			Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "", nil }}
	}
	reg.Register(
		mk("mcp__github__list_prs", "mcp:github", "List open pull requests for a repository"),
		mk("mcp__github__create_commit", "mcp:github", "Create a commit on a branch"),
		mk("mcp__spotify__play", "mcp:spotify", "Start playback of a track or playlist"),
		mk("mcp__spotify__search", "mcp:spotify", "Search the streaming catalog for tracks and albums"),
	)
	ds := TemplatesWithMCP(reg)

	keeper := -1
	for i, d := range ds {
		if d.Name == "Keeper" {
			keeper = i
		}
	}
	if keeper < 0 {
		t.Fatal("Keeper template missing")
	}
	if !contains(ds[keeper].Tools, "mcp__github__list_prs") || !contains(ds[keeper].Tools, "mcp__github__create_commit") {
		t.Fatalf("github MCP tools should have folded into Keeper (git/github specialist): %v", ds[keeper].Tools)
	}
	// the github tools must not also have been duplicated into an unrelated new template
	for _, d := range ds {
		if d.Group == "MCP" && strings.Contains(strings.ToLower(d.Name), "github") {
			t.Fatalf("github should have fit Keeper, not spawned its own template: %+v", d)
		}
	}

	var spotify *Draft
	for i, d := range ds {
		if d.Group == "MCP" {
			spotify = &ds[i]
		}
	}
	if spotify == nil {
		t.Fatalf("no built-in template fits a music streamer — it should have gotten its own draft: %+v", ds)
	}
	if !contains(spotify.Tools, "mcp__spotify__play") || !contains(spotify.Tools, "mcp__spotify__search") {
		t.Fatalf("the new draft should carry all of the server's tools: %v", spotify.Tools)
	}
	for _, d := range ds {
		if d.Name != spotify.Name && contains(d.Tools, "mcp__spotify__play") {
			t.Fatalf("spotify tools must not also have been folded into %s", d.Name)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// The team's souls are independent, so they are written in parallel (bounded), the final list keeps the planner's
// order, and progress events never overlap (the callback feeds a websocket and is not required to be reentrant).
func TestProposeWritesSoulsInParallelButReportsInOrder(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	reg := tools.NewRegistry(d.Pool)
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		if strings.Contains(sys, "Write the system prompt") {
			user, _ := ms[len(ms)-1].(map[string]any)["content"].(string)
			name := user[strings.Index(user, "Agent: ")+7:]
			name = name[:strings.IndexByte(name, '\n')]
			// DelayMS runs outside the fake's lock: four overlapping requests take one delay, four queued ones take four
			return testutil.Reply{DelayMS: 250, Content: "You are " + name + ", a specialist. Method: 1. do the work carefully. 2. report concisely with evidence. Rules: never invent facts."}
		}
		return testutil.Reply{Content: `{"agents":[{"name":"Alpha","group":"G","description":"a"},{"name":"Beta","group":"G","description":"b"},{"name":"Gamma","group":"G","description":"c"},{"name":"Delta","group":"G","description":"d"}]}`}
	}
	var busy atomic.Int32
	var overlap atomic.Bool
	var drafts []string
	start := time.Now()
	out, used, err := Propose(context.Background(), r, reg, nil, "hints", Constraints{Count: 4, AllowDelegate: true}, "", func(p Progress) {
		if busy.Add(1) > 1 {
			overlap.Store(true)
		}
		time.Sleep(2 * time.Millisecond)
		if p.Draft != nil {
			drafts = append(drafts, p.Draft.Name)
		}
		busy.Add(-1)
	})
	if err != nil || !used {
		t.Fatalf("propose: used=%v err=%v", used, err)
	}
	elapsed := time.Since(start)
	if elapsed > 800*time.Millisecond { // four 250ms requests in sequence would take over a second
		t.Fatalf("souls must be written concurrently, took %v", elapsed)
	}
	if overlap.Load() {
		t.Fatalf("progress callbacks must never run at the same time")
	}
	if len(drafts) != 4 {
		t.Fatalf("every draft is reported as it lands: %v", drafts)
	}
	names := []string{}
	for _, d := range out {
		names = append(names, d.Name)
		if !strings.Contains(d.Soul, "You are "+d.Name) {
			t.Fatalf("each draft must carry its OWN soul: %s → %q", d.Name, d.Soul)
		}
	}
	if strings.Join(names, ",") != "Alpha,Beta,Gamma,Delta" {
		t.Fatalf("the final team keeps the planner's order: %v", names)
	}
}

// While a team is being generated the preview showed every agent with the default robot (a generated draft has
// no icon yet). Each draft now carries a purpose-based hint for the preview — but the hint is never saved, so the
// created agent still gets its icon chosen by the model from its soul.
func TestGeneratedDraftsGetAnIconHintThatIsNotSaved(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	reg := tools.NewRegistry(d.Pool)
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		if strings.Contains(sys, "Write the system prompt") {
			return testutil.Reply{Content: "You are Scout, a specialist. Method: 1. do the work carefully. 2. report concisely with evidence. Rules: never invent facts."}
		}
		return testutil.Reply{Content: `{"agents":[
			{"name":"Scout","group":"Web","description":"searches the web, checks prices and news","traits":["web","search","prices"]},
			{"name":"Ledger","group":"Finance","description":"parses spreadsheets and csv files, computes statistics","traits":["data","csv","statistics"]}]}`}
	}
	out, used, err := Propose(context.Background(), r, reg, nil, "hints", Constraints{Count: 2}, "", nil)
	if err != nil || !used {
		t.Fatalf("propose: %v %v", used, err)
	}
	hints := map[string]string{}
	for _, dr := range out {
		hints[dr.Name] = dr.IconHint
		if dr.IconHint == "" {
			t.Fatalf("%s has no icon hint", dr.Name)
		}
		if dr.Icon != "" {
			t.Fatalf("the hint must not be the saved icon: %+v", dr)
		}
	}
	if hints["Scout"] == hints["Ledger"] {
		t.Fatalf("different purposes should not share one icon: %v", hints)
	}
	ps := agent.NewProfileStore(d.Pool)
	if _, err := Apply(context.Background(), ps, out, false); err != nil {
		t.Fatal(err)
	}
	p, err := ps.Get(context.Background(), "Scout")
	if err != nil {
		t.Fatal(err)
	}
	if p.Icon != "" {
		t.Fatalf("a created agent must start without an icon so the model can pick one, got %q", p.Icon)
	}
}

func regWithTools(n int) *tools.Registry {
	reg := tools.NewRegistry(nil)
	for i := 1; i <= n; i++ {
		reg.Register(&tools.Tool{Name: fmt.Sprintf("tool_%d", i), Description: "does thing " + fmt.Sprint(i), Category: "x", Risk: tools.RiskRead, Params: tools.Obj(""),
			Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "", nil }})
	}
	return reg
}

func TestConstraintsViolationsAndEnforcement(t *testing.T) {
	valid := map[string]bool{"a": true, "b": true, "c": true, "d": true}
	plan := []Draft{
		{Name: "One", Group: "Home", Tools: []string{"a", "b", "c", "d"}, CanDelegate: true},
		{Name: "Two", Group: "home", Tools: []string{"a"}},
		{Name: "Three", Group: "Web", Tools: []string{"nonsense", "a"}},
	}
	c := Constraints{MaxTools: 2, MaxAgents: 2, Style: "domain"}
	v := strings.Join(c.violations(plan, valid, nil), "\n")
	for _, want := range []string{"3 agents; at most 2", "One has 4 tools; at most 2", "One has can_delegate true", "share the domain"} {
		if !strings.Contains(v, want) {
			t.Errorf("violations lack %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "Three has") {
		t.Errorf("unknown tool names must not count against the limit: %s", v)
	}
	out, notes := c.enforce(plan)
	if len(out) != 2 || len(out[0].Tools) != 2 || out[0].Tools[0] != "a" || out[0].CanDelegate || len(notes) < 2 {
		t.Fatalf("enforce: %+v %v", out, notes)
	}
	if got := strings.Join((Constraints{Count: 4}).violations(plan, valid, nil), "\n"); !strings.Contains(got, "exactly 4") {
		t.Fatalf("an exact count is a limit too: %v", got)
	}
	if v := (Constraints{}).violations(plan[:1], valid, nil); len(v) != 1 || !strings.Contains(v[0], "can_delegate") {
		t.Fatalf("with no limits only delegation (off by default) is flagged: %v", v)
	}
}

// The limits are written into the planner's prompt, a plan that breaks them goes back once with the exact
// violations, and the corrected plan is what the user sees.
func TestProposeSendsABrokenPlanBackOnceWithTheViolations(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	reg := regWithTools(10)
	var planSystem string
	var planCalls int
	var repairUser string
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		user, _ := ms[len(ms)-1].(map[string]any)["content"].(string)
		if strings.Contains(sys, "Write the system prompt") {
			return testutil.Reply{Content: "You are Someone, a specialist. Method: 1. do the work carefully. 2. report concisely with evidence. Rules: never invent facts."}
		}
		planCalls++
		planSystem = sys
		if strings.Contains(user, "breaks these limits") {
			repairUser = user
			return testutil.Reply{Content: `{"agents":[{"name":"Alpha","group":"Home","description":"home","tools":["tool_1","tool_2","tool_3","tool_4"]}]}`}
		}
		return testutil.Reply{Content: `{"agents":[{"name":"Alpha","group":"Home","description":"home","tools":["tool_1","tool_2","tool_3","tool_4","tool_5","tool_6","tool_7","tool_8"]}]}`}
	}
	out, used, err := Propose(context.Background(), r, reg, nil, "I run a smart home", Constraints{MaxTools: 4, MaxAgents: 3, Style: "domain"}, "", nil)
	if err != nil || !used {
		t.Fatalf("propose: %v %v", used, err)
	}
	for _, want := range []string{"AT MOST 4 per agent", "AT MOST 3", "DOMAIN-centric", "false for every agent"} {
		if !strings.Contains(planSystem, want) {
			t.Errorf("the planner prompt lacks %q", want)
		}
	}
	if planCalls != 2 || !strings.Contains(repairUser, "Alpha has 8 tools; at most 4") {
		t.Fatalf("one repair round with the violation expected: calls=%d repair=%q", planCalls, repairUser)
	}
	if len(out) != 1 || len(out[0].Tools) != 4 {
		t.Fatalf("the corrected plan is what the user sees: %+v", out)
	}
}

// A model that ignores the limits (and the repair request) still cannot get past them: the app trims.
func TestProposeEnforcesLimitsTheModelIgnores(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	reg := regWithTools(10)
	var planCalls int
	var notes []string
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		if strings.Contains(sys, "Write the system prompt") {
			return testutil.Reply{Content: "You are Someone, a specialist. Method: 1. do the work carefully. 2. report concisely with evidence. Rules: never invent facts."}
		}
		planCalls++
		var as []string
		for _, n := range []string{"A", "B", "C", "D"} {
			as = append(as, fmt.Sprintf(`{"name":"Agent%s","group":"G%s","description":"d","can_delegate":true,"tools":["tool_1","tool_2","tool_3","tool_4","tool_5","tool_6"]}`, n, n))
		}
		return testutil.Reply{Content: `{"agents":[` + strings.Join(as, ",") + `]}`}
	}
	out, _, err := Propose(context.Background(), r, reg, nil, "x", Constraints{MaxTools: 3, MaxAgents: 2}, "", func(p Progress) {
		if strings.HasPrefix(p.Note, "Applied your limits") {
			notes = append(notes, p.Note)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if planCalls != 2 {
		t.Fatalf("exactly one repair round, got %d plan calls", planCalls)
	}
	if len(out) != 2 {
		t.Fatalf("capped at 2 agents: %d", len(out))
	}
	for _, a := range out {
		if len(a.Tools) != 3 || a.CanDelegate || a.Tools[0] != "tool_1" {
			t.Fatalf("each agent trimmed to 3 tools in the model's order, no delegation: %+v", a)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "trimmed 6 tools to 3") {
		t.Fatalf("the user is told what was applied: %v", notes)
	}
}

func TestAutoCountAndNoLimitsLeaveThePlanAlone(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	reg := regWithTools(10)
	var planCalls int
	var planSystem string
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		if strings.Contains(sys, "Write the system prompt") {
			return testutil.Reply{Content: "You are Someone, a specialist. Method: 1. do the work carefully. 2. report concisely with evidence. Rules: never invent facts."}
		}
		planCalls++
		planSystem = sys
		return testutil.Reply{Content: `{"agents":[{"name":"A","group":"G1","description":"d","tools":["tool_1","tool_2","tool_3","tool_4","tool_5","tool_6","tool_7"]},{"name":"B","group":"G2","description":"d","tools":["tool_8"]}]}`}
	}
	out, _, err := Propose(context.Background(), r, reg, nil, "x", Constraints{}, "", nil)
	if err != nil || planCalls != 1 || len(out) != 2 || len(out[0].Tools) != 7 {
		t.Fatalf("a plan within the (default) limits is used as is, with no repair: calls=%d out=%+v err=%v", planCalls, out, err)
	}
	if !strings.Contains(planSystem, "as many agents as the user's needs call for") {
		t.Fatalf("auto count wording missing: %s", planSystem[:200])
	}
}

// Team size 0 and Max agents 0 is full auto: the model decides how many agents, only a safety ceiling remains.
func TestFullAutoLetsTheModelDecideTheTeamSize(t *testing.T) {
	c := Constraints{}
	if !c.free() || !strings.Contains(c.countRule(), "you decide how many") || strings.Contains(c.countRule(), "AT MOST") {
		t.Fatalf("full auto wording: %q", c.countRule())
	}
	var plan []Draft
	for i := 0; i < 12; i++ {
		plan = append(plan, Draft{Name: fmt.Sprintf("A%d", i), Group: fmt.Sprintf("G%d", i)})
	}
	if v := c.violations(plan, nil, nil); len(v) != 0 {
		t.Fatalf("a big plan is fine in full auto: %v", v)
	}
	if out, notes := c.enforce(plan); len(out) != 12 || len(notes) != 0 {
		t.Fatalf("nothing is trimmed in full auto: %d %v", len(out), notes)
	}
	var runaway []Draft
	for i := 0; i < 45; i++ {
		runaway = append(runaway, Draft{Name: fmt.Sprintf("R%d", i)})
	}
	if out, _ := c.enforce(runaway); len(out) != hardAgentCeiling {
		t.Fatalf("the safety ceiling still applies: %d", len(out))
	}
	if (Constraints{MaxAgents: 5}).free() || (Constraints{Count: 3}).free() {
		t.Fatal("any explicit size or cap means not free")
	}
}

func TestMCPToolsAreListedPerServerWithAWildcardForAWholeServer(t *testing.T) {
	few := map[string][]string{"github": {"mcp__github__b — x", "mcp__github__a — y"}, "spotify": {"mcp__spotify__play — z"}}
	lines := strings.Join(mcpToolLines(few), "\n")
	for _, want := range []string{"mcp__<server>__*", "mcp__github__a — y", "mcp__spotify__play — z"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("few tools are all listed, with the wildcard rule; missing %q:\n%s", want, lines)
		}
	}
	many := map[string][]string{}
	for i := 0; i < 120; i++ {
		many["big"] = append(many["big"], fmt.Sprintf("mcp__big__t%03d — thing", i))
	}
	many["small"] = []string{"mcp__small__only — one"}
	got := mcpToolLines(many)
	text := strings.Join(got, "\n")
	if len(got) > 6 || !strings.Contains(text, `server "big" — 120 tools`) || !strings.Contains(text, "mcp__big__*") || strings.Contains(text, "mcp__big__t100") {
		t.Fatalf("a big roster is summarised per server, not dumped (nor silently cut at 60):\n%s", text)
	}
	if mcpToolLines(nil) != nil {
		t.Fatal("no MCP servers, no lines")
	}
}

func TestExpandToolsHandlesWholeServerWildcards(t *testing.T) {
	valid := map[string]bool{"shell": true, "mcp__gh__a": true, "mcp__gh__b": true, "mcp__other__x": true}
	got := expandTools([]string{"shell", "mcp__gh__*", "mcp__gh__a", "nonsense", "mcp__none__*"}, valid)
	if strings.Join(got, ",") != "shell,mcp__gh__a,mcp__gh__b" {
		t.Fatalf("expand: %v", got)
	}
}

func TestProposeGivesAWholeMCPServerOnAWildcardAndUsesTheCompleter(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	reg := tools.NewRegistry(d.Pool)
	for _, n := range []string{"create_issue", "list_prs", "comment"} {
		reg.Register(&tools.Tool{Name: "mcp__github__" + n, Description: "[MCP github] " + n, Category: "mcp:github", Risk: tools.RiskExec, Deferred: true, Params: tools.Obj(""),
			Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "", nil }})
	}
	reg.Register(&tools.Tool{Name: "mcp__spotify__play", Description: "[MCP spotify] play", Category: "mcp:spotify", Risk: tools.RiskExec, Deferred: true, Params: tools.Obj(""),
		Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "", nil }})
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		if strings.Contains(sys, "Write the system prompt") {
			return testutil.Reply{Content: "You are Hub, a specialist. Method: 1. do the work carefully. 2. report concisely with evidence. Rules: never invent facts."}
		}
		return testutil.Reply{Content: `{"agents":[{"name":"Hub","group":"Code","description":"works with GitHub","tools":["mcp__github__*"]}]}`}
	}
	var titles []string
	var mu sync.Mutex
	viaCompleter := func(ctx context.Context, title, system, user string, jsonOut bool) (string, error) {
		mu.Lock()
		titles = append(titles, title)
		mu.Unlock()
		return r.Complete(ctx, "", system, user, jsonOut)
	}
	out, used, err := Propose(context.Background(), r, reg, nil, "use all available tools", Constraints{Count: 1}, "", nil, WithCompleter(viaCompleter))
	if err != nil || !used || len(out) != 1 {
		t.Fatalf("propose: %v %v %v", used, err, out)
	}
	if len(out[0].Tools) != 3 || strings.Contains(strings.Join(out[0].Tools, ","), "spotify") {
		t.Fatalf("the wildcard must give exactly the github server's tools: %v", out[0].Tools)
	}
	sort.Strings(titles)
	if len(titles) != 2 || titles[0] != "Onboarding: planning the team" || titles[1] != "Onboarding: writing Hub" {
		t.Fatalf("every model call goes through the completer, titled for the UI: %v", titles)
	}
}

func eligibleTools() []ToolInfo {
	return []ToolInfo{
		{"web_search", "web", "search the web for pages"}, {"web_fetch", "web", "fetch a web page"}, {"web_media", "web", "find images and video"},
		{"calendar_add", "calendar", "add a calendar event"}, {"calendar_list", "calendar", "list calendar events"}, {"reminders_add", "calendar", "add a reminder"},
		{"mac_say", "macos", "speak text aloud"}, {"mac_clipboard_get", "macos", "read the clipboard"},
		{"mcp__gh__issues", "mcp:gh", "list issues"}, {"mcp__gh__prs", "mcp:gh", "list pull requests"},
	}
}

// "Use every tool": the limit is met by SPREADING tools over agents, never by dropping them.
func TestCoverAllPlacesEveryToolWithoutBreakingTheLimit(t *testing.T) {
	c := Constraints{MaxTools: 3, CoverAll: true}
	plan := []Draft{
		{Name: "Marlo", Group: "Web", Description: "finds prices and news on the web", Traits: []string{"web", "search"}, Tools: []string{"web_search"}},
		{Name: "Dova", Group: "Home", Description: "reminders and the calendar", Traits: []string{"calendar", "reminders"}, Tools: []string{"calendar_add", "calendar_list"}},
	}
	un := uncovered(plan, eligibleTools())
	if len(un) != 7 {
		t.Fatalf("7 offered tools are unused: %d", len(un))
	}
	if v := coverageViolation(un, 3); !strings.Contains(v, "7 tools are not given to any agent") || !strings.Contains(v, "without any agent exceeding 3") {
		t.Fatalf("violation wording: %s", v)
	}
	if got := strings.Join(c.violations(plan, map[string]bool{"web_search": true, "calendar_add": true, "calendar_list": true}, eligibleTools()), "\n"); !strings.Contains(got, "not given to any agent") {
		t.Fatalf("an unused tool is a violation under CoverAll: %s", got)
	}
	if got := (Constraints{MaxTools: 3}).violations(plan, nil, eligibleTools()); len(got) != 0 {
		t.Fatalf("without CoverAll unused tools are fine: %v", got)
	}
	out, notes := c.cover(plan, eligibleTools(), map[string]bool{"marlo": true, "dova": true})
	have := map[string]bool{}
	for _, d := range out {
		if len(d.Tools) > 3 {
			t.Fatalf("%s breaks the limit with %d tools", d.Name, len(d.Tools))
		}
		for _, tl := range d.Tools {
			if have[tl] {
				t.Fatalf("%s given to two agents", tl)
			}
			have[tl] = true
		}
	}
	for _, e := range eligibleTools() {
		if !have[e.Name] {
			t.Fatalf("%s was dropped", e.Name)
		}
	}
	// the web tool went to the web agent, the reminder to the calendar agent (fit), the rest to new agents by category
	if !strings.Contains(strings.Join(out[0].Tools, ","), "web_fetch") || !strings.Contains(strings.Join(out[1].Tools, ","), "reminders_add") {
		t.Fatalf("tools go to the agent they fit first: %+v", out[:2])
	}
	if len(out) <= 2 || len(notes) < 2 {
		t.Fatalf("overflow becomes new agents, and the user is told: %d %v", len(out), notes)
	}
	var names []string
	for _, d := range out[2:] {
		names = append(names, d.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "Macos") || !strings.Contains(strings.Join(names, ","), "Gh") {
		t.Fatalf("overflow agents are named for their category: %v", names)
	}
	// an exact team size is never exceeded: what does not fit is reported
	fixed, fnotes := (Constraints{Count: 2, MaxTools: 3, CoverAll: true}).cover(plan, eligibleTools(), map[string]bool{})
	if len(fixed) != 2 || !strings.Contains(strings.Join(fnotes, ";"), "could not be placed") {
		t.Fatalf("a fixed team size wins over coverage: %d %v", len(fixed), fnotes)
	}
	// with no tool limit nobody ever lacks room, so no agent is added
	loose, _ := (Constraints{CoverAll: true}).cover(plan, eligibleTools(), map[string]bool{})
	if len(loose) != 2 || len(uncovered(loose, eligibleTools())) != 0 {
		t.Fatalf("without a limit everything fits the existing agents: %d", len(loose))
	}
}

func TestProposeCoverAllRepairsThenFillsWhatTheModelLeaves(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	reg := regWithTools(7)
	var planCalls int
	var repairUser string
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		user, _ := ms[len(ms)-1].(map[string]any)["content"].(string)
		if strings.Contains(sys, "Write the system prompt") {
			return testutil.Reply{Content: "You are Someone, a specialist. Method: 1. do the work carefully. 2. report concisely with evidence. Rules: never invent facts."}
		}
		planCalls++
		if strings.Contains(sys, "USE EVERY TOOL") == false {
			t.Errorf("the planner prompt must carry the coverage rule")
		}
		if strings.Contains(user, "breaks these limits") {
			repairUser = user
		}
		// the model keeps covering only two tools, whatever it is told
		return testutil.Reply{Content: `{"agents":[{"name":"Alpha","group":"A","description":"a","tools":["tool_1","tool_2"]}]}`}
	}
	out, _, err := Propose(context.Background(), r, reg, nil, "use all tools", Constraints{MaxTools: 3, CoverAll: true}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if planCalls != 2 || !strings.Contains(repairUser, "5 tools are not given to any agent") {
		t.Fatalf("one repair round naming the unused tools: calls=%d repair=%q", planCalls, repairUser)
	}
	covered := map[string]bool{}
	for _, a := range out {
		if len(a.Tools) > 3 {
			t.Fatalf("limit broken: %+v", a)
		}
		for _, tl := range a.Tools {
			covered[tl] = true
		}
		if a.Soul == "" {
			t.Fatalf("every agent, added ones included, gets a soul: %s", a.Name)
		}
	}
	if len(covered) != 7 {
		t.Fatalf("all 7 tools must be placed: %v (%d agents)", covered, len(out))
	}
}
