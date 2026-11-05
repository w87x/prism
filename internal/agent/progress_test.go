package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestRepeatedReadResultsWarnWithoutAborting(t *testing.T) {
	g := &loopGuard{}
	for i := 0; i < 6; i++ {
		g.note(fmt.Sprintf("search:%d", i))
	}
	if !g.progressing() {
		t.Fatal("distinct calls should allow extension")
	}
	result := strings.Repeat("No matching listings were found. ", 3)
	for i := 0; i < 2; i++ {
		if got := g.noteReadResult("search", result); got != "" {
			t.Fatalf("warning too early: %s", got)
		}
	}
	if got := g.noteReadResult("search", result); !strings.Contains(got, "identical") {
		t.Fatalf("missing warning: %s", got)
	}
	if g.progressing() || g.abort {
		t.Fatal("repeated evidence must prevent automatic extension without aborting")
	}
	g.noteReadResult("search", strings.Repeat("A different result with new evidence. ", 3))
	if !g.progressing() {
		t.Fatal("new evidence should restore progress")
	}
	g.noteReadResult("search", result)
	g.noteReadResult("search", result)
	g.noteReadResult("search", "short")
	if got := g.noteReadResult("search", result); got != "" {
		t.Fatalf("short intervening result must reset streak: %s", got)
	}
}

func TestRepeatedFetchIgnoresAccessTimestamp(t *testing.T) {
	g := &loopGuard{}
	for i := 0; i < 3; i++ {
		got := g.noteReadResult("web_fetch", fmt.Sprintf("URL: https://example.test/a\nFetched at: 2026-11-05T12:00:0%dZ\nStatus: 200\nThe same page content remains unchanged.", i))
		if i == 2 && !strings.Contains(got, "identical") {
			t.Fatalf("missing repeated evidence warning: %q", got)
		}
	}
}
