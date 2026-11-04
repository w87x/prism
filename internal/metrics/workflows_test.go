package metrics

import (
	"context"
	"prism/internal/tasks"
	"prism/internal/testutil"
	"testing"
	"time"
)

func TestWorkflowTraces(t *testing.T) {
	d := testutil.DB(t)
	ctx := context.Background()
	ts := tasks.NewStore(d.Pool)
	root, err := ts.Create(ctx, tasks.Task{FromKind: "user", ToAgent: "Atlas", Input: "test"}, true)
	if err != nil {
		t.Fatal(err)
	}
	child, err := ts.Create(ctx, tasks.Task{ParentID: &root.ID, RootID: root.ID, FromKind: "agent", ToAgent: "Scout", Input: "work", Depth: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(ctx, `INSERT INTO llm_calls(model,agent,ms,ok,task_id) VALUES('fake','Scout',42,true,$1)`, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(ctx, `INSERT INTO tool_calls(agent,tool,ms,ok,task_id) VALUES('Scout','search',3,false,$1)`, child.ID); err != nil {
		t.Fatal(err)
	}
	got, err := (&Store{DB: d.Pool}).WorkflowTraces(ctx, time.Hour)
	if err != nil || len(got) != 1 {
		t.Fatalf("traces=%+v err=%v", got, err)
	}
	v := got[0]
	if v.TaskID != root.ID || v.ModelCalls != 1 || v.ModelMS != 42 || v.ToolCalls != 1 || v.FailedTools != 1 || v.Delegations != 1 || v.MaxDepth != 1 {
		t.Fatalf("trace=%+v", v)
	}
}
