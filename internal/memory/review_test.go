package memory

import (
	"encoding/json"
	"strings"

	"context"
	"prism/internal/tools"
	"testing"
	"time"
)

// TestReviewSurfacesContradictionsStaleConclusionsAndPruneCandidates exercises all three review
// categories together, since they share one aggregation query set (see review.go) and a bug in one
// could easily start silently swallowing the others.
func TestReviewSurfacesContradictionsStaleConclusionsAndPruneCandidates(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)

	// 1. an unresolved contradiction between two active facts
	a := store(t, s, StoreReq{Bank: "user", Text: "User is vegetarian"})
	b := store(t, s, StoreReq{Bank: "user", Text: "User eats steak every Friday"})
	if err := s.Link(ctx, a.ID, b.ID, LinkContradicts, "diet claims disagree", "agent", 0.8); err != nil {
		t.Fatal(err)
	}

	// 2. a conclusion whose evidence has since been retired — Stale
	e1 := store(t, s, StoreReq{Bank: "user", Text: "User asked for shorter replies on Monday"})
	e2 := store(t, s, StoreReq{Bank: "user", Text: "User asked for shorter replies on Tuesday"})
	concID, err := s.insertConclusion(ctx, e1.BankID, "User consistently prefers shorter replies", []int64{e1.ID, e2.ID}, 0.8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now() WHERE id=$1`, e1.ID); err != nil {
		t.Fatal(err)
	}

	// 3. a fact that is about to be auto-pruned: low rank, unused for a long time, unpinned
	p := store(t, s, StoreReq{Bank: "user", Text: "User once mentioned liking a particular brand of pen"})
	old := time.Now().Add(-200 * 24 * time.Hour)
	if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET rank=0.1, last_used=$2, created_at=$2 WHERE id=$1`, p.ID, old); err != nil {
		t.Fatal(err)
	}

	// a fact that looks similar to the prune candidate but is pinned must NOT show up
	pinned := store(t, s, StoreReq{Bank: "user", Text: "User's emergency contact is their sister"})
	if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET rank=0.1, last_used=$2, created_at=$2, pinned=true WHERE id=$1`, pinned.ID, old); err != nil {
		t.Fatal(err)
	}

	rev, err := s.Review(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(rev.Contradictions) != 1 {
		t.Fatalf("contradictions = %d, want 1: %+v", len(rev.Contradictions), rev.Contradictions)
	}
	c := rev.Contradictions[0]
	if !(c.A.ID == a.ID && c.B.ID == b.ID) && !(c.A.ID == b.ID && c.B.ID == a.ID) {
		t.Fatalf("contradiction does not reference the linked facts: %+v", c)
	}
	if c.Note != "diet claims disagree" {
		t.Fatalf("contradiction note = %q", c.Note)
	}

	if len(rev.StaleConclusions) != 1 || rev.StaleConclusions[0].ID != concID {
		t.Fatalf("stale conclusions = %+v, want [%d]", rev.StaleConclusions, concID)
	}
	if !rev.StaleConclusions[0].Stale {
		t.Fatal("returned conclusion not marked Stale")
	}

	var pruneIDs []int64
	for _, f := range rev.PruneCandidates {
		pruneIDs = append(pruneIDs, f.ID)
		if f.ID == pinned.ID {
			t.Fatal("a pinned fact must never show up as a prune candidate")
		}
	}
	found := false
	for _, id := range pruneIDs {
		if id == p.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("prune candidate %d missing from %v", p.ID, pruneIDs)
	}
}

// A maintainer listing its own not-yet-created profile bank must get an explanation, not an error: banks
// are created on first store, so "profile" legitimately doesn't exist for a fresh agent.
func TestMemoryListToolMissingBankIsNotAnError(t *testing.T) {
	s, _ := newSvc(t)
	store(t, s, StoreReq{Bank: "user", Text: "User likes tea"})
	reg := tools.NewRegistry(nil)
	RegisterTools(reg, s, func(context.Context, string) []string { return []string{"user"} })
	list, _ := reg.Get("memory_list")
	out, err := list.Run(context.Background(), &tools.Env{Agent: "Metis"}, json.RawMessage(`{"bank":"profile"}`))
	if err != nil || !strings.Contains(out, "no facts yet") || !strings.Contains(out, "user") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// memory_reclassify is the only tool that can move a fact into or out of user/profile banks (merge/split are
// restricted to project/domain) — the gap that let a misfiled fact (e.g. a third party's details stored in
// "user") sit there with nothing able to fix it short of hand-editing the database.
func TestMemoryReclassifyToolMovesAcrossAnyBankKind(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	f := store(t, s, StoreReq{Bank: "user", Text: "Alexander is a writer who blogs on author.today", Agent: "Atlas"})
	reg := tools.NewRegistry(nil)
	RegisterTools(reg, s, func(context.Context, string) []string { return []string{"user"} })
	reclassify, ok := reg.Get("memory_reclassify")
	if !ok {
		t.Fatal("memory_reclassify not registered")
	}

	// moving into a not-yet-existing domain bank creates it, exactly like memory_store would
	out, err := reclassify.Run(ctx, &tools.Env{Agent: "Mnemosyne"}, json.RawMessage(`{"id":`+itoa(f.ID)+`,"bank":"domain:Alexander","reason":"about a third party, not the user"}`))
	if err != nil || !strings.Contains(out, "domain:Alexander") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	moved, err := s.GetFact(ctx, f.ID)
	if err != nil || moved.Bank != "domain:Alexander" {
		t.Fatalf("fact did not move: %+v err=%v", moved, err)
	}

	// re-running with the same destination is a clear error, not a silent no-op
	if _, err := reclassify.Run(ctx, &tools.Env{Agent: "Mnemosyne"}, json.RawMessage(`{"id":`+itoa(f.ID)+`,"bank":"domain:Alexander"}`)); err == nil {
		t.Fatal("moving a fact to the bank it is already in should error")
	}

	// a bad fact id fails clearly instead of silently doing nothing
	if _, err := reclassify.Run(ctx, &tools.Env{Agent: "Mnemosyne"}, json.RawMessage(`{"id":999999,"bank":"user"}`)); err == nil {
		t.Fatal("a nonexistent fact id should error")
	}
}
