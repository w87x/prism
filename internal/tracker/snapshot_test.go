package tracker

import (
	"context"
	"strings"
	"testing"
	"time"
)

func mkTyped(t *testing.T, s *Service) *Tracker {
	t.Helper()
	tr, err := s.Create(context.Background(), "Mini PCs", "shortlist", []Column{
		{Name: "Name", Type: TypeString, Required: true},
		{Name: "Price", Type: TypeNumber, Unit: "USD"},
		{Name: "InStock", Type: TypeBoolean},
		{Name: "Released", Type: TypeTime},
		{Name: "Tags", Type: TypeStrings},
		{Name: "Notes"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestTypedColumnsAreCoercedAndRefuseNonsense(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTyped(t, s)
	row, _, err := s.UpsertRow(ctx, tr.ID, "u1", map[string]any{"Name": "EVO-X2", "price": "$1,999", "InStock": "yes", "Released": "2026-05-01", "Tags": "mini, amd", "Anything": "free-form is fine"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if row.Data["Price"] != float64(1999) || row.Data["InStock"] != true || row.Data["Released"] != "2026-05-01" {
		t.Fatalf("coercion: %+v", row.Data)
	}
	if tags, ok := row.Data["Tags"].([]any); !ok || len(tags) != 2 {
		t.Fatalf("tags: %#v", row.Data["Tags"])
	}
	if row.Data["Anything"] != "free-form is fine" {
		t.Fatalf("undeclared fields stay free-form: %+v", row.Data)
	}
	if _, ok := row.Data["price"]; ok {
		t.Fatalf("a field must be stored under the declared column name: %+v", row.Data)
	}
	// every problem at once; a refused write stores nothing
	_, _, err = s.UpsertRow(ctx, tr.ID, "u2", map[string]any{"Name": "X", "Price": "cheap", "Released": "next spring", "InStock": "maybe"}, "")
	if err == nil || !strings.Contains(err.Error(), `"Price" expects a number`) || !strings.Contains(err.Error(), `"Released" expects`) || !strings.Contains(err.Error(), `"InStock" expects true or false`) {
		t.Fatalf("all problems must be listed: %v", err)
	}
	if _, err := s.rowByKey(ctx, tr.ID, "u2"); err == nil {
		t.Fatalf("a refused row must not exist")
	}
	if _, _, err := s.UpsertRow(ctx, tr.ID, "u3", map[string]any{"Price": 1}, ""); err == nil || !strings.Contains(err.Error(), `"Name" is required`) {
		t.Fatalf("required column on a new row: %v", err)
	}
	// but an UPDATE of an existing row need not repeat required columns
	if _, _, err := s.UpsertRow(ctx, tr.ID, "u1", map[string]any{"Price": "1899"}, ""); err != nil {
		t.Fatalf("update without required columns: %v", err)
	}
	if _, err := s.Create(ctx, "Bad", "", []Column{{Name: "A", Type: "integer"}}, "t"); err == nil {
		t.Fatalf("an unknown type must be refused")
	}
	if _, err := s.Create(ctx, "Dup", "", []Column{{Name: "A"}, {Name: "a"}}, "t"); err == nil {
		t.Fatalf("duplicate column names must be refused")
	}
}

func TestSnapshotCompleteAndPartialAndIdempotent(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTyped(t, s)
	row := func(k, name string, price any) Observed {
		return Observed{Key: k, Data: map[string]any{"Name": name, "Price": price}, SourceURL: "https://shop/" + k}
	}
	r1, err := s.Commit(ctx, tr.ID, "run-1", []Observed{row("a", "EVO-X2", 1999), row("b", "Framework Desktop", 2099), row("c", "Mac mini", 599)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Added != 3 || r1.Missing != 0 || r1.Replayed {
		t.Fatalf("first run: %+v", r1)
	}
	// replay: same run id + same rows applies nothing again
	again, err := s.Commit(ctx, tr.ID, "run-1", []Observed{row("c", "Mac mini", 599), row("a", "EVO-X2", 1999), row("b", "Framework Desktop", 2099)}, true)
	if err != nil || !again.Replayed || again.Added != 3 {
		t.Fatalf("a retried run must return its receipt: %+v %v", again, err)
	}
	// same run id, different rows: refused
	if _, err := s.Commit(ctx, tr.ID, "run-1", []Observed{row("a", "EVO-X2", 1)}, true); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("a reused run id with other rows must be refused: %v", err)
	}
	// a PARTIAL page that lacks "b" and "c" must not touch them
	r2, err := s.Commit(ctx, tr.ID, "run-2", []Observed{row("a", "EVO-X2", 1899)}, false)
	if err != nil || r2.Changed != 1 || r2.Missing != 0 {
		t.Fatalf("partial run: %+v %v", r2, err)
	}
	if b, _ := s.rowByKey(ctx, tr.ID, "b"); b.Status != "active" {
		t.Fatalf("a partial snapshot must leave absent rows alone: %+v", b)
	}
	// a COMPLETE snapshot without "c" marks it missing — and keeps its data
	r3, err := s.Commit(ctx, tr.ID, "run-3", []Observed{row("a", "EVO-X2", 1899), row("b", "Framework Desktop", 2099)}, true)
	if err != nil || r3.Missing != 1 || r3.Unchanged != 2 {
		t.Fatalf("complete run: %+v %v", r3, err)
	}
	c, _ := s.rowByKey(ctx, tr.ID, "c")
	if c.Status != "missing" || c.MissingSince == nil || c.Data["Price"] != float64(599) {
		t.Fatalf("missing, not deleted: %+v", c)
	}
	if live, _ := s.Rows(ctx, tr.ID, "", false, 50); len(live) != 2 {
		t.Fatalf("the live table excludes missing rows: %d", len(live))
	}
	// it comes back
	r4, err := s.Commit(ctx, tr.ID, "run-4", []Observed{row("a", "EVO-X2", 1899), row("b", "Framework Desktop", 2099), row("c", "Mac mini", 549)}, true)
	if err != nil || r4.Reappeared != 1 {
		t.Fatalf("reappearance: %+v %v", r4, err)
	}
	kinds := map[string]int{}
	ch, _ := s.Changes(ctx, tr.ID, time.Time{}, 100)
	for _, x := range ch {
		kinds[x.Kind]++
	}
	if kinds["added"] != 3 || kinds["missing"] != 1 || kinds["reappeared"] != 1 || kinds["changed"] < 2 {
		t.Fatalf("event kinds: %v", kinds)
	}
	// a bad row fails the whole run and applies nothing
	before, _ := s.Rows(ctx, tr.ID, "", true, 50)
	_, err = s.Commit(ctx, tr.ID, "run-5", []Observed{row("a", "EVO-X2", 1), {Key: "z", Data: map[string]any{"Name": "Z", "Price": "free"}}}, true)
	if err == nil || !strings.Contains(err.Error(), `row "z"`) {
		t.Fatalf("a bad row must name itself: %v", err)
	}
	after, _ := s.Rows(ctx, tr.ID, "", true, 50)
	if len(before) != len(after) {
		t.Fatalf("a failed run must apply nothing")
	}
	a, _ := s.rowByKey(ctx, tr.ID, "a")
	if a.Data["Price"] != float64(1899) {
		t.Fatalf("row a must be untouched by the failed run: %+v", a.Data)
	}
	if _, err := s.Commit(ctx, tr.ID, "", nil, true); err == nil {
		t.Fatalf("a run id is required")
	}
	if _, err := s.Commit(ctx, tr.ID, "dup", []Observed{row("a", "x", 1), row("a", "y", 2)}, false); err == nil {
		t.Fatalf("duplicate keys in one snapshot must be refused")
	}
	if runs, _ := s.Runs(ctx, tr.ID, 10); len(runs) != 4 {
		t.Fatalf("runs recorded: %d", len(runs))
	}
}

func TestAlertConditionsAreThreeValued(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	tr := mkTyped(t, s)
	var fired []Alert
	s.OnAlert = func(ctx context.Context, a Alert) { fired = append(fired, a) }

	if _, err := s.SetConditions(ctx, tr.ID, []Condition{{Name: "cheap", Field: "price", Op: "lte", Value: "2500", Notify: true}, {Name: "bad", Field: "Nope", Op: "lt", Value: 1}}); err == nil {
		t.Fatalf("an alert on an unknown column must be refused")
	}
	if _, err := s.SetConditions(ctx, tr.ID, []Condition{{Name: "x", Field: "Notes", Op: "lt", Value: 1}}); err == nil {
		t.Fatalf("ordering needs a number or date column")
	}
	if _, err := s.SetConditions(ctx, tr.ID, []Condition{{Name: "cheap", Field: "price", Op: "lte", Value: "$2,500", Notify: true}}); err != nil {
		t.Fatal(err)
	}
	put := func(key string, data map[string]any) {
		t.Helper()
		if _, _, err := s.UpsertRow(ctx, tr.ID, key, data, "https://shop/"+key); err != nil {
			t.Fatal(err)
		}
	}
	put("a", map[string]any{"Name": "EVO-X2", "Price": 2999})
	if len(fired) != 0 {
		t.Fatalf("2999 is not ≤ 2500")
	}
	put("a", map[string]any{"Price": 2399})
	if len(fired) != 1 || fired[0].Row.Key != "a" || !fired[0].Met {
		t.Fatalf("crossing the threshold must fire once: %+v", fired)
	}
	put("a", map[string]any{"Price": 2299}) // still true: no second alert
	if len(fired) != 1 {
		t.Fatalf("an alert fires on the transition, not on every write")
	}
	// a write that does not mention Price leaves it UNKNOWN-for-this-write: no clearing, no re-firing
	put("a", map[string]any{"InStock": true})
	if len(fired) != 1 {
		t.Fatalf("unrelated writes must not re-fire")
	}
	put("a", map[string]any{"Price": 2799}) // true → false
	put("a", map[string]any{"Price": 2100}) // false → true again
	if len(fired) != 2 {
		t.Fatalf("after clearing, meeting it again fires again: %d", len(fired))
	}
	// already satisfying when first seen: fires at once; a row with no price is unknown, never an alert
	put("b", map[string]any{"Name": "Mac mini", "Price": 599})
	put("c", map[string]any{"Name": "Mystery"})
	if len(fired) != 3 || fired[2].Row.Key != "b" {
		t.Fatalf("a row that already satisfies fires immediately, an unobserved one does not: %+v", fired)
	}
	ch, _ := s.Changes(ctx, tr.ID, time.Time{}, 100)
	met, cleared := 0, 0
	for _, c := range ch {
		switch c.Kind {
		case "condition_met":
			met++
		case "condition_cleared":
			cleared++
		}
	}
	if met != 3 || cleared != 1 {
		t.Fatalf("transitions are events: met=%d cleared=%d", met, cleared)
	}
	// a snapshot evaluates too, and a MISSING row is unknown (it neither fires nor clears)
	fired = nil
	if _, err := s.Commit(ctx, tr.ID, "r1", []Observed{{Key: "d", Data: map[string]any{"Name": "New box", "Price": 1500}}}, true); err != nil {
		t.Fatal(err)
	}
	if len(fired) != 1 || fired[0].Row.Key != "d" {
		t.Fatalf("snapshot rows are evaluated: %+v", fired)
	}
	if a, _ := s.rowByKey(ctx, tr.ID, "a"); a.Status != "missing" {
		t.Fatalf("a should be missing now")
	}
	// removing the alert clears its state
	if _, err := s.SetConditions(ctx, tr.ID, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM tracker_condition_state`).Scan(&n)
	if n != 0 {
		t.Fatalf("state of removed alerts must go: %d", n)
	}
}

func TestEvalConditionUnknownIsNotFalse(t *testing.T) {
	col := Column{Name: "Price", Type: TypeNumber}
	c := Condition{Name: "x", Field: "Price", Op: "lte", Value: 100.0}
	row := func(status string, d map[string]any) Row { return Row{Status: status, Data: d} }
	for name, tc := range map[string]struct {
		r    Row
		want string
	}{
		"met":         {row("active", map[string]any{"Price": 50.0}), resTrue},
		"not met":     {row("active", map[string]any{"Price": 150.0}), resFalse},
		"unobserved":  {row("active", map[string]any{"Name": "x"}), resUnknown},
		"empty":       {row("active", map[string]any{"Price": ""}), resUnknown},
		"not numeric": {row("active", map[string]any{"Price": "call us"}), resUnknown},
		"missing row": {row("missing", map[string]any{"Price": 50.0}), resUnknown},
		"retired row": {row("gone", map[string]any{"Price": 50.0}), resUnknown},
	} {
		if got := evalCondition(c, col, tc.r); got != tc.want {
			t.Errorf("%s: got %s want %s", name, got, tc.want)
		}
	}
	date := Column{Name: "Released", Type: TypeTime}
	soon := Condition{Name: "d", Field: "Released", Op: "lt", Value: "2026-12-01"}
	if got := evalCondition(soon, date, row("active", map[string]any{"Released": "2026-10-05"})); got != resTrue {
		t.Errorf("date comparison: %s", got)
	}
	has := Condition{Name: "t", Field: "Tags", Op: "contains", Value: "AMD"}
	if got := evalCondition(has, Column{Name: "Tags", Type: TypeStrings}, row("active", map[string]any{"Tags": []any{"mini", "amd"}})); got != resTrue {
		t.Errorf("list contains: %s", got)
	}
	ex := Condition{Name: "e", Field: "Price", Op: "exists"}
	if got := evalCondition(ex, col, row("active", map[string]any{})); got != resFalse {
		t.Errorf("exists on absent: %s", got)
	}
}
