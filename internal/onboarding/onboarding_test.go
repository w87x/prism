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
