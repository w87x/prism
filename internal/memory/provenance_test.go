package memory

import (
	"context"
	"testing"
	"time"
)

// Bug: editing a fact's text overwrote it in place — no history, and any conclusion resting on the old
// wording never learned it had changed (factCols' "stale" flag is computed off valid_to, which a plain
// UPDATE never touched). Correcting a fact must go through the same supersede path Store() already uses.
func TestUpdateFactTextChangeSupersedesPreservingHistory(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	old := store(t, s, StoreReq{Bank: "user", Text: "User lives in Berlin", Tags: []string{"home"}})

	newText := "User lives in Munich"
	got, err := s.UpdateFact(ctx, old.ID, &newText, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == old.ID {
		t.Fatal("expected a new fact row, not an in-place text overwrite")
	}
	if got.Text != newText || got.Supersedes == nil || *got.Supersedes != old.ID {
		t.Fatalf("new fact: %+v", got)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "home" {
		t.Fatalf("expected tags carried over when not explicitly changed: %+v", got.Tags)
	}

	oldNow, err := s.GetFact(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldNow.ValidTo == nil || oldNow.SupersededBy == nil || *oldNow.SupersededBy != got.ID {
		t.Fatalf("old fact must be retired and point to its replacement: %+v", oldNow)
	}
	if oldNow.Text != "User lives in Berlin" {
		t.Fatal("the old text must still be readable — that is the whole point of preserving history")
	}

	// with history off (the default view), only the current fact should turn up
	facts, err := s.Facts(ctx, old.BankID, "", false, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawOld, sawNew bool
	for _, f := range facts {
		if f.ID == old.ID {
			sawOld = true
		}
		if f.ID == got.ID {
			sawNew = true
		}
	}
	if sawOld || !sawNew {
		t.Fatalf("non-history view: sawOld=%v sawNew=%v", sawOld, sawNew)
	}
}

// Changing only tags/rank is metadata, not a correction to the claim itself — it must not fork a new row.
func TestUpdateFactMetadataOnlyStaysInPlace(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	f := store(t, s, StoreReq{Bank: "user", Text: "User prefers dark roast coffee"})
	newRank := 2.5
	got, err := s.UpdateFact(ctx, f.ID, nil, []string{"coffee", "preference"}, &newRank)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != f.ID {
		t.Fatal("a tags/rank-only edit must not create a new fact")
	}
	if got.Text != f.Text || got.Rank != newRank || len(got.Tags) != 3 { // the two given tags plus user-rank
		t.Fatalf("updated fact: %+v", got)
	}
}

// The reviewer's exact ask: editing a fact must "trigger review of conclusions that depend on it". Since
// staleness is a live computation off valid_to (see factCols), superseding the fact is enough — no extra
// step needs to run. This proves the cascade actually happens, not just that UpdateFact returns a new row.
func TestCorrectingAFactMarksDependentConclusionStale(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	a := store(t, s, StoreReq{Bank: "user", Text: "User works at Acme Corp"})
	b := store(t, s, StoreReq{Bank: "user", Text: "User commutes to the Acme Corp office three days a week"})
	bank, err := s.BankBySpec(ctx, "user", "", false)
	if err != nil {
		t.Fatal(err)
	}
	concID, err := s.insertConclusion(ctx, bank.ID, "User works an in-office/remote hybrid schedule at Acme Corp", []int64{a.ID, b.ID}, 0.8, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetFact(ctx, concID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Stale {
		t.Fatal("a freshly drawn conclusion must not start stale")
	}

	newText := "User works at Initech now"
	if _, err := s.UpdateFact(ctx, a.ID, &newText, nil, nil); err != nil {
		t.Fatal(err)
	}

	after, err := s.GetFact(ctx, concID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Stale {
		t.Fatal("correcting a fact the conclusion cites as evidence must mark that conclusion stale")
	}
}

// A pinned fact is the user's own standing say that it matters: Prune must never auto-archive it, however
// low its rank or however long it's gone unused.
func TestPinnedFactSurvivesPrune(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	pinned := store(t, s, StoreReq{Bank: "user", Text: "User is severely allergic to peanuts"})
	unpinned := store(t, s, StoreReq{Bank: "user", Text: "User once mentioned liking a particular brand of pen"})
	if err := s.SetPinned(ctx, pinned.ID, true); err != nil {
		t.Fatal(err)
	}
	// backdate both past Prune's 90-day/low-rank window
	if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET rank=0.1, created_at=now()-interval '200 days' WHERE id=ANY($1)`, []int64{pinned.ID, unpinned.ID}); err != nil {
		t.Fatal(err)
	}
	archived, _, err := s.Prune(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if archived != 1 {
		t.Fatalf("expected exactly the unpinned fact archived, got %d", archived)
	}
	p, err := s.GetFact(ctx, pinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.ValidTo != nil {
		t.Fatal("a pinned fact must never be auto-archived by Prune")
	}
	u, err := s.GetFact(ctx, unpinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.ValidTo == nil {
		t.Fatal("the unpinned fact should have been archived as usual")
	}
}

// A pinned fact's retrieval weight must not decay with time the way an ordinary unused fact's does.
func TestPinnedFactSkipsRankDecay(t *testing.T) {
	old := time.Now().Add(-365 * 24 * time.Hour)
	decayed := effRank(1.0, nil, old, false)
	pinned := effRank(1.0, nil, old, true)
	if decayed >= 0.9 {
		t.Fatalf("sanity: a year-old unused fact should have decayed noticeably, got %v", decayed)
	}
	if pinned != 1.0 {
		t.Fatalf("a pinned fact must skip decay entirely, got %v (want 1.0)", pinned)
	}
}

// "Mark outdated" retires a fact with no replacement — distinct from correcting it (which supersedes to a
// new fact). It must stay inspectable with history, and must not silently succeed on an already-retired one.
func TestMarkOutdatedRetiresWithNoReplacement(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	f := store(t, s, StoreReq{Bank: "user", Text: "User's temporary contract runs through this quarter"})
	if err := s.MarkOutdated(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFact(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ValidTo == nil {
		t.Fatal("expected the fact retired")
	}
	if got.SupersededBy != nil {
		t.Fatal("mark-outdated must not point to a replacement — there isn't one")
	}
	if err := s.MarkOutdated(ctx, f.ID); err == nil {
		t.Fatal("marking an already-retired fact outdated again must error, not silently succeed")
	}
}

// A fact stored with a task id records it, so "why do you remember this" can point back to the actual
// conversation — not just a generic source label.
func TestStoreRecordsTaskProvenance(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	f, err := s.Store(ctx, StoreReq{Bank: "user", Text: "User's flight lands at 6pm on Friday", TaskID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if f.Fact.TaskID != 42 {
		t.Fatalf("expected task_id 42 recorded, got %d", f.Fact.TaskID)
	}
	withoutTask := store(t, s, StoreReq{Bank: "user", Text: "User likes their coffee black"})
	if withoutTask.TaskID != 0 {
		t.Fatalf("expected no task id when none was given, got %d", withoutTask.TaskID)
	}
}
