package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"prism/internal/llm"
	"prism/internal/tasks"
	"strings"
	"testing"
)

func TestMissingSavedArtifactChecksOnlyClaimedToolOutput(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sess, err := h.e.Sessions.Create(ctx, "Scout", "task", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("result"), 0600); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := h.e.DB.QueryRow(ctx, `INSERT INTO artifacts(name,path,size,created_by) VALUES('report.txt',$1,6,'Scout') RETURNING id`, path).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Msg{{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "save", Name: "artifact_save", Arguments: `{}`}}}}, {Message: llm.Message{Role: "tool", ToolCallID: "save", Content: fmt.Sprintf("Artifact #%d saved", id)}}} {
		if _, err := h.e.Sessions.Append(ctx, sess.ID, m); err != nil {
			t.Fatal(err)
		}
	}
	result := fmt.Sprintf("Here is artifact #%d", id)
	if got, err := h.e.missingSavedArtifact(ctx, sess.ID, result); err != nil || got != "" {
		t.Fatalf("existing: %q %v", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := h.e.missingSavedArtifact(ctx, sess.ID, result); err != nil || !strings.Contains(got, "no file") {
		t.Fatalf("missing: %q %v", got, err)
	}
	task, err := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "agent", ToAgent: "Scout", Input: "save report", SessionID: &sess.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Tasks.SetCriteria(ctx, task.ID, "save the report")
	before := len(h.fake.Recorded())
	checked := h.e.checkAcceptance(ctx, RunSpec{Task: &task}, &RunResult{Text: result})
	if !strings.Contains(checked.Text, "NOT met") || len(h.fake.Recorded()) != before {
		t.Fatalf("deterministic check: %q", checked.Text)
	}
	if meta := h.e.Tasks.MetaOf(ctx, task.ID); meta.CriteriaMet == nil || *meta.CriteriaMet {
		t.Fatalf("criteria: %+v", meta)
	}
	if got, err := h.e.missingSavedArtifact(ctx, sess.ID, "No artifact was requested"); err != nil || got != "" {
		t.Fatalf("unclaimed: %q %v", got, err)
	}
}
