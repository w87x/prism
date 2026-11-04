package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"prism/internal/tools"
)

func TestInferredToolIntent(t *testing.T) {
	cases := map[string]string{
		"Search Avito.ru for FEVM FAEX2":    "web_search",
		"Look up prices online":             "web_search",
		"Search Jackett for a movie":        "",
		"Schedule a weekly web search":      "",
		"Search the local folder for notes": "",
		"Organize a meeting":                "",
	}
	for input, want := range cases {
		if got := inferredToolIntent(input); got != want {
			t.Errorf("%q: got %q, want %q", input, got, want)
		}
	}
}

func TestCapabilityRouting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.e.Tools.Register(
		&tools.Tool{Name: "web_search", Risk: tools.RiskRead},
		&tools.Tool{Name: "private_tool", Only: []string{"SomeoneElse"}},
		&tools.Tool{Name: "exec_test", Risk: tools.RiskExec},
	)
	for _, p := range []Profile{
		{Name: "SearchScheduler", Description: "search Avito.ru schedules", Tools: []string{"clock"}, Enabled: true, Role: RoleWorker},
		{Name: "WebHolder", Description: "research", Tools: []string{"web_search", "private_tool", "exec_test"}, Enabled: true, Role: RoleWorker},
	} {
		if _, err := h.e.Profiles.Save(ctx, p, ""); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.e.bestSpecialistForTools(ctx, "search Avito.ru schedules", "Atlas", []string{"web_search"})
	if err != nil || got != "WebHolder" {
		t.Fatalf("got %q: %v", got, err)
	}
	if got := h.e.namedSpecialist(ctx, "Use WebHolder to search Avito.ru", "Atlas"); got != "WebHolder" {
		t.Fatalf("named: %q", got)
	}
	if got := h.e.namedSpecialist(ctx, "Use Forge to design an agent", "Atlas"); got != "Forge" {
		t.Fatalf("named maintenance agent: %q", got)
	}
	if got := h.e.namedSpecialist(ctx, "Use Missing to search online", "Atlas"); got != "" {
		t.Fatalf("unknown named: %q", got)
	}
	searchRaw, _ := json.Marshal(map[string]any{"query": "research"})
	found, err := h.e.toolAgentFind().Run(ctx, &tools.Env{Agent: "Atlas"}, searchRaw)
	if err != nil || !strings.Contains(found, "web_search") || strings.Contains(found, "private_tool") {
		t.Fatalf("catalog capabilities=%q err=%v", found, err)
	}

	if err := h.e.validateRequiredTools(ctx, "SearchScheduler", []string{"web_search"}); err == nil {
		t.Fatal("accepted missing tool")
	}
	if err := h.e.validateRequiredTools(ctx, "WebHolder", []string{"private_tool"}); err == nil {
		t.Fatal("accepted restricted tool")
	}
	p, err := h.e.Profiles.Get(ctx, "WebHolder")
	if err != nil {
		t.Fatal(err)
	}
	p.Probation = true
	if err := h.e.validateRequestedTools([]string{"exec_test"}, true); err == nil {
		t.Fatal("accepted exec requirement from restricted caller")
	}
	if h.e.profileHasTools(*p, []string{"exec_test"}) {
		t.Fatal("accepted exec tool on probation")
	}
	if err := h.e.Profiles.SetProbation(ctx, p.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, holder := range h.e.holdersOf(ctx, "exec_test") {
		if holder == "WebHolder" {
			t.Fatal("probationary agent advertised as exec tool holder")
		}
	}
	off := false
	if err := h.e.Tools.SetState(ctx, "web_search", &off, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.e.holdersOf(ctx, "web_search")) != 0 {
		t.Fatal("disabled tool advertised as available")
	}
	on := true
	if err := h.e.Tools.SetState(ctx, "web_search", &on, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := h.e.bestSpecialistForTools(ctx, "search", "Atlas", []string{"missing_tool"}); err == nil {
		t.Fatal("accepted missing registered tool")
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"agent": "SearchScheduler", "instruction": "search", "required_tools": []string{"web_search"}}}})
	result, err := h.e.toolDelegate().Run(ctx, &tools.Env{Agent: "Atlas"}, raw)
	if err != nil || !strings.Contains(result, "does not hold") {
		t.Fatalf("result=%s err=%v", result, err)
	}
}

func TestLeafDelegateRespectsTeam(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, p := range []Profile{
		{Name: "Lead", Role: RoleWorker, Enabled: true, Team: []string{"Teammate"}},
		{Name: "Outsider", Role: RoleWorker, Enabled: true},
	} {
		if _, err := h.e.Profiles.Save(ctx, p, ""); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"agent": "Outsider", "instruction": "do a task", "leaf": true}}})
	result, err := h.e.toolDelegate().Run(ctx, &tools.Env{Agent: "Lead"}, raw)
	if err != nil || !strings.Contains(result, "not on your team") {
		t.Fatalf("result=%q err=%v", result, err)
	}
}

func TestRequiredToolsRouteAcrossDomains(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, name := range []string{"fake_library_search", "fake_calendar_write", "fake_repo_read"} {
		h.e.Tools.Register(&tools.Tool{Name: name})
	}
	for _, p := range []Profile{
		{Name: "MediaOwner", Role: RoleWorker, Enabled: true, Group: "Media", Tools: []string{"fake_library_search"}},
		{Name: "CalendarOwner", Role: RoleWorker, Enabled: true, Group: "Planning", Tools: []string{"fake_calendar_write"}},
		{Name: "CodeOwner", Role: RoleWorker, Enabled: true, Group: "Coding", Tools: []string{"fake_repo_read"}},
	} {
		if _, err := h.e.Profiles.Save(ctx, p, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ request, tool, want string }{
		{"find a movie", "fake_library_search", "MediaOwner"},
		{"arrange an appointment", "fake_calendar_write", "CalendarOwner"},
		{"inspect a repository", "fake_repo_read", "CodeOwner"},
	} {
		got, err := h.e.bestSpecialistForTools(ctx, tc.request, "Atlas", []string{tc.tool})
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %s", tc.request, got, err, tc.want)
		}
	}
	if _, err := h.e.bestSpecialistForTools(ctx, "two capabilities", "Atlas", []string{"fake_calendar_write", "fake_repo_read"}); err == nil {
		t.Fatal("must not silently assign an agent missing one requirement")
	}
}
