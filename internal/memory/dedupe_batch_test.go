package memory

import (
	"context"
	"fmt"
	"prism/internal/testutil"
	"testing"
)

func TestClassifyRelationsBatchesAndRejectsAmbiguousIDs(t *testing.T) {
	s, fake := newSvc(t)
	fake.Handler = func(_ map[string]any, _ int) testutil.Reply {
		return testutil.Reply{Content: `{"relations":[{"id":1,"relation":"same"},{"id":2,"relation":"contradiction"},{"id":3,"relation":"same"},{"id":3,"relation":"unrelated"},{"id":999,"relation":"same"}]}`}
	}
	result, err := s.classifyRelations(context.Background(), "new fact", []cand{{id: 1, text: "first"}, {id: 2, text: "second"}, {id: 3, text: "third"}})
	if err != nil || result[1] != "same" || result[2] != "contradiction" || result[3] != "" || result[999] != "" {
		t.Fatalf("result=%v err=%v", result, err)
	}
	if len(fake.Recorded()) != 1 {
		t.Fatalf("expected one model call, got %d", len(fake.Recorded()))
	}
}

func TestDedupeFactsBatchesWithoutMergingContradictions(t *testing.T) {
	s, fake := newSvc(t)
	s.VectorOn = false
	ctx := context.Background()
	bank, err := s.BankBySpec(ctx, "user", "", true)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"User prefers green tea every morning at home", "User prefers green tea every morning at home daily", "User prefers green tea every morning at home today"}
	ids := make([]int64, 3)
	for i, text := range texts {
		if err := s.db.QueryRow(ctx, `INSERT INTO memory_facts(bank_id,text) VALUES($1,$2) RETURNING id`, bank.ID, text).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	fake.Handler = func(_ map[string]any, _ int) testutil.Reply {
		return testutil.Reply{Content: fmt.Sprintf(`{"relations":[{"id":%d,"relation":"same"},{"id":%d,"relation":"contradiction"}]}`, ids[0], ids[1])}
	}
	result, err := s.DedupeFacts(ctx, "user")
	if err != nil || result.Merged != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for i, id := range ids {
		var live bool
		if err := s.db.QueryRow(ctx, `SELECT valid_to IS NULL FROM memory_facts WHERE id=$1`, id).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if live != (i != 0) {
			t.Fatalf("fact %d live=%v", i, live)
		}
	}
	if len(fake.Recorded()) != 1 {
		t.Fatalf("expected a single batched comparison, got %d", len(fake.Recorded()))
	}
}
