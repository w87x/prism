package tracker

import (
	"context"
	"testing"
	"time"

	"prism/internal/testutil"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	d := testutil.DB(t)
	return &Service{DB: d.Pool}
}

func mkTracker(t *testing.T, s *Service, name string) *Tracker {
	t.Helper()
	tr, err := s.Create(context.Background(), name, "test tracker", []Column{{Name: "Price"}, {Name: "Location"}}, "test")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return tr
}

func TestCreateRejectsDuplicateNameAndEmptyColumns(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	mkTracker(t, s, "Apartments")
	if _, err := s.Create(ctx, "Apartments", "", []Column{{Name: "x"}}, "test"); err == nil {
		t.Fatal("expected a duplicate tracker name to be refused")
	}
	if _, err := s.Create(ctx, "Empty", "", nil, "test"); err == nil {
		t.Fatal("expected a tracker with no columns to be refused")
	}
}

// A brand new row reports no changes — there's nothing to diff it against yet.
func TestUpsertNewRowHasNoChanges(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTracker(t, s, "Apartments")
	row, changes, err := s.UpsertRow(ctx, tr.ID, "https://listing/1", map[string]any{"Price": "1200", "Location": "Downtown"}, "https://listing/1")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("a brand new row should report no changes, got %+v", changes)
	}
	if row.Data["Price"] != "1200" || row.Status != "active" {
		t.Fatalf("row not as expected: %+v", row)
	}
}

// Re-submitting the same key with a changed field must report exactly that field changed, and leave
// untouched fields as they were.
func TestUpsertExistingRowReportsOnlyChangedFields(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTracker(t, s, "Apartments")
	if _, _, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1200", "Location": "Downtown"}, ""); err != nil {
		t.Fatal(err)
	}
	row, changes, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1100"}, "")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if len(changes) != 1 || changes[0].Field != "Price" || changes[0].OldValue != "1200" || changes[0].NewValue != "1100" {
		t.Fatalf("expected exactly one Price change 1200->1100, got %+v", changes)
	}
	if row.Data["Location"] != "Downtown" {
		t.Fatalf("untouched field must be preserved, got %+v", row.Data)
	}
}

// Resubmitting identical data must be a true no-op: no change rows, and the row's updated_at must not move
// (so "nothing happened" is actually nothing happened, not silent churn).
func TestUpsertIdenticalDataIsANoOp(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTracker(t, s, "Apartments")
	first, _, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1200"}, "https://x")
	if err != nil {
		t.Fatal(err)
	}
	second, changes, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1200"}, "https://x")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("identical data must produce no changes, got %+v", changes)
	}
	if !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("a true no-op must not touch updated_at: %v vs %v", first.UpdatedAt, second.UpdatedAt)
	}
}

// The change log persists in tracker_changes and is readable back via Changes, in newest-first order.
func TestChangesAreRecordedAndOrdered(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTracker(t, s, "Apartments")
	if _, _, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1200"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1100"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1000"}, ""); err != nil {
		t.Fatal(err)
	}
	changes, err := s.Changes(ctx, tr.ID, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 recorded price changes, got %d: %+v", len(changes), changes)
	}
	if changes[0].NewValue != "1000" || changes[1].NewValue != "1100" {
		t.Fatalf("expected newest-first order, got %+v", changes)
	}
}

// Retiring a row marks it gone and logs the retirement as a change; a subsequent sighting (upsert) must
// revive it back to active and record that too.
func TestRetireThenReviveRoundTrips(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTracker(t, s, "Apartments")
	if _, _, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1200"}, ""); err != nil {
		t.Fatal(err)
	}
	retired, err := s.RetireRow(ctx, tr.ID, "k1", "listing taken down")
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if retired.Status != "gone" {
		t.Fatalf("expected status gone, got %q", retired.Status)
	}
	rows, err := s.Rows(ctx, tr.ID, "", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a retired row must not appear when include_gone is false, got %+v", rows)
	}

	revived, changes, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1150"}, "")
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if revived.Status != "active" {
		t.Fatalf("expected the row to come back active, got %q", revived.Status)
	}
	foundStatusChange := false
	for _, c := range changes {
		if c.Field == "status" && c.OldValue == "gone" && c.NewValue == "active" {
			foundStatusChange = true
		}
	}
	if !foundStatusChange {
		t.Fatalf("expected a status gone->active change to be recorded, got %+v", changes)
	}
}

// Retiring an already-gone row must be a harmless no-op, not a duplicate change entry.
func TestRetiringAnAlreadyGoneRowIsANoOp(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTracker(t, s, "Apartments")
	if _, _, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1200"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetireRow(ctx, tr.ID, "k1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetireRow(ctx, tr.ID, "k1", ""); err != nil {
		t.Fatal(err)
	}
	changes, err := s.Changes(ctx, tr.ID, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range changes {
		if c.Field == "status" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one status change from retiring once, got %d: %+v", n, changes)
	}
}

// Deleting a tracker cascades to its rows and change history.
func TestDeleteTrackerCascades(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTracker(t, s, "Apartments")
	if _, _, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1200"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "Apartments"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, "Apartments"); err == nil {
		t.Fatal("expected the tracker to be gone")
	}
	rows, err := s.Rows(ctx, tr.ID, "", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected rows to cascade-delete with the tracker, got %+v", rows)
	}
}

func TestListReportsRowCounts(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTracker(t, s, "Apartments")
	if _, _, err := s.UpsertRow(ctx, tr.ID, "k1", map[string]any{"Price": "1200"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertRow(ctx, tr.ID, "k2", map[string]any{"Price": "1300"}, ""); err != nil {
		t.Fatal(err)
	}
	ts, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].Rows != 2 {
		t.Fatalf("expected 1 tracker with 2 rows, got %+v", ts)
	}
}
