package scheduler

import (
	"context"
	"strings"
	"testing"

	"prism/internal/agent"
	"prism/internal/memory"
	"prism/internal/tasks"
	"prism/internal/testutil"
	"prism/internal/tools"
)

// A cron or intent whose text clearly matches an existing project shares that project's topic; otherwise it gets
// one of its own. Deleting a solo topic's cron/intent releases it; deleting one that shares a project topic must
// not touch that shared topic, since other things may still use it.
func TestTopicResolutionAndRelease(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	fake.Handler = func(req map[string]any, _ int) testutil.Reply { return testutil.Reply{Content: `{"project":""}`} }
	r, st := testutil.Setup(t, d, fake)
	reg := tools.NewRegistry(d.Pool)
	e := agent.NewEngine(agent.Deps{DB: d.Pool, LLM: r, Tools: reg, Profiles: agent.NewProfileStore(d.Pool), Sessions: agent.NewSessionStore(d.Pool),
		Tasks: tasks.NewStore(d.Pool), Memory: memory.New(d.Pool, r, st), Settings: st})
	if err := e.Profiles.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: d.Pool, Engine: e, Settings: st, Env: Env{LLM: r}}
	ctx := context.Background()
	if _, err := e.Memory.EnsureBank(ctx, "project", "Trip", "", "Planning the Italy trip"); err != nil {
		t.Fatal(err)
	}

	var deleted []string
	s.DeleteTopic = func(ctx context.Context, name string) error { deleted = append(deleted, name); return nil }

	// no project banks matched: falls back to its own name
	name, bank := s.resolveTopic(ctx, "Check flight prices", "Check flight prices")
	if bank != 0 || name != "Check flight prices" {
		t.Fatalf("got %q %d", name, bank)
	}

	// matches the existing project: shares its topic
	fake.Handler = func(req map[string]any, _ int) testutil.Reply { return testutil.Reply{Content: `{"project":"Trip"}`} }
	name2, bank2 := s.resolveTopic(ctx, "Book hotel", "Book hotel for the Italy trip")
	if bank2 == 0 || name2 != "Trip" {
		t.Fatalf("got %q %d", name2, bank2)
	}

	// a cron sharing that project's topic: deleting it must not release the shared topic
	fake.Handler = func(req map[string]any, _ int) testutil.Reply { return testutil.Reply{Content: `{"project":"Trip"}`} }
	cid, err := s.SaveCron(ctx, Cron{Name: "OPDS catalog check", Agent: "Atlas", Expr: "0 9 * * *", Prompt: "check the trip's OPDS catalog", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCron(ctx, cid); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 {
		t.Fatalf("a shared project topic must not be released: %v", deleted)
	}

	// a solo intent's own topic IS released when it is deleted
	fake.Handler = func(req map[string]any, _ int) testutil.Reply { return testutil.Reply{Content: `{"project":""}`} }
	iid, err := s.CreateIntent(ctx, Intent{Owner: "Atlas", Description: "watch for a price drop", Type: "intent", Notify: true}, Predicate{Kind: "time", At: "2999-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIntent(ctx, iid); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || !strings.Contains(deleted[0], "watch for a price drop") {
		t.Fatalf("solo topic should have been released: %v", deleted)
	}
}
