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
