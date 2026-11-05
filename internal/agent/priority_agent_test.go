package agent

import (
	"context"
	"testing"
	"time"

	"prism/internal/llm"
	"prism/internal/testutil"
	"prism/internal/tools"
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

func TestUserQuestionReleasesInteractiveEndpointForMaintenance(t *testing.T) {
	h := newHarness(t)
	h.fake.Handler = func(_ map[string]any, _ int) testutil.Reply { return testutil.Reply{Content: "ok"} }
	ctx, finish := h.e.LLM.BeginInteractive(context.Background(), "role:chat")
	defer finish()
	lowCtx, cancelLow := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelLow()
	done := make(chan error, 1)
	go func() {
		_, err := h.e.LLM.Complete(lowCtx, h.e.LLM.ActionRef(lowCtx, "memory_dedupe", "fast"), "test", "test", false)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("maintenance ran before question: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	askCtx, cancelAsk := context.WithTimeout(ctx, 3*time.Second)
	defer cancelAsk()
	answer := make(chan error, 1)
	go func() {
		_, err := h.e.ask(askCtx, RunInfo{Agent: "Atlas"}, tools.Question{Kind: "clarify", Text: "Choose"})
		answer <- err
	}()
	waitFor(t, time.Second, func() bool { return len(h.e.PendingAsks()) > 0 })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("maintenance did not run while user question was open")
	}
	pending := h.e.PendingAsks()
	h.e.AnswerAsk(pending[0]["id"].(int64), "yes")
	select {
	case err := <-answer:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("answer was not delivered")
	}
	if !llm.IsInteractive(ctx) {
		t.Fatal("interactive priority did not resume")
	}
}
