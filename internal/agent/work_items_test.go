package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"prism/internal/llm"
	"prism/internal/tasks"
	"prism/internal/testutil"
	"prism/internal/tools"
)

func TestDelegationLinksPlanAndChecksItsCriteria(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.e.Profiles.Save(ctx, Profile{Name: "Scout", Role: RoleWorker, Enabled: true, Soul: "You are Scout."}, "test"); err != nil {
		t.Fatal(err)
	}
	root, err := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "user", ToAgent: "Atlas", Input: "Get both items"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.e.Tasks.SaveWorkItems(ctx, root.ID, []tasks.WorkItem{{Number: 1, Title: "Get first", DoneWhen: "First item has an ID"}, {Number: 2, Title: "Get second", DoneWhen: "Second item has an ID"}}); err != nil {
		t.Fatal(err)
	}
	var judgeInput string
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		if strings.Contains(sys, "team lead checking") {
			_, judgeInput, _ = msgAt(req, -1)
			return testutil.Reply{Content: `{"met":true,"gap":""}`}
		}
		return testutil.Reply{Content: "First item ID: 42."}
	}
	args, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"agent": "Scout", "instruction": "Get the first item", "plan_items": []int{1}}}})
	result, err := h.e.toolDelegate().Run(ctx, &tools.Env{Agent: "Atlas", TaskID: root.ID}, args)
	if err != nil {
		t.Fatal(err)
	}
	items, err := h.e.Tasks.WorkItems(ctx, root.ID)
	if err != nil || len(items) != 2 || items[0].Status != tasks.Done || items[1].Status != "unassigned" {
		t.Fatalf("items=%+v err=%v result=%s", items, err, result)
	}
	if !strings.Contains(judgeInput, "First item has an ID") {
		t.Fatalf("judge missed planned criterion: %s", judgeInput)
	}
}

func TestAcceptanceChecksDescendantsButNotSiblingWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	root, _ := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "user", ToAgent: "Atlas", Input: "two jobs"}, true)
	parent, _ := h.e.Tasks.Create(ctx, tasks.Task{ParentID: &root.ID, RootID: root.ID, FromKind: "agent", ToAgent: "Scout", Input: "first job"}, true)
	sibling, _ := h.e.Tasks.Create(ctx, tasks.Task{ParentID: &root.ID, RootID: root.ID, FromKind: "agent", ToAgent: "Scout", Input: "second job"}, true)
	child, _ := h.e.Tasks.Create(ctx, tasks.Task{ParentID: &parent.ID, RootID: root.ID, FromKind: "agent", ToAgent: "Scout", Input: "first subtask"}, true)
	add := func(tk tasks.Task, tool string) {
		s, err := h.e.Sessions.Create(ctx, "Scout", "task", "", tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.e.Tasks.SetSession(ctx, tk.ID, s.ID); err != nil {
			t.Fatal(err)
		}
		_, err = h.e.Sessions.Append(ctx, s.ID, Msg{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: tool, Name: tool, Arguments: "{}"}}}, Provenance: "agent"})
		if err != nil {
			t.Fatal(err)
		}
	}
	add(child, "grandchild_evidence")
	add(sibling, "sibling_evidence")
	var judge string
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, judge, _ = msgAt(req, -1)
		return testutil.Reply{Content: `{"met":true,"gap":""}`}
	}
	_, _, ok := h.e.judgeAcceptance(ctx, "evidence", "request", "report", parent.ID, nil)
	if !ok || !strings.Contains(judge, "grandchild_evidence") || strings.Contains(judge, "sibling_evidence") {
		t.Fatalf("judge received wrong evidence: %s", judge)
	}
}

func TestChatPersistsPlannerDraftWithoutClaimingCompletion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		if strings.Contains(sys, "front door of a personal assistant") {
			return testutil.Reply{Content: `{"class":"task","project":"","chat":"","confidence":"high","tasks":[{"title":"Find first","done_when":"First source found"},{"title":"Queue second","done_when":"Second queued"}],"asks":[]}`}
		}
		return testutil.Reply{Content: "The request was reviewed."}
	}
	if err := h.e.UserMessage(ctx, UserMsg{Text: "Please process both requested items", Channel: "web"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, func() bool { return h.events.count("run.end") > 0 && !h.e.ChatBusy(ChatKey("web", "")) })
	all, err := h.e.Tasks.List(ctx, tasks.Filter{Agent: "Atlas"})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range all {
		if task.FromKind == "user" {
			items, err := h.e.Tasks.WorkItems(ctx, task.ID)
			if err != nil || len(items) != 2 || items[0].Status != "unassigned" || items[1].Status != "unassigned" {
				t.Fatalf("draft=%+v err=%v", items, err)
			}
			return
		}
	}
	t.Fatal("chat task not recorded")
}
