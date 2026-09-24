package memory

import (
	"context"
	"fmt"
	"testing"

	"prism/internal/testutil"
)

func TestFuseRanksRewardsAgreementBetweenSignals(t *testing.T) {
	// fact 0: best semantically, no keyword match; fact 1: second on both; fact 2: best on keywords only; 3 below the bar
	rel := []float64{0.9, 0.7, 0.68, 0.1}
	sem := []float64{0.9, 0.7, 0.4, 0.1}
	lex := map[int]float64{1: 1.0, 2: 0.8}
	f := fuseRanks(rel, sem, lex, 0.28)
	if !(f[1] > f[0] && f[1] > f[2]) {
		t.Fatalf("the fact both signals rank well must lead: %v", f)
	}
	if f[1] != 1 || f[3] != 0 {
		t.Fatalf("normalised to the best, and facts under the bar get nothing: %v", f)
	}
	if got := fuseRanks(nil, nil, nil, 0.3); len(got) != 0 {
		t.Fatal("empty input")
	}
}

func TestDeepFindLetsTheModelReorder(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	a := store(t, s, StoreReq{Bank: "domain:Pets", Text: "The cat sleeps on the sofa in the living room"})
	b := store(t, s, StoreReq{Bank: "domain:Pets", Text: "The cat food is bought at the market on Saturday"})
	c := store(t, s, StoreReq{Bank: "domain:Pets", Text: "The cat sofa scratching problem was solved with a post"})
	plain, err := s.Find(ctx, FindReq{Query: "cat sofa", Banks: []string{"domain:Pets"}, NoLinks: true, MinRel: 0.01})
	if err != nil || len(plain) < 3 {
		t.Fatalf("plain: %v %v", plain, err)
	}
	asked := 0
	fake.Handler = func(req map[string]any, n int) testutil.Reply {
		asked++
		// the model prefers the fact about the scratching problem and drops the rest of its list
		return testutil.Reply{Content: fmt.Sprintf(`{"order":[%d, 999999, %d]}`, c.ID, c.ID)}
	}
	deep, err := s.Find(ctx, FindReq{Query: "how was the scratching solved", Banks: []string{"domain:Pets"}, NoLinks: true, Deep: true, MinRel: 0.01})
	if err != nil || asked == 0 || len(deep) < 3 || deep[0].ID != c.ID {
		t.Fatalf("deep: %v asked=%d err=%v", deep, asked, err)
	}
	seen := map[int64]bool{}
	for _, f := range deep {
		if seen[f.ID] {
			t.Fatalf("a fact must not appear twice: %v", deep)
		}
		seen[f.ID] = true
	}
	if !seen[a.ID] || !seen[b.ID] {
		t.Fatalf("facts the model did not mention keep their place after the chosen ones: %v", deep)
	}
	// a broken answer leaves the ordinary order alone
	fake.Handler = func(map[string]any, int) testutil.Reply { return testutil.Reply{Content: "no idea"} }
	again, err := s.Find(ctx, FindReq{Query: "cat sofa", Banks: []string{"domain:Pets"}, NoLinks: true, Deep: true, MinRel: 0.01})
	if err != nil || len(again) != len(plain) || again[0].ID != plain[0].ID {
		t.Fatalf("fallback: %v vs %v (%v)", again, plain, err)
	}
}
