package tasksum

import (
	"context"
	"testing"

	"prism/internal/tasks"
	"prism/internal/testutil"
)

func mkTask(t *testing.T, st *tasks.Store) int64 {
	t.Helper()
	tk, err := st.Create(context.Background(), tasks.Task{FromKind: "user", FromName: "user", ToAgent: "Atlas", Input: "do the thing"}, false)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return tk.ID
}

func TestSaveIsUpsertByTask(t *testing.T) {
	d := testutil.DB(t)
	ts := tasks.NewStore(d.Pool)
	s := &Store{DB: d.Pool}
	id := mkTask(t, ts)

	if _, err := s.Save(context.Background(), Summary{TaskID: id, Title: "first pass", Goal: "compare prices", Outcome: "found none"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := s.Save(context.Background(), Summary{TaskID: id, Title: "revised", Goal: "compare prices", Outcome: "found three"}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "revised" || got.Outcome != "found three" {
		t.Fatalf("expected the second save to replace the first, got %+v", got)
	}
	all, err := s.List(context.Background(), 100)
	if err != nil || len(all) != 1 {
		t.Fatalf("expected exactly one row per task, got %d (%v)", len(all), err)
	}
}

func TestGetOnUnknownTaskErrors(t *testing.T) {
	d := testutil.DB(t)
	s := &Store{DB: d.Pool}
	if _, err := s.Get(context.Background(), 999999); err == nil {
		t.Fatal("expected an error for a task with no summary")
	}
}

func TestFindRanksByRelevance(t *testing.T) {
	d := testutil.DB(t)
	ts := tasks.NewStore(d.Pool)
	s := &Store{DB: d.Pool}

	a := mkTask(t, ts)
	b := mkTask(t, ts)
	c := mkTask(t, ts)
	if _, err := s.Save(context.Background(), Summary{TaskID: a, Title: "Cheapest flight to Lisbon", Goal: "find a cheap flight to Lisbon", Attempts: "tried Skyscanner, tried Kayak", Outcome: "booked via Kayak"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), Summary{TaskID: b, Title: "Weekly grocery run", Goal: "order groceries", Outcome: "ordered from the usual store"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), Summary{TaskID: c, Title: "Hotel in Lisbon", Goal: "find a hotel near the Lisbon airport", Outcome: "booked a hotel"}); err != nil {
		t.Fatal(err)
	}

	hits, err := s.Find(context.Background(), "Lisbon flight", 5)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(hits) == 0 || hits[0].TaskID != a {
		t.Fatalf("expected the flight summary to rank first for a flight query, got %+v", hits)
	}
	for _, h := range hits {
		if h.TaskID == b {
			t.Fatalf("the unrelated grocery summary should not match: %+v", hits)
		}
	}
}

// A summary's usefulness does not fade with age: two equally-relevant summaries should come back
// most-recent-first, since List already orders that way and Find must not reorder ties.
func TestFindTieBreaksByRecency(t *testing.T) {
	d := testutil.DB(t)
	ts := tasks.NewStore(d.Pool)
	s := &Store{DB: d.Pool}

	older := mkTask(t, ts)
	newer := mkTask(t, ts)
	if _, err := s.Save(context.Background(), Summary{TaskID: older, Title: "Older widget research", Goal: "research widget suppliers", Outcome: "shortlisted two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), Summary{TaskID: newer, Title: "Newer widget research", Goal: "research widget suppliers", Outcome: "shortlisted two"}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Find(context.Background(), "widget suppliers research", 5)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(hits) < 2 || hits[0].TaskID != newer || hits[1].TaskID != older {
		t.Fatalf("expected newer task first on a tie, got %+v", hits)
	}
}
