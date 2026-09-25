package agent

import (
	"testing"

	"prism/internal/settings"
	"prism/internal/testutil"
	"prism/internal/tools"
	"prism/internal/tools/builtin"
)

// The built-in coding agents must only name tools that exist, and survive an onboarding "recreate".
func TestCodingProfilesUseRealTools(t *testing.T) {
	d := testutil.DB(t)
	reg := tools.NewRegistry(d.Pool)
	builtin.Register(reg, builtin.Deps{DB: d.Pool, Settings: settings.New(d.Pool), DataDir: t.TempDir()})
	for _, n := range []string{"ask_colleague", "memory_find", "memory_store", "web_search", "web_fetch"} { // registered by other packages
		reg.Register(&tools.Tool{Name: n})
	}
	found := 0
	for _, p := range WellKnown() {
		if p.Name != "Coder" && p.Name != "Reviewer" {
			continue
		}
		found++
		for _, tl := range p.Tools {
			if _, ok := reg.Get(tl); !ok {
				t.Errorf("%s names an unknown tool %q", p.Name, tl)
			}
		}
		if p.Group != "Coding" || p.MaxIterations < 20 || p.System {
			t.Errorf("%s profile = %+v", p.Name, p)
		}
	}
	if found != 2 {
		t.Fatalf("expected Coder and Reviewer, found %d", found)
	}
	if !IsWellKnown("coder") || IsWellKnown("Stranger") {
		t.Fatal("IsWellKnown")
	}
}
