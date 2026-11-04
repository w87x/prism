package agent

import (
	"context"
	"encoding/json"
	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/testutil"
	"prism/internal/tools"
	"strings"
	"testing"
)

func TestDeferredMCPOnlyLoadsAssignedTools(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.e.Settings.Set(ctx, settings.KeyGeneral, settings.General{DeferredMCP: true}); err != nil {
		t.Fatal(err)
	}
	h.e.Tools.Register(&tools.Tool{Name: "mcp__demo__read", Source: "mcp:1", Deferred: true, Risk: tools.RiskRead, Params: tools.Obj(""), Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "real result", nil }}, &tools.Tool{Name: "mcp__demo__other", Source: "mcp:1", Deferred: true, Risk: tools.RiskRead})
	var first, second map[string]bool
	h.fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		role, content, _ := msgAt(req, -1)
		if role == "user" {
			first = toolNamesIn(req)
			return testutil.Reply{Tools: []llm.ToolCall{tc("denied", "tool_search", map[string]any{"query": "mcp__demo__other", "load": "mcp__demo__other"})}}
		}
		if strings.Contains(content, "not an available deferred") {
			return testutil.Reply{Tools: []llm.ToolCall{tc("find", "tool_search", map[string]any{"query": "mcp__demo__read", "load": "mcp__demo__read"})}}
		}
		if strings.Contains(content, "Loaded the assigned") {
			second = toolNamesIn(req)
			return testutil.Reply{Tools: []llm.ToolCall{tc("read", "mcp__demo__read", map[string]any{})}}
		}
		return testutil.Reply{Content: content}
	}
	res, err := runWorker(t, h, "MCPWorker", []string{"mcp__demo__read"}, "read something")
	if err != nil || !strings.Contains(res.Text, "real result") {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if first["mcp__demo__read"] || !second["mcp__demo__read"] || second["mcp__demo__other"] {
		t.Fatalf("first=%v second=%v", first, second)
	}
}

func TestDeferredMCPDoesNotBypassRestrictions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.e.Settings.Set(ctx, settings.KeyGeneral, settings.General{DeferredMCP: true}); err != nil {
		t.Fatal(err)
	}
	h.e.Tools.Register(&tools.Tool{Name: "mcp__test__read", Source: "mcp:1", Deferred: true, Risk: tools.RiskRead}, &tools.Tool{Name: "mcp__test__write", Source: "mcp:1", Deferred: true, Risk: tools.RiskExec})
	p, err := h.e.Profiles.Save(ctx, Profile{Name: "RestrictedWorker", Role: RoleWorker, Enabled: true, Tools: []string{"mcp__test__read", "mcp__test__write"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	h.fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		role, content, _ := msgAt(req, -1)
		if role == "user" {
			return testutil.Reply{Tools: []llm.ToolCall{tc("load", "tool_search", map[string]any{"query": "write", "load": "mcp__test__write"})}}
		}
		return testutil.Reply{Content: content}
	}
	for _, spec := range []RunSpec{{Restricted: true}, {ReadOnly: true}} {
		sess, err := h.e.Sessions.Create(ctx, p.Name, "task", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		spec.Profile = p
		spec.Session = sess
		spec.Input = "attempt"
		out, err := h.e.Run(ctx, spec)
		if err != nil || !strings.Contains(out.Text, "not an available deferred") {
			t.Fatalf("restriction result=%+v err=%v", out, err)
		}
	}
}
