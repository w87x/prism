package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/testutil"
)

func TestScheduledAtlasDelegatesAndStaysQuiet(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.e.Profiles.Save(ctx, Profile{Name: "Scout", Role: RoleWorker, Soul: "You are Scout, a web specialist.", Enabled: true, MaxIterations: 6}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.e.Settings.Set(ctx, settings.KeyAutonomy, settings.Autonomy{RoutineModel: "main", NoticeFilter: "all"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var coordinatorInput, childInput string
	h.fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		role, input, _ := msgAt(req, -1)
		switch {
		case strings.Contains(sys, "You are Atlas") && role == "user":
			mu.Lock()
			coordinatorInput = input
			mu.Unlock()
			return testutil.Reply{Tools: []llm.ToolCall{tc("d1", "delegate", map[string]any{"tasks": []map[string]string{{"agent": "Scout", "instruction": "Check release evidence; only report a change."}}})}}
		case strings.Contains(sys, "You are Scout") && role == "user":
			mu.Lock()
			childInput = input
			mu.Unlock()
			return testutil.Reply{Content: "No change."}
		case strings.Contains(sys, "You are Atlas") && role == "tool":
			return testutil.Reply{Content: "NO_REPLY"}
		}
		return testutil.Reply{Content: "unexpected"}
	}
	task, err := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "cron", FromName: "release watch", ToAgent: "Atlas", Input: "Check release evidence; notify only on change."}, true)
	if err != nil {
		t.Fatal(err)
	}
	before := h.events.count("notice")
	out := h.e.RunTask(ctx, task, TaskOpts{})
	mu.Lock()
	defer mu.Unlock()
	if out.Status != tasks.Done || out.Result != "NO_REPLY" || !strings.Contains(coordinatorInput, "[Scheduled coordination]") || !strings.Contains(childInput, "Check release evidence") {
		t.Fatalf("coordinated run: %+v coordinator=%q child=%q", out, coordinatorInput, childInput)
	}
	if got := h.events.count("notice") - before; got != 0 {
		t.Fatalf("unchanged scheduled run sent %d notices", got)
	}
}

func TestScheduledAtlasUsesRunbookAsCoordinationGuidance(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.e.DB.Exec(ctx, `INSERT INTO runbooks (key,agent,title,steps,status) VALUES ('cron:watch','Atlas','watch','Delegate checking to a suitable specialist.','active')`); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var input string
	h.fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		_, msg, _ := msgAt(req, -1)
		if strings.Contains(sys, "You are Atlas") {
			mu.Lock()
			input = msg
			mu.Unlock()
		}
		return testutil.Reply{Content: "NO_REPLY"}
	}
	task, err := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "cron", FromName: "watch", ToAgent: "Atlas", Input: "Check the item"}, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = h.e.RunTask(ctx, task, TaskOpts{})
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(input, "[Coordination runbook") || !strings.Contains(input, "delegate the specialist work") || !strings.Contains(input, "Delegate specialist work as usual") || strings.Contains(input, "Do the routine steps yourself") {
		t.Fatalf("coordinator received the wrong runbook instructions: %q", input)
	}
}

func TestScheduledAtlasPreservesRequestedPeriodicReports(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var mu sync.Mutex
	var input string
	h.fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		_, msg, _ := msgAt(req, -1)
		if strings.Contains(sys, "deserves to interrupt them") {
			return testutil.Reply{Content: `{"tell":true}`}
		}
		if strings.Contains(sys, "You are Atlas") {
			mu.Lock()
			input = msg
			mu.Unlock()
		}
		return testutil.Reply{Content: "Weekly report: nothing changed."}
	}
	task, err := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "cron", FromName: "weekly", ToAgent: "Atlas", Input: "Send a weekly report even when nothing changes."}, true)
	if err != nil {
		t.Fatal(err)
	}
	before := h.events.count("notice")
	_ = h.e.RunTask(ctx, task, TaskOpts{})
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(input, "including periodic updates when explicitly requested") || h.events.count("notice") != before+1 {
		t.Fatalf("requested report was not preserved: input=%q notices=%d", input, h.events.count("notice")-before)
	}
}

func TestBuiltInCronKeepsItsOwnCoordinatorInstructions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.e.DB.Exec(ctx, `INSERT INTO crons(name,agent,expr,prompt,enabled,system) VALUES('Heartbeat','Atlas','@daily','checklist',true,true)`); err != nil {
		t.Fatal(err)
	}
	p, err := h.e.Profiles.Get(ctx, "Atlas")
	if err != nil {
		t.Fatal(err)
	}
	if h.e.scheduledCoordinator(ctx, tasks.Task{FromKind: "cron", FromName: "Heartbeat"}, p) {
		t.Fatal("built-in heartbeat must retain its own routine instructions")
	}
	if !h.e.scheduledCoordinator(ctx, tasks.Task{FromKind: "intent", FromName: "intent #1"}, p) {
		t.Fatal("an Atlas action intent must use scheduled coordination")
	}
}
