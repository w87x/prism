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

// Mixed mode: the ready-made agents come as they are, and the MCP tools go through sort → describe → write — here three unrelated
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
		case strings.Contains(sys, "You sort a user's connected tools"):
			if strings.Contains(usr, "web_search") {
				t.Errorf("the built-in tools must not be offered to the MCP sorter")
			}
			// the model sorts by what the tools are for: the three services behind one server
			fam := map[string][]string{}
			for _, l := range strings.Split(usr, "\n") {
				if i := strings.Index(l, " — "); i > 0 && strings.HasPrefix(l, "mcp__") {
					name := l[:i]
					for _, f := range []string{"torrent", "files", "timetable"} {
						if strings.Contains(name, "__"+f+"_") {
							fam[f] = append(fam[f], `"`+name+`"`)
						}
					}
				}
			}
			return testutil.Reply{Content: `{"groups":[{"name":"Media","tools":[` + strings.Join(fam["torrent"], ",") + `]},{"name":"Storage","tools":[` + strings.Join(fam["files"], ",") + `]},{"name":"Travel","tools":[` + strings.Join(fam["timetable"], ",") + `]}]}`}
		case strings.Contains(sys, "GROUP: Media"):
			return testutil.Reply{Content: `{"agents":[{"name":"Hunter","description":"finds releases","traits":["torrent"]}]}`}
		case strings.Contains(sys, "GROUP: Storage"):
			return testutil.Reply{Content: `{"agents":[{"name":"Keeperfs","description":"files","traits":["nas"]}]}`}
		case strings.Contains(sys, "GROUP: Travel"):
			return testutil.Reply{Content: `{"agents":[{"name":"Rover","description":"journeys","traits":["trains"]}]}`}
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
	if len(by["Hunter"].Tools) != 10 || contains(by["Hunter"].Tools, "mcp__box__files_op0") || by["Hunter"].Group != "Media" {
		t.Errorf("Hunter holds its group's 10 tools only: %v", by["Hunter"].Tools)
	}
}

func names(ds []Draft) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

// Step 1 repairs what a model does: names it invented are dropped, a tool listed twice stays with its first group, a tool it forgot goes with
// its family, and an answer that is no use at all leaves the name-based families to carry on.
func TestSortToolsRepairsTheModelsGrouping(t *testing.T) {
	ts := []ToolInfo{
		{"mcp__box__jackett_search", "mcp:box", "search"}, {"mcp__box__jackett_list", "mcp:box", "list"},
		{"mcp__box__rasp_search", "mcp:box", "routes"}, {"mcp__box__rasp_station", "mcp:box", "station"},
	}
	ask := func(_ context.Context, _, _, _ string) (string, error) {
		return `{"groups":[{"name":"Downloads","tools":["mcp__box__jackett_search","mcp__box__invented"]},{"name":"Downloads","tools":["mcp__box__jackett_search"]},{"name":"Empty","tools":["nope"]}]}`, nil
	}
	got := sortTools(context.Background(), ask, "h", ts)
	by := map[string][]string{}
	for _, g := range got {
		for _, tl := range g.tools {
			by[g.name] = append(by[g.name], tl.Name)
		}
	}
	if len(by["Downloads"]) != 2 || !contains(by["Downloads"], "mcp__box__jackett_list") {
		t.Fatalf("the forgotten jackett tool joins its family's group: %v", by)
	}
	if len(by["Rasp"]) != 2 || len(by["Empty"]) != 0 {
		t.Fatalf("forgotten tools of a family nobody grouped get a group named after it: %v", by)
	}
	failing := func(context.Context, string, string, string) (string, error) { return "", fmt.Errorf("down") }
	fam := sortTools(context.Background(), failing, "h", ts)
	if len(fam) != 2 {
		t.Fatalf("a failed sort leaves the families: %+v", fam)
	}
}

// How many agents each group gets: one by default, more for a big group, merged groups when a fixed size is smaller.
func TestAllocateMeetsTheTeamSize(t *testing.T) {
	mk := func(n int) []ToolInfo { return make([]ToolInfo, n) }
	sets := []toolSet{{"A", mk(30)}, {"B", mk(6)}, {"C", mk(3)}, {"D", mk(2)}}
	_, counts := allocate(sets, Constraints{MaxTools: 12})
	sum := 0
	for _, c := range counts {
		sum += c
	}
	if len(counts) != 4 || counts[0] != 3 || sum != 6 { // A needs ceil(30/12)=3 agents, the others one each
		t.Fatalf("by the tool limit: %v", counts)
	}
	merged, counts := allocate(sets, Constraints{Count: 3, MaxTools: 12})
	sum = 0
	for _, c := range counts {
		sum += c
	}
	if len(merged) != 3 || sum != 3 || !strings.Contains(merged[2].name, " and ") {
		t.Fatalf("a fixed size of 3: %d groups %v (%+v)", len(merged), counts, merged)
	}
	_, counts = allocate([]toolSet{{"A", mk(10)}, {"B", mk(4)}}, Constraints{Count: 4})
	sum = 0
	for _, c := range counts {
		sum += c
	}
	if sum != 4 {
		t.Fatalf("a fixed size above the group count gives extra agents to the biggest group: %v", counts)
	}
}
