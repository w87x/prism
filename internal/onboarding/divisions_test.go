package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A call is cut off when it stops producing output, not when it takes long: a slow model that keeps writing is left alone.
func TestCallIdleCutsOffSilenceNotSlowness(t *testing.T) {
	ctx := context.Background()
	out, err := callIdle(ctx, 80*time.Millisecond, 5*time.Second, func(c context.Context) (string, error) {
		for i := 0; i < 6; i++ { // 360ms in all, longer than the idle limit, but a token every 60ms
			time.Sleep(60 * time.Millisecond)
			Pulse(c)
		}
		return "done", nil
	})
	if err != nil || out != "done" {
		t.Fatalf("a steadily writing call must finish: %q %v", out, err)
	}
	_, err = callIdle(ctx, 80*time.Millisecond, 5*time.Second, func(c context.Context) (string, error) {
		<-c.Done()
		return "", c.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "nothing for") {
		t.Fatalf("a silent call must be cut off with a clear reason: %v", err)
	}
}

func fakeTools() []ToolInfo {
	var ts []ToolInfo
	for _, g := range []string{"web", "browser", "files", "mail"} {
		for i := 0; i < 3; i++ {
			ts = append(ts, ToolInfo{Name: fmt.Sprintf("%s_%d", g, i), Category: g, Desc: g + " thing"})
		}
	}
	ts = append(ts, ToolInfo{Name: "mcp__nas__list", Category: "mcp:nas", Desc: "list nas"}, ToolInfo{Name: "mcp__nas__add", Category: "mcp:nas", Desc: "add to nas"})
	return ts
}

// The division is repaired in code: invented tool groups are dropped, a group nobody was given is placed, and a fixed team size is divided.
func TestPlanDivisionsRepairsAndDividesTheCount(t *testing.T) {
	ask := func(ctx context.Context, system, user string) (string, error) {
		if !strings.Contains(system, "exactly 4") {
			t.Errorf("the count rule must be in the prompt:\n%s", system)
		}
		return `{"domains":[
			{"name":"Media","scope":"films and downloads on the nas","groups":["nas","web","invented"],"agents":3},
			{"name":"Home","scope":"mail and files","groups":["mail"],"agents":3}]}`, nil
	}
	divs, err := planDivisions(context.Background(), ask, Constraints{Count: 4}, "hints", nil, fakeTools())
	if err != nil {
		t.Fatal(err)
	}
	total, placed := 0, map[string]bool{}
	for _, d := range divs {
		total += d.Agents
		for _, g := range d.Groups {
			if g == "invented" {
				t.Fatal("an unknown group must be dropped")
			}
			placed[g] = true
		}
	}
	if total != 4 {
		t.Fatalf("the agents must add up to the fixed size, got %d: %+v", total, divs)
	}
	for _, g := range []string{"web", "browser", "files", "mail", "nas"} {
		if !placed[g] {
			t.Fatalf("group %s was left out: %+v", g, divs)
		}
	}
	if _, err := planDivisions(context.Background(), func(context.Context, string, string) (string, error) {
		return `{"domains":[{"name":"Everything","scope":"all","groups":["web"],"agents":2}]}`, nil
	}, Constraints{}, "h", nil, fakeTools()); err == nil {
		t.Fatal("a single domain is no division: the caller plans in one piece")
	}
}

// Each domain is planned by its own call that sees only its tools, and its agents live in that domain.
func TestPlanByDomainsGivesEachDomainItsOwnToolsAndGroup(t *testing.T) {
	divs := []Division{{Name: "Media", Scope: "films", Groups: []string{"nas", "web"}, Agents: 1}, {Name: "Home", Scope: "mail", Groups: []string{"mail"}, Agents: 1}}
	ask := func(ctx context.Context, system, user string) (string, error) {
		switch {
		case strings.Contains(user, `"Media" domain`):
			if strings.Contains(system, "mail_0") || !strings.Contains(system, "web_0") {
				t.Errorf("the Media call must see its tools only:\n%s", system)
			}
			return `{"agents":[{"name":"Niko","tools":["web_0"]}]}`, nil
		case strings.Contains(user, `"Home" domain`):
			if strings.Contains(system, "web_0") || !strings.Contains(system, "mail_0") {
				t.Errorf("the Home call must see its tools only:\n%s", system)
			}
			return `{"agents":[{"name":"Mia","tools":["mail_0"]}]}`, nil
		}
		return "", fmt.Errorf("unexpected call")
	}
	parse := func(out string) ([]Draft, error) {
		var p struct{ Agents []Draft }
		if err := jsonUnmarshal(out, &p); err != nil || len(p.Agents) == 0 {
			return nil, fmt.Errorf("bad")
		}
		return p.Agents, nil
	}
	got := planByDomains(context.Background(), divs, Constraints{}, "hints", nil, fakeTools(), ask, parse, func(Progress) {})
	if len(got) != 2 || got[0].Group != "Media" || got[1].Group != "Home" {
		t.Fatalf("agents and their domains: %+v", got)
	}
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// An MCP server often carries several services; the tool names show them. Verb-first and one-word names say nothing about a service, so the
// server stays the unit there.
func TestMCPFamiliesFollowTheServiceInTheToolName(t *testing.T) {
	var ts []ToolInfo
	add := func(server string, names ...string) {
		for _, n := range names {
			ts = append(ts, ToolInfo{Name: "mcp__" + server + "__" + n, Category: "mcp:" + server})
		}
	}
	add("box", "jackett_search", "jackett_list_indexers", "tvmaze_search", "tvmaze_schedule", "rasp_search", "rasp_find_station")
	for i := 0; i < 8; i++ {
		add("box", fmt.Sprintf("synology_fs_op%d", i), fmt.Sprintf("synology_ds_op%d", i))
	}
	add("github", "create_issue", "list_prs", "comment")
	add("solo", "ping")
	got := map[string]int{}
	for _, g := range toolGroups(ts) {
		got[g.name] = len(g.tools)
	}
	want := map[string]int{"jackett": 2, "tvmaze": 2, "rasp": 2, "synology_fs": 8, "synology_ds": 8, "github": 3, "solo": 1}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("group %s has %d tools, want %d (all: %v)", k, got[k], n, got)
		}
	}
}
