package agent

import (
	"prism/internal/testutil"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamedTokensRefreshRunActivity(t *testing.T) {
	h := newHarness(t)
	var fresh atomic.Bool
	previous := h.e.Emit
	h.e.Emit = func(kind string, data any) {
		if kind == "run.delta" {
			for _, run := range h.e.ActiveRuns() {
				if run.Agent == "StreamWorker" && time.Since(time.UnixMilli(run.LastActive)) < time.Minute {
					fresh.Store(true)
				}
			}
		}
		previous(kind, data)
	}
	h.fake.Handler = func(_ map[string]any, _ int) testutil.Reply {
		h.e.mu.Lock()
		for _, run := range h.e.runs {
			if run.Info.Agent == "StreamWorker" {
				run.lastActive.Store(time.Now().Add(-time.Hour).UnixMilli())
			}
		}
		h.e.mu.Unlock()
		return testutil.Reply{Reasoning: "Thinking", Content: "Done"}
	}
	_, err := runWorker(t, h, "StreamWorker", nil, "do a small task")
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.Load() {
		t.Fatal("streamed output did not update last-active time")
	}
}
