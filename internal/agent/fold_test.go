package agent

import (
	"strings"
	"testing"

	"prism/internal/llm"
)

func foldFixture(attempts int) []Msg {
	big := strings.Repeat("A", 5000)
	ms := []Msg{{Message: llm.Message{Role: "user", Content: "upload the files"}}}
	for i := 0; i < attempts; i++ {
		id := "c" + string(rune('a'+i))
		ms = append(ms,
			Msg{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Name: "upload", Arguments: `{"b64":"` + big + `"}`}}}},
			Msg{Message: llm.Message{Role: "tool", Name: "upload", ToolCallID: id, Content: "Error: invalid base64 at byte 13932\n" + strings.Repeat("noise ", 500)}})
	}
	return ms
}

// Bug: a retry loop kept every failed attempt's full error output AND its full (here 5KB) arguments in
// every later request. Old failures now collapse to one-line facts; the newest stay verbatim.
func TestFoldFailuresCollapsesOldAttemptsButKeepsRecentOnes(t *testing.T) {
	ms := foldFixture(8)
	out := msgsOf(ms)
	foldFailures(ms, out)

	if !strings.HasPrefix(out[2].Content, "[upload failed (attempt 1): invalid base64 at byte 13932") {
		t.Fatalf("oldest failure not folded: %q", out[2].Content)
	}
	if strings.Contains(out[2].Content, "noise") {
		t.Fatalf("folded failure kept the full output")
	}
	if a := out[1].ToolCalls[0].Arguments; len(a) > 300 || !strings.Contains(a, "_elided") {
		t.Fatalf("failed call's arguments not elided (len %d)", len(a))
	}
	last := len(out) - 1
	if !strings.Contains(out[last].Content, "noise") {
		t.Fatalf("the newest failure must stay verbatim")
	}
	if len(ms[1].ToolCalls[0].Arguments) < 5000 {
		t.Fatalf("stored history was modified; only the model's view may change")
	}
	for i := range out { // pairing intact: no message was removed
		if out[i].Role != ms[i].Role {
			t.Fatalf("message %d changed role", i)
		}
	}
}

func TestFoldFailuresLeavesSuccessesAndShortHistoryAlone(t *testing.T) {
	ms := foldFixture(2)
	out := msgsOf(ms)
	foldFailures(ms, out)
	if strings.HasPrefix(out[2].Content, "[upload failed") {
		t.Fatalf("a history shorter than the keep-recent window must not be folded")
	}
	ok := []Msg{{Message: llm.Message{Role: "tool", Name: "x", Content: "fine " + strings.Repeat("y", 900)}}}
	ok = append(ok, make([]Msg, 12)...)
	o2 := msgsOf(ok)
	foldFailures(ok, o2)
	if !strings.HasPrefix(o2[0].Content, "fine") {
		t.Fatalf("a successful result must never be folded")
	}
}

func TestToolResultCapIsRoomyEarlyAndTightWhenFull(t *testing.T) {
	if got := toolResultCap(10_000, 262_144); got != maxToolResultChars {
		t.Errorf("roomy: got %d", got)
	}
	if got := toolResultCap(120_000, 262_144); got != toolResultCapTight {
		t.Errorf("tight: got %d", got)
	}
	if got := toolResultCap(0, 0); got != toolResultCapTight {
		t.Errorf("unknown window must be conservative, got %d", got)
	}
}
