package agent

import (
	"context"
	"testing"

	"prism/internal/llm"
	"prism/internal/testutil"
)

func TestNamedActionReachesRouterAndRetainsModelSelection(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var budget float64
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		budget, _ = req["max_tokens"].(float64)
		return testutil.Reply{Content: "ok"}
	}
	var recorded llm.Call
	h.e.LLM.OnCall(func(c llm.Call) { recorded = c })
	ref := h.e.LLM.ActionRef(ctx, "memory_extract", "fast")
	if _, err := h.e.LLM.Complete(ctx, ref, "system", "input", false); err != nil {
		t.Fatal(err)
	}
	if recorded.Action != "memory_extract" || recorded.Model == "" || !recorded.OK {
		t.Fatalf("recorded=%+v", recorded)
	}
	if budget != 0 {
		t.Fatalf("background output budget = %v", budget)
	}
	if h.e.LLM.Window(ctx, ref) != h.e.LLM.Window(ctx, llm.ModelRef(ref)) {
		t.Fatal("action label changed model resolution")
	}
}
