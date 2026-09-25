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
