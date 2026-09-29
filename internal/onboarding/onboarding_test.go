package onboarding

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
