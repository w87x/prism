package agent

import (
	"context"
	"testing"

	"prism/internal/settings"
	"prism/internal/tasks"
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

// The task-finished hook fires for a task that ended done, and only then.
func TestOnTaskDoneFiresForFinishedTasks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p, err := h.e.Profiles.Save(ctx, Profile{Name: "Quick", Soul: "You are Quick.", Enabled: true, MaxIterations: 6}, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []tasks.Task
	h.e.OnTaskDone = func(_ context.Context, tk tasks.Task) { got = append(got, tk) }
	h.fake.Handler = func(map[string]any, int) testutil.Reply { return testutil.Reply{Content: "all done"} }
	sess, _ := h.e.Sessions.Create(ctx, p.Name, "task", "", 0)
	task, _ := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "user", ToAgent: p.Name, Input: "do it", SessionID: &sess.ID}, true)
	h.e.RunTask(ctx, task, TaskOpts{})
	if len(got) != 1 || got[0].ID != task.ID || got[0].Status != tasks.Done {
		t.Fatalf("hook calls = %+v", got)
	}
}
