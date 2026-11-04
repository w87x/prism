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
