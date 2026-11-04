package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"prism/internal/llm"
	"prism/internal/tasks"
	"prism/internal/testutil"
	"prism/internal/tools"
)

// A missing tool goes to the coordinator, which gets the evidence and resumes the same worker.
func TestCoordinatorResumesLeafAfterMissingCapability(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.e.Tools.Register(&tools.Tool{Name: "web_search", Description: "search the web", Params: tools.Obj(""), Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "source: example", nil }})
	for _, p := range []Profile{
		{Name: "Analyst", Role: RoleWorker, Enabled: true, Soul: "You are Analyst.", Description: "analyse evidence", Tools: []string{"ask_colleague"}},
		{Name: "WebHolder", Role: RoleWorker, Enabled: true, Soul: "You are WebHolder.", Description: "web research", Tools: []string{"web_search"}},
	} {
		if _, err := h.e.Profiles.Save(ctx, p, ""); err != nil {
			t.Fatal(err)
		}
	}
	var analystSawAsk bool
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		role, content, _ := msgAt(req, -1)
		if strings.Contains(sys, "You are Analyst.") {
			if toolNamesIn(req)["ask_colleague"] {
				analystSawAsk = true
			}
			if role == "user" && strings.Contains(content, "source: example") {
				return testutil.Reply{Content: "Analysis using source: example"}
			}
			return testutil.Reply{Tools: []llm.ToolCall{tc("blocked", "report_blocked", map[string]any{"missing": "web_search", "why": "find source", "done": "prepared analysis"})}}
		}
		if strings.Contains(sys, "You are WebHolder.") {
			return testutil.Reply{Content: "source: example"}
		}
		return testutil.Reply{Content: "ok"}
	}
	root, err := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "system", ToAgent: "Atlas", Input: "analyse"}, true)
	if err != nil {
		t.Fatal(err)
	}
	env := &tools.Env{Agent: "Atlas", TaskID: root.ID}
	call := func(v any) string {
		b, _ := json.Marshal(v)
		out, err := h.e.toolDelegate().Run(ctx, env, b)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := call(map[string]any{"tasks": []map[string]any{{"agent": "Analyst", "instruction": "analyse evidence", "leaf": true}}})
	if !strings.Contains(first, "MISSING CAPABILITY: web_search") || !strings.Contains(first, "[needs input]") {
		t.Fatalf("blocked result: %s", first)
	}
	if strings.Contains(first, "ask_user") || !strings.Contains(first, "Route the missing step") {
		t.Fatalf("not actionable for the coordinator: %s", first)
	}
	if analystSawAsk {
		t.Fatal("delegated agent saw ask_colleague despite explicit profile assignment")
	}
	all, err := h.e.Tasks.List(ctx, tasks.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, task := range all {
		if task.ToAgent == "Analyst" {
			id = task.ID
		}
	}
	if id == 0 {
		t.Fatal("missing analyst task")
	}
	evidence := call(map[string]any{"tasks": []map[string]any{{"agent": "WebHolder", "instruction": "get evidence", "leaf": true}}})
	if !strings.Contains(evidence, "source: example") {
		t.Fatalf("evidence: %s", evidence)
	}
	final := call(map[string]any{"tasks": []map[string]any{{"task_id": id, "instruction": "source: example", "leaf": true}}})
	if !strings.Contains(final, "[done]") || !strings.Contains(final, "Analysis using source") {
		t.Fatalf("resume: %s", final)
	}
	if h.events.count("ask.request") != 0 {
		t.Fatal("missing capability reached the user")
	}
}
