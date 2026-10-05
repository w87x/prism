package onboarding

import (
	"context"
	"encoding/json"
	"prism/internal/agent"
	"strings"
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
	_, _, _ = Propose(context.Background(), r, reg, nil, "I track GitHub issues", 3, "", nil)
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
	out, used, err := Propose(context.Background(), r, reg, nil, "hints", 4, "", func(p Progress) {
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
	out, used, err := Propose(context.Background(), r, reg, nil, "hints", 2, "", nil)
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
