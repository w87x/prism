package agent

import (
	"strings"
	"testing"

	"prism/internal/memory"
)

func TestPlanFactPreservesMemoryStanding(t *testing.T) {
	for _, tc := range []struct {
		name string
		fact memory.Fact
		flag string
	}{
		{"unverified", memory.Fact{ID: 1, Bank: "user", Text: "A claim", Confidence: 0.4}, "unverified"},
		{"disputed", memory.Fact{ID: 2, Bank: "user", Text: "A claim", Confidence: 0.8, Status: memory.StatusContested}, "disputed"},
		{"stale conclusion", memory.Fact{ID: 3, Bank: "user", Text: "A claim", Confidence: 0.8, Kind: memory.ConclusionKind, Stale: true}, "needs review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := formatPlanFact(tc.fact)
			if !strings.Contains(got, tc.flag) || !strings.Contains(got, "A claim") || !strings.Contains(got, "(user)") {
				t.Fatalf("planner lost fact or standing: %q", got)
			}
		})
	}
}

func TestPlanNoteIncludesDependenciesAndCompletion(t *testing.T) {
	p := &Plan{Class: "task", Tasks: []PlanTask{
		{Title: "Inspect build", DoneWhen: "Errors are identified"},
		{Title: "Fix errors", Depends: []string{"Inspect build"}, DoneWhen: "Build passes"},
	}}
	got := planNote(p, nil, "")
	for _, want := range []string{"Done when: Errors are identified", "Depends on: Inspect build", "Done when: Build passes", "use as a draft"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan note lacks %q: %s", want, got)
		}
	}
}

func TestPlanDependencyWarning(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tasks   []PlanTask
		warning bool
	}{
		{"valid", []PlanTask{{Title: "A"}, {Title: "B", Depends: []string{"A"}}}, false},
		{"missing", []PlanTask{{Title: "B", Depends: []string{"A"}}}, true},
		{"cycle", []PlanTask{{Title: "A", Depends: []string{"B"}}, {Title: "B", Depends: []string{"A"}}}, true},
		{"duplicate", []PlanTask{{Title: "A"}, {Title: "A"}}, true},
		{"self", []PlanTask{{Title: "A", Depends: []string{"A"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := planDependencyWarning(tc.tasks); (got != "") != tc.warning {
				t.Fatalf("warning = %q", got)
			}
		})
	}
}
