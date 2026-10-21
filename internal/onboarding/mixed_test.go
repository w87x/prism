package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"prism/internal/testutil"
	"prism/internal/tools"
)

// Mixed mode: the ready-made agents come as they are, and the model designs agents only for the MCP tools, by domain — here three unrelated
// services behind ONE server, which a per-server split would have lumped into a single agent.
func TestMixedModePlansMCPToolsByDomainAndKeepsTheBuiltinAgents(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	reg := tools.NewRegistry(d.Pool)
	var mcp []string
	for _, family := range []string{"torrent", "files", "timetable"} {
		for i := 0; i < 10; i++ {
			name := fmt.Sprintf("mcp__box__%s_op%d", family, i)
			mcp = append(mcp, name)
			reg.Register(&tools.Tool{Name: name, Description: family + " operation", Category: "mcp:box", Risk: tools.RiskExec, Deferred: true, Params: tools.Obj(""),
				Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "", nil }})
		}
	}
	reg.Register(&tools.Tool{Name: "web_search", Description: "search the web", Category: "web", Risk: tools.RiskRead, Params: tools.Obj(""),
		Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "", nil }})
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		usr, _ := ms[len(ms)-1].(map[string]any)["content"].(string)
		switch {
		case strings.Contains(sys, "Write the system prompt"):
			return testutil.Reply{Content: "You are a specialist. Method: 1. do the work carefully. 2. report concisely with evidence. Rules: never invent facts."}
		case strings.Contains(sys, "divide a user's world"):
			if strings.Contains(sys, "web_search") {
				t.Errorf("the built-in tools must not be offered to the MCP planner")
			}
			return testutil.Reply{Content: `{"domains":[{"name":"Media","scope":"torrents","groups":["torrent"],"agents":1},{"name":"Storage","scope":"files","groups":["files"],"agents":1},{"name":"Travel","scope":"timetables","groups":["timetable"],"agents":1}]}`}
		case strings.Contains(usr, `"Media" domain`):
			return testutil.Reply{Content: `{"agents":[{"name":"Hunter","description":"finds releases","tools":["mcp__box__torrent_op0"]}]}`}
		case strings.Contains(usr, `"Storage" domain`):
			return testutil.Reply{Content: `{"agents":[{"name":"Keeperfs","description":"files","tools":["mcp__box__files_op0"]}]}`}
		case strings.Contains(usr, `"Travel" domain`):
			return testutil.Reply{Content: `{"agents":[{"name":"Rover","description":"journeys","tools":["mcp__box__timetable_op0"]}]}`}
		}
		return testutil.Reply{Content: `{"agents":[]}`}
	}
	var seen []string
	out, used, err := Propose(context.Background(), r, reg, nil, "I watch films and keep files on a NAS", Constraints{}, "", func(p Progress) {
		if p.Draft != nil {
			seen = append(seen, p.Draft.Name)
		}
	}, WithBuiltin(Templates()))
	if err != nil || !used {
		t.Fatalf("propose: used=%v err=%v", used, err)
	}
	by := map[string]Draft{}
	for _, dr := range out {
		by[dr.Name] = dr
	}
	for _, n := range []string{"Scout", "Hearth", "Vigil", "Ledger", "Courier"} {
		if _, ok := by[n]; !ok {
			t.Errorf("the built-in agent %s is missing", n)
		}
	}
	if len(seen) == 0 || seen[0] != Templates()[0].Name {
		t.Errorf("the ready-made agents should be reported first, so autosave takes them: %v", seen)
	}
	for _, n := range []string{"Hunter", "Keeperfs", "Rover"} {
		dr, ok := by[n]
		if !ok {
			t.Fatalf("the dynamic agent %s is missing: %v", n, names(out))
		}
		if dr.Group == "" || len(dr.Soul) < 40 {
			t.Errorf("%s needs a group and a soul: %+v", n, dr)
		}
	}
	// every MCP tool is placed, and each family stays with its own agent
	for _, name := range mcp {
		held := ""
		for _, dr := range out {
			if contains(dr.Tools, name) {
				held += dr.Name + " "
			}
		}
		if held == "" {
			t.Errorf("%s was left without an agent", name)
		}
	}
	if !contains(by["Hunter"].Tools, "mcp__box__torrent_op0") || contains(by["Hunter"].Tools, "mcp__box__files_op0") {
		t.Errorf("Hunter's tools: %v", by["Hunter"].Tools)
	}
}

func names(ds []Draft) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}
