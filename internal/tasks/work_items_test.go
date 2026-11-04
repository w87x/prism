package tasks

import (
	"context"
	"testing"

	"prism/internal/testutil"
)

func TestWorkItemsFollowOnlyTheirLinkedTasks(t *testing.T) {
	d := testutil.DB(t)
	s := NewStore(d.Pool)
	ctx := context.Background()
	root, err := s.Create(ctx, Task{FromKind: "user", ToAgent: "Atlas", Input: "two outcomes"}, true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(ctx, Task{FromKind: "user", ToAgent: "Atlas", Input: "another request"}, true)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.Create(ctx, Task{ParentID: &root.ID, RootID: root.ID, FromKind: "agent", ToAgent: "Scout", Input: "first outcome"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWorkItems(ctx, root.ID, []WorkItem{{Number: 1, Title: "First", DoneWhen: "Found"}, {Number: 2, Title: "Second"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkWorkItems(ctx, other.ID, child.ID, []int{1}); err == nil {
		t.Fatal("linked task across unrelated requests")
	}
	if err := s.LinkWorkItems(ctx, root.ID, child.ID, []int{1, 99}); err == nil {
		t.Fatal("invalid item accepted")
	}
	items, err := s.WorkItems(ctx, root.ID)
	if err != nil || len(items) != 2 || items[0].Status != "unassigned" {
		t.Fatalf("atomic rollback: %+v %v", items, err)
	}
	if err := s.LinkWorkItems(ctx, root.ID, child.ID, []int{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, child.ID, Partial, "incomplete", "missing", ""); err != nil {
		t.Fatal(err)
	}
	items, err = s.WorkItems(ctx, root.ID)
	if err != nil || items[0].Status != Partial || items[1].Status != "unassigned" {
		t.Fatalf("status: %+v %v", items, err)
	}
	retry, err := s.Create(ctx, Task{ParentID: &root.ID, RootID: root.ID, FromKind: "agent", ToAgent: "Scout", Input: "finish first"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkWorkItems(ctx, root.ID, retry.ID, []int{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, retry.ID, Done, "done", "", ""); err != nil {
		t.Fatal(err)
	}
	items, err = s.WorkItems(ctx, root.ID)
	if err != nil || items[0].Status != Done || items[1].Status != "unassigned" {
		t.Fatalf("retry: %+v %v", items, err)
	}
}

func TestWaitingPlanAssignmentCanBeReplaced(t *testing.T) {
	d := testutil.DB(t)
	s := NewStore(d.Pool)
	ctx := context.Background()
	root, err := s.Create(ctx, Task{FromKind: "user", ToAgent: "Atlas", Input: "search"}, true)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Create(ctx, Task{ParentID: &root.ID, RootID: root.ID, FromKind: "agent", ToAgent: "Hearth", Input: "search"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWorkItems(ctx, root.ID, []WorkItem{{Number: 1, Title: "Search"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkWorkItems(ctx, root.ID, first.ID, []int{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, first.ID, WaitingInput, "", "", "which tool?"); err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(ctx, Task{ParentID: &root.ID, RootID: root.ID, FromKind: "agent", ToAgent: "Scout", Input: "search"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkWorkItems(ctx, root.ID, second.ID, []int{1}); err != nil {
		t.Fatal(err)
	}
	old, err := s.Get(ctx, first.ID)
	if err != nil || old.Status != Cancelled {
		t.Fatalf("old=%+v err=%v", old, err)
	}
	items, err := s.WorkItems(ctx, root.ID)
	if err != nil || len(items) != 1 || items[0].DelegateID == nil || *items[0].DelegateID != second.ID || items[0].Status != Running {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}
