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

func TestDedupeToolOutputsKeepsFirstAndRecentCopies(t *testing.T) {
	blob := strings.Repeat("B", 3000)
	var ms []Msg
	for i := 0; i < 12; i++ {
		ms = append(ms, Msg{Message: llm.Message{Role: "tool", Name: "shell", Content: blob}})
	}
	out := msgsOf(ms)
	dedupeToolOutputs(ms, out)
	if out[0].Content != blob {
		t.Fatalf("the first copy must stay whole")
	}
	if !strings.HasPrefix(out[1].Content, "[identical to an earlier shell output") {
		t.Fatalf("an old repeat was not deduped: %.60q", out[1].Content)
	}
	if out[11].Content != blob {
		t.Fatalf("a repeat inside the recent window must stay verbatim")
	}
	short := []Msg{{Message: llm.Message{Role: "tool", Name: "x", Content: "ok"}}, {Message: llm.Message{Role: "tool", Name: "x", Content: "ok"}}}
	o2 := msgsOf(short)
	dedupeToolOutputs(short, o2)
	if o2[1].Content != "ok" {
		t.Fatalf("short outputs are not worth stubbing")
	}
}

func TestElideStaleOutputsKeepsHeadTailAndSparesRecentAndSmall(t *testing.T) {
	big := strings.Repeat("H", 700) + strings.Repeat("m", 5000) + strings.Repeat("T", 300)
	var ms []Msg
	ms = append(ms, Msg{Message: llm.Message{Role: "tool", Name: "file_read", Content: big}})
	ms = append(ms, Msg{Message: llm.Message{Role: "tool", Name: "file_read", Content: "small"}})
	for i := 0; i < 12; i++ {
		ms = append(ms, Msg{Message: llm.Message{Role: "tool", Name: "file_read", Content: big}})
	}
	view := foldedView(ms)
	if !strings.HasPrefix(view[0].Content, strings.Repeat("H", 700)) || !strings.HasSuffix(view[0].Content, strings.Repeat("T", 300)) || len(view[0].Content) > 1300 {
		t.Fatalf("old bulky output not elided to head+tail (len %d)", len(view[0].Content))
	}
	if view[1].Content != "small" {
		t.Fatalf("small output must be untouched")
	}
	if view[len(view)-1].Content != big {
		t.Fatalf("recent output must be untouched")
	}
	if ms[0].Content != big {
		t.Fatalf("stored history must not change")
	}
}

func TestCtxBreakdownSumsByRole(t *testing.T) {
	view := []llm.Message{{Role: "user", Content: strings.Repeat("a", 400)}, {Role: "tool", Content: strings.Repeat("b", 4000)}}
	b := ctxBreakdown(50, 70, view)
	if b["system"] != 50 || b["tools"] != 70 || b["tool"] <= b["user"] || b["user"] == 0 {
		t.Fatalf("unexpected breakdown %v", b)
	}
}

func TestFoldDelegationsKeepsHeaderAndPointerButNotNeedsInput(t *testing.T) {
	done := "## Task #212 → Cipher [done]\n" + strings.Repeat("Uploaded the files and verified each one. ", 40)
	wait := "## Task #213 → Fetch [needs input]\nQuestion: which folder?\n(Answer it yourself…)"
	ms := []Msg{{Message: llm.Message{Role: "tool", Name: "delegate", Content: done + "\n\n" + wait}}}
	for i := 0; i < 8; i++ {
		ms = append(ms, Msg{Message: llm.Message{Role: "user", Content: "x"}})
	}
	view := foldedView(ms)
	got := view[0].Content
	if !strings.Contains(got, "## Task #212 → Cipher [done]") || !strings.Contains(got, "task_status(212)") || len(got) > 700 {
		t.Fatalf("delegation not folded to header+pointer (len %d): %q", len(got), got)
	}
	if !strings.Contains(got, "Question: which folder?") {
		t.Fatalf("a task waiting for input must stay whole: %q", got)
	}
	if ms[0].Content != done+"\n\n"+wait {
		t.Fatalf("stored history changed")
	}
	recent := []Msg{{Message: llm.Message{Role: "tool", Name: "delegate", Content: done}}}
	if r := foldedView(recent); r[0].Content != done {
		t.Fatalf("a recent delegation result must stay whole")
	}
}
