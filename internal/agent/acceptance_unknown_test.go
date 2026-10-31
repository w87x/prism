package agent

import (
	"context"
	"strings"
	"testing"

	"prism/internal/tasks"
)

func TestUnavailableAcceptanceCheckClearsOldVerdict(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task, err := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "agent", FromName: "Atlas", ToAgent: "Scout", Input: "Find the answer"}, false)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Tasks.SetCriteria(ctx, task.ID, "answer is sourced")
	h.e.Tasks.SetCriteriaResult(ctx, task.ID, true, "")
	h.e.Tasks.SetCriteria(ctx, task.ID, "answer is sourced and dated")
	if meta := h.e.Tasks.MetaOf(ctx, task.ID); meta.CriteriaMet != nil {
		t.Fatalf("changed criteria kept an old verdict: %+v", meta)
	}
	h.e.Tasks.SetCriteriaResult(ctx, task.ID, true, "")
	h.e.LLM = nil
	got := h.e.checkAcceptance(ctx, RunSpec{Task: &task}, &RunResult{Text: "A result"})
	meta := h.e.Tasks.MetaOf(ctx, task.ID)
	if meta.CriteriaMet != nil || !strings.Contains(meta.Gap, "could not") || !strings.Contains(got.Text, "check unavailable") {
		t.Fatalf("unavailable check not visible: meta=%+v result=%q", meta, got.Text)
	}
}
