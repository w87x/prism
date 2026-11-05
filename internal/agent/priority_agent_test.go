package agent

import (
	"context"
	"testing"
	"time"

	"prism/internal/llm"
	"prism/internal/testutil"
)

func TestBackgroundInferenceDoesNotOccupyForegroundSemaphore(t *testing.T) {
	h := newHarness(t)
	h.e.SetLLMConcurrency(1)
	slot := h.e.sem()
	slot <- struct{}{}
	defer func() { <-slot }()
	h.fake.Handler = func(_ map[string]any, _ int) testutil.Reply { return testutil.Reply{Content: "done"} }
	ar := &activeRun{Info: RunInfo{Agent: "Background"}}
	ar.window.Store(262144)
	calib := 1.0
	ctx, cancel := context.WithTimeout(llm.WithBackground(context.Background()), 2*time.Second)
	defer cancel()
	res, err := h.e.chat(ctx, "role:chat", "system", nil, nil, ar, "Background", &calib, func() {})
	if err != nil || res.Content != "done" {
		t.Fatalf("background call blocked on foreground semaphore: result=%+v err=%v", res, err)
	}
	if len(slot) != 1 {
		t.Fatal("background changed foreground semaphore occupancy")
	}
}
