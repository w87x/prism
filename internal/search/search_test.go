package search

import (
	"context"
	"testing"

	"prism/internal/testutil"
)

// TestAllSearchesEveryCategoryAndExcludesNonMatches seeds one row of each kind (memory fact, tracker, kb
// page, task) sharing a distinctive token, plus a decoy row per table that must NOT match, and checks
// Search finds exactly the four matches and none of the decoys — a fan-out query like this can easily
// have one leg silently return nothing (wrong column, wrong table) without the others revealing it.
func TestAllSearchesEveryCategoryAndExcludesNonMatches(t *testing.T) {
	ctx := context.Background()
	d := testutil.DB(t)
	s := New(d.Pool)

	const token = "zephyrion"

	var bankID int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO memory_banks(kind,name) VALUES('user','user') ON CONFLICT (kind,name,owner) DO UPDATE SET name=EXCLUDED.name RETURNING id`).Scan(&bankID); err != nil {
		t.Fatal(err)
	}
	var factID int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO memory_facts(bank_id,text) VALUES($1,$2) RETURNING id`, bankID, "User's favourite project is called "+token).Scan(&factID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO memory_facts(bank_id,text) VALUES($1,'User likes plain tea')`, bankID); err != nil {
		t.Fatal(err)
	}

	var trackerID int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO trackers(name,description) VALUES($1,'a tracker') RETURNING id`, "Project "+token).Scan(&trackerID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO trackers(name,description) VALUES('Apartments','unrelated')`); err != nil {
		t.Fatal(err)
	}

	var kbID int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO kb_pages(title,query,body) VALUES($1,'q','notes about it') RETURNING id`, "Notes on "+token).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO kb_pages(title,query,body) VALUES('Unrelated page','q','nothing here')`); err != nil {
		t.Fatal(err)
	}

	var taskID int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO tasks(from_kind,to_agent,title,input) VALUES('user','Atlas',$1,'do it') RETURNING id`, "Look into "+token).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO tasks(from_kind,to_agent,title,input) VALUES('user','Atlas','Unrelated task','do it')`); err != nil {
		t.Fatal(err)
	}

	results, err := s.All(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, r := range results {
		got[r.Kind] = r.ID
	}
	if got["memory"] != factID {
		t.Errorf("memory result = %v, want fact %d", got["memory"], factID)
	}
	if got["tracker"] != trackerID {
		t.Errorf("tracker result = %v, want tracker %d", got["tracker"], trackerID)
	}
	if got["knowledge"] != kbID {
		t.Errorf("knowledge result = %v, want page %d", got["knowledge"], kbID)
	}
	if got["task"] != taskID {
		t.Errorf("task result = %v, want task %d", got["task"], taskID)
	}
	if len(results) != 4 {
		t.Fatalf("got %d results, want exactly 4 (decoys must not match): %+v", len(results), results)
	}

	if empty, err := s.All(ctx, "   "); err != nil || len(empty) != 0 {
		t.Fatalf("blank query should return no results, got %+v err=%v", empty, err)
	}
}
