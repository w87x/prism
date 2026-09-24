package tasks

import (
	"context"
	"testing"
	"time"

	"prism/internal/testutil"
)

func TestQueueLifecycle(t *testing.T) {
	d := testutil.DB(t)
	s := NewStore(d.Pool)
	ctx := context.Background()
	root, err := s.Create(ctx, Task{FromKind: "user", FromName: "user", ToAgent: "Atlas", Input: "do things\nmore"}, false)
	if err != nil || root.Title != "do things" || root.RootID != root.ID || root.Status != Queued {
		t.Fatalf("create: %+v %v", root, err)
	}
	child, _ := s.Create(ctx, Task{ParentID: &root.ID, RootID: root.ID, FromKind: "agent", FromName: "Atlas", ToAgent: "Scout", Input: "x", Depth: 1, Priority: 5}, true)
	// higher priority queued work is claimed first
	low, _ := s.Create(ctx, Task{FromKind: "cron", ToAgent: "A", Input: "low"}, false)
	hi, _ := s.Create(ctx, Task{FromKind: "cron", ToAgent: "B", Input: "hi", Priority: 9}, false)
	got, _ := s.ClaimNext(ctx)
	if got == nil || got.ID != hi.ID || got.Status != Running {
		t.Fatalf("claim order: %+v (want %d)", got, hi.ID)
	}
	// Wait wakes when the task finishes
	done := make(chan Task, 1)
	go func() { tk, _ := s.Wait(ctx, child.ID); done <- tk }()
	time.Sleep(100 * time.Millisecond)
	_ = s.Finish(ctx, child.ID, Done, "result!", "", "")
	select {
	case tk := <-done:
		if tk.Status != Done || tk.Result != "result!" || tk.FinishedAt == nil {
			t.Fatalf("wait: %+v", tk)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not wake")
	}
	// restart safety: running tasks go back to the queue
	n, _ := s.RequeueRunning(ctx)
	if n != 1 { // only `hi` was still running
		t.Fatalf("requeued %d", n)
	}
	// multi-turn continuation
	_ = s.Finish(ctx, low.ID, WaitingInput, "", "", "which shop?")
	r, err := s.Resume(ctx, low.ID)
	if err != nil || r.Status != Running || r.Question != "" {
		t.Fatalf("resume: %+v %v", r, err)
	}
	if list, _ := s.List(ctx, Filter{RootID: root.ID}); len(list) != 2 {
		t.Fatalf("tree: %d", len(list))
	}
}

// Rerun must be restricted to the user's own direct requests to Atlas: never a delegated sub-task, never
// something another agent or the scheduler started, and never anything still in flight.
func TestRerunnable(t *testing.T) {
	cases := []struct {
		name string
		task Task
		want bool
	}{
		{"cancelled direct user request to Atlas", Task{Status: Cancelled, FromKind: "user", ToAgent: "Atlas"}, true},
		{"still running", Task{Status: Running, FromKind: "user", ToAgent: "Atlas"}, false},
		{"failed, not cancelled", Task{Status: Failed, FromKind: "user", ToAgent: "Atlas"}, false},
		{"delegated sub-task, not the user's own", Task{Status: Cancelled, FromKind: "agent", FromName: "Atlas", ToAgent: "Scout"}, false},
		{"started by a cron, not the user", Task{Status: Cancelled, FromKind: "cron", ToAgent: "Atlas"}, false},
		{"user request routed to someone other than Atlas", Task{Status: Cancelled, FromKind: "user", ToAgent: "Scout"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.task.Rerunnable(); got != c.want {
				t.Fatalf("Rerunnable() = %v, want %v", got, c.want)
			}
		})
	}
}

// NeedsSummary must offer only terminal tasks with a real session that still need distilling — not
// in-flight work, not a task with no transcript to summarize, not one already resolved, and not one that
// has exhausted its retries (see maxSummaryAttempts).
func TestNeedsSummary(t *testing.T) {
	d := testutil.DB(t)
	s := NewStore(d.Pool)
	ctx := context.Background()
	sess := int64(1)

	mk := func(status string, withSession bool) Task {
		tk, err := s.Create(ctx, Task{FromKind: "user", FromName: "user", ToAgent: "Atlas", Input: "x", SessionID: func() *int64 {
			if withSession {
				return &sess
			}
			return nil
		}()}, true)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := s.Finish(ctx, tk.ID, status, "done", "", ""); err != nil {
			t.Fatalf("finish: %v", err)
		}
		return tk
	}

	pending := mk(Done, true)
	failedPending := mk(Failed, true)
	noSession := mk(Done, false)
	cancelled := mk(Cancelled, true)

	due, err := s.NeedsSummary(ctx, 50)
	if err != nil {
		t.Fatalf("needs summary: %v", err)
	}
	ids := map[int64]bool{}
	for _, t2 := range due {
		ids[t2.ID] = true
	}
	if !ids[pending.ID] || !ids[failedPending.ID] {
		t.Fatalf("expected done and failed terminal tasks with a session to be offered: %+v", due)
	}
	if ids[noSession.ID] {
		t.Fatal("a task with no session has no transcript to summarize and must be excluded")
	}
	if ids[cancelled.ID] {
		t.Fatal("cancelled tasks are not summarized")
	}

	if err := s.MarkSummarized(ctx, pending.ID); err != nil {
		t.Fatalf("mark summarized: %v", err)
	}
	due, _ = s.NeedsSummary(ctx, 50)
	for _, t2 := range due {
		if t2.ID == pending.ID {
			t.Fatal("a summarized task must not be offered again")
		}
	}

	for i := 0; i < maxSummaryAttempts; i++ {
		if err := s.BumpSummaryAttempts(ctx, failedPending.ID); err != nil {
			t.Fatalf("bump: %v", err)
		}
	}
	due, _ = s.NeedsSummary(ctx, 50)
	for _, t2 := range due {
		if t2.ID == failedPending.ID {
			t.Fatal("a task that exhausted its retries must not be offered again")
		}
	}
}
