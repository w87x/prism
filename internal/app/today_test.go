package app

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"prism/internal/agent"
	"prism/internal/memory"
	"prism/internal/tasks"
	"prism/internal/testutil"
)

// Bug: every section of Today was a nil Go slice when nothing had happened yet (no tasks, no scheduler
// configured, no plugins) — a nil slice marshals to JSON null, and the page reads d.<section>.length
// unconditionally once loaded, so a brand-new, empty PRISM crashed the Today page instead of showing empty
// sections. Caught by actually loading the page against a fresh scratch instance, not by this test alone —
// added here so it stays caught.
func TestGetTodayNeverReturnsNullSections(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fake)
	mem := memory.New(d.Pool, r, st)
	a := &App{
		DB:       d,
		Settings: st,
		LLM:      r,
		Memory:   mem,
		Profiles: agent.NewProfileStore(d.Pool),
		Sessions: agent.NewSessionStore(d.Pool),
		Tasks:    tasks.NewStore(d.Pool),
	}
	a.Engine = agent.NewEngine(agent.Deps{DB: d.Pool, LLM: r, Profiles: a.Profiles, Sessions: a.Sessions, Tasks: a.Tasks, Memory: mem, Settings: st})
	// a.Ext is left at its zero value: no scheduler, no plugins configured — exactly the state that broke.

	today := a.GetToday(context.Background())
	if today.NeedsAttention == nil || today.WorkingOn == nil || today.Produced == nil || today.Commitments == nil || today.Projects == nil {
		t.Fatalf("a section is a nil slice, not an empty one: %+v", today)
	}
	b, err := json.Marshal(today)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"needs_attention":null`, `"working_on":null`, `"produced":null`, `"commitments":null`, `"projects":null`} {
		if bytes.Contains(b, []byte(key)) {
			t.Fatalf("a section serialized as JSON null, which crashes the page's unconditional .length read: %s", b)
		}
	}
}
