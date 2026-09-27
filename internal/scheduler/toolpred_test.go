package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestToolPredicate(t *testing.T) {
	out := `{"data":{"tasks":[{"status":"downloading"},{"status":"finished"}]}}`
	env := Env{CallTool: func(ctx context.Context, tool string, a json.RawMessage) (string, error) { return out, nil }}
	p := &Predicate{Kind: "tool", Tool: "mcp__ds__list", Field: "data.tasks.*.status", Expect: `^finished$`}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	r, err := p.Eval(context.Background(), env)
	if err != nil || r.Fired || !strings.Contains(r.Progress, "downloading") {
		t.Fatalf("%+v %v", r, err)
	}
	out = `{"data":{"tasks":[{"status":"finished"}]}}`
	r, _ = p.Eval(context.Background(), env)
	if !r.Fired {
		t.Fatalf("should fire: %+v", r)
	}
	bad := &Predicate{Kind: "tool", Tool: "x", Args: "[1]", Expect: "a"}
	if bad.Validate() == nil {
		t.Fatal("args must be an object")
	}
}

// One agent may keep at most three watches, and an identical one is not started twice.
func TestWatchGuardCapsAndDedupes(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	mk := func(id string) Predicate {
		return Predicate{Kind: "tool", Tool: "mcp__ds__list", Args: `{"id":"` + id + `"}`, Expect: "done"}
	}
	for i, id := range []string{"a", "b", "c"} {
		p := mk(id)
		if ex, err := s.watchGuard(ctx, "Steward", p); err != nil || ex != 0 {
			t.Fatalf("watch %d refused: %d %v", i, ex, err)
		}
		if _, err := s.CreateIntent(ctx, Intent{Owner: "Steward", Type: "watch", Description: "w " + id, CadenceS: 30, Notify: true}, p); err != nil {
			t.Fatal(err)
		}
	}
	if ex, err := s.watchGuard(ctx, "Steward", mk("b")); err != nil || ex == 0 {
		t.Fatalf("identical watch should be reported: %d %v", ex, err)
	}
	if _, err := s.watchGuard(ctx, "Steward", mk("d")); err == nil || !strings.Contains(err.Error(), "already have 3") {
		t.Fatalf("fourth watch should be refused: %v", err)
	}
	if _, err := s.watchGuard(ctx, "Scout", mk("d")); err != nil {
		t.Fatalf("another agent has its own allowance: %v", err)
	}
}

// A wildcard field with changed=true tracks individual items (mirrors the rss predicate): the first check
// silently baselines whatever is already there, and later checks fire only with genuinely new ones, never
// the pre-existing backlog — this is what makes "watch my inbox" or "watch this task list" not re-report
// everything that was already there when the watch was created.
func TestToolPredicateTracksNewItemsOnly(t *testing.T) {
	calls := 0
	var out string
	env := Env{CallTool: func(ctx context.Context, tool string, a json.RawMessage) (string, error) { calls++; return out, nil }}
	p := &Predicate{Kind: "tool", Tool: "mail_list", Field: "data.messages.*", Changed: true}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}

	// first check: two pre-existing messages baseline silently, no fire
	out = `{"data":{"messages":[{"id":"m1","subject":"Old one"},{"id":"m2","subject":"Also old"}]}}`
	r, err := p.Eval(context.Background(), env)
	if err != nil || r.Fired || !strings.Contains(r.Progress, "baseline: 2") {
		t.Fatalf("baseline check: %+v %v", r, err)
	}

	// no change: still nothing fires
	r, err = p.Eval(context.Background(), env)
	if err != nil || r.Fired || !strings.Contains(r.Progress, "none new") {
		t.Fatalf("no-change check: %+v %v", r, err)
	}

	// one new message arrives alongside the old ones: fires with only the new one as evidence
	out = `{"data":{"messages":[{"id":"m1","subject":"Old one"},{"id":"m2","subject":"Also old"},{"id":"m3","subject":"New arrival"}]}}`
	r, err = p.Eval(context.Background(), env)
	if err != nil || !r.Fired || !strings.Contains(r.Progress, "1 new") || !strings.Contains(r.Evidence, "New arrival") || strings.Contains(r.Evidence, "Old one") {
		t.Fatalf("new-item check: %+v %v", r, err)
	}

	// firing again with nothing new does not re-fire on m3
	r, err = p.Eval(context.Background(), env)
	if err != nil || r.Fired {
		t.Fatalf("must not re-fire on an already-seen item: %+v %v", r, err)
	}
	if calls != 4 {
		t.Fatalf("calls = %d", calls)
	}
}
