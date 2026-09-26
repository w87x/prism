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
	mk := func(id string) Predicate { return Predicate{Kind: "tool", Tool: "mcp__ds__list", Args: `{"id":"` + id + `"}`, Expect: "done"} }
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
