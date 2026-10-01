package scheduler

import (
	"testing"
	"time"
)

// Bug: a flat 2-minute budget for every predicate check surfaced as a bare "context deadline exceeded" for
// an "llm" predicate — which chains a live web search (or page fetch) with an LLM judging the evidence — on
// real hardware where either leg alone can take a while. "tool" shares the risk (an arbitrary, possibly slow
// MCP tool); everything else is a single fast local/network call and keeps the original budget.
func TestCheckTimeoutGivesSlowerKindsMoreBudget(t *testing.T) {
	for _, k := range []string{"llm", "tool"} {
		if got := checkTimeout(k); got != 5*time.Minute {
			t.Errorf("checkTimeout(%q) = %v, want 5m", k, got)
		}
	}
	for _, k := range []string{"http", "file", "process", "rss", "time", "download", "mail", "shell", ""} {
		if got := checkTimeout(k); got != 2*time.Minute {
			t.Errorf("checkTimeout(%q) = %v, want 2m", k, got)
		}
	}
}
