package app

import (
	"strings"
	"testing"
)

func TestLiveFeedTail(t *testing.T) {
	var a App
	a.live.observe("run.start", map[string]any{"run": int64(7), "agent": "Coder"})
	for i := 0; i < 5; i++ {
		a.live.observe("run.delta", map[string]any{"run": int64(7), "kind": "thinking", "text": "abc"})
	}
	a.live.observe("run.tool", map[string]any{"run": int64(7), "tool": "x", "phase": "start"})
	a.live.observe("run.delta", map[string]any{"run": int64(7), "kind": "content", "text": "hi"})
	l := a.Live()[7]
	if l.Start["agent"] != "Coder" || len(l.Events) != 3 {
		t.Fatalf("want start + 3 events (merged stream, tool, content), got %+v", l)
	}
	if l.Events[0]["text"] != "abcabcabcabcabc" {
		t.Fatalf("tokens of one stream should merge: %v", l.Events[0]["text"])
	}
	// bounded: a very long stream keeps only its tail
	a.live.observe("run.delta", map[string]any{"run": int64(7), "kind": "content", "text": strings.Repeat("z", 20000)})
	total := 0
	for _, e := range a.Live()[7].Events {
		if s, ok := e["text"].(string); ok {
			total += len(s)
		}
	}
	if total > liveMaxChars {
		t.Fatalf("tail not bounded: %d chars", total)
	}
	a.live.observe("run.end", map[string]any{"run": int64(7)})
	if _, ok := a.Live()[7]; ok {
		t.Fatal("ended run must be dropped")
	}
}
