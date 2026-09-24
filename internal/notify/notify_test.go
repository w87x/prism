package notify

import (
	"context"
	"testing"

	"prism/internal/testutil"
)

// An approval that has been answered (anywhere) must stop counting as unread news.
func TestResolvedAsksAreMarkedRead(t *testing.T) {
	d := testutil.DB(t)
	s := &Store{DB: d.Pool}
	ctx := context.Background()
	for _, it := range []Item{
		{Kind: "ask", Level: "attention", Title: "Cipher asks for approval", Ref: "ask:7"},
		{Kind: "ask", Level: "attention", Title: "Scout asks for approval", Ref: "ask:8"},
		{Kind: "error", Level: "error", Title: "Task failed", Ref: "tasks"},
	} {
		if _, err := s.Add(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	if _, unread, _ := s.List(ctx, 10); unread != 3 {
		t.Fatalf("unread %d", unread)
	}
	if n, err := s.MarkReadRef(ctx, "ask:7"); err != nil || n != 1 {
		t.Fatalf("MarkReadRef: %d %v", n, err)
	}
	if n, _ := s.MarkReadRef(ctx, "ask:7"); n != 0 {
		t.Fatal("marking twice changes nothing")
	}
	items, unread, _ := s.List(ctx, 10)
	if unread != 2 {
		t.Fatalf("only the answered ask leaves the count: %d", unread)
	}
	for _, it := range items {
		if it.Ref == "ask:8" && it.Read {
			t.Fatal("other asks stay unread")
		}
	}
}
