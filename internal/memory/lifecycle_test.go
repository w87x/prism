package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Bug (found while comparing with the memo design): Find bumped hits/rank/last_used on every fact it returned, and
// the per-turn auto-recall returns the same facts every turn — so facts climbed to max rank merely by being
// retrieved. Now one reinforcement per scope.
func TestRetrievalReinforcesOncePerScope(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	r, err := s.Store(ctx, StoreReq{Bank: "user", Text: "The user keeps a Synology NAS called Vault", Source: "user"})
	if err != nil {
		t.Fatal(err)
	}
	find := func(scope string) {
		if _, err := s.Find(ctx, FindReq{Query: "Synology NAS", Banks: []string{"user"}, K: 3, NoLinks: true, Scope: scope}); err != nil {
			t.Fatal(err)
		}
	}
	hits := func() int { f, _ := s.GetFact(ctx, r.Fact.ID); return f.Hits }
	base := hits()
	find("task:1")
	find("task:1")
	find("task:1")
	if got := hits(); got != base+1 {
		t.Fatalf("three retrievals in one scope must reinforce once: hits %d → %d", base, got)
	}
	find("task:2")
	if got := hits(); got != base+2 {
		t.Fatalf("a new scope reinforces again: %d", got)
	}
	find("")
	find("")
	if got := hits(); got != base+4 {
		t.Fatalf("an unscoped retrieval keeps the old behaviour: %d", got)
	}
}

func TestExpiredFactsRetireThemselves(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	r, err := s.Store(ctx, StoreReq{Bank: "user", Text: "The GMKtec EVO-X2 costs $1,999 at the official store", Source: "user", TTLDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	keep, _ := s.Store(ctx, StoreReq{Bank: "user", Text: "The user owns a GMKtec mini PC", Source: "user"})
	q := FindReq{Query: "GMKtec", Banks: []string{"user"}, K: 5, NoLinks: true}
	if res, _ := s.Find(ctx, q); !hasID(res, r.Fact.ID) {
		t.Fatalf("an unexpired TTL fact must be findable")
	}
	if n := s.ExpireDue(ctx); n != 0 {
		t.Fatalf("nothing is due yet, retired %d", n)
	}
	if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET expires_at=now()-interval '1 hour' WHERE id=$1`, r.Fact.ID); err != nil {
		t.Fatal(err)
	}
	lastSweep.Store(time.Now().Add(-time.Hour).UnixNano()) // let Find's own throttled sweep run
	res, _ := s.Find(ctx, q)
	if hasID(res, r.Fact.ID) || !hasID(res, keep.Fact.ID) {
		t.Fatalf("expired fact must drop out of Find and the lasting one stay: %+v", res)
	}
	f, _ := s.GetFact(ctx, r.Fact.ID)
	if f.ValidTo == nil {
		t.Fatalf("expiry must retire the fact (valid_to), not delete it")
	}
}

func hasID(fs []Fact, id int64) bool {
	for _, f := range fs {
		if f.ID == id {
			return true
		}
	}
	return false
}

func TestMMRSelectPrefersDifferentFactsOverParaphrases(t *testing.T) {
	texts := []string{
		"The user prefers dark roast coffee in the morning",
		"The user likes dark roast coffee every morning",
		"The user drinks dark roast coffee in the morning hours",
		"The user's NAS is a Synology DS923 in the hallway",
	}
	var cands []cand
	var sc []scoredIdx
	for i, tx := range texts {
		cands = append(cands, cand{id: int64(i + 1), text: tx})
		sc = append(sc, scoredIdx{i, 1.0 - float64(i)*0.02})
	}
	got := mmrSelect(cands, sc, 2)
	if len(got) != 2 || got[0].i != 0 || got[1].i != 3 {
		t.Fatalf("want the best coffee fact plus the NAS fact, got %+v", got)
	}
	if all := mmrSelect(cands, sc, 4); len(all) != 4 {
		t.Fatalf("k >= pool must return everything")
	}
}

func TestClampTTL(t *testing.T) {
	for in, want := range map[int]int{-3: 0, 0: 0, 1: 1, 30: 30, 365: 365, 366: 0, 5000: 0} {
		if got := clampTTL(in); got != want {
			t.Errorf("clampTTL(%d)=%d want %d", in, got, want)
		}
	}
	if !strings.Contains(extractPrompt, "ttl_days") {
		t.Errorf("extraction prompt must ask for ttl_days")
	}
}
