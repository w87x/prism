package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"prism/internal/testutil"
)

// A model is written from what memory holds about its question, cites only facts it was shown, stays put
// until enough new facts arrive in its scope, and is then rewritten.
func TestMentalModelRefreshesWhenNewFactsArrive(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	var ids []int64
	for _, x := range []string{"User drinks green tea every morning", "User owns a ceramic tea pot", "User buys tea leaves from a Berlin shop"} {
		ids = append(ids, store(t, s, StoreReq{Bank: "user", Text: x}).ID)
	}
	calls := 0
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		for _, m := range req["messages"].([]any) {
			if c, _ := m.(map[string]any)["content"].(string); strings.Contains(c, "mental model") {
				calls++
				return testutil.Reply{Content: fmt.Sprintf(`{"answer":"User is a tea drinker (version %d).","sources":[%d,999999]}`, calls, ids[0])}
			}
		}
		return testutil.Reply{Content: `{"relations":[]}`}
	}
	if _, err := s.SaveModel(ctx, 0, "", "tea", nil); err == nil {
		t.Fatal("a model needs a name")
	}
	id, err := s.SaveModel(ctx, 0, "Tea habits", "What does the user do about tea?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveModel(ctx, 0, "Tea habits", "again", nil); err == nil {
		t.Fatal("duplicate names must be refused")
	}
	rs, err := s.ModelsDue(ctx, 0, 2)
	if err != nil || len(rs) != 1 || calls != 1 {
		t.Fatalf("first pass: %+v err=%v calls=%d", rs, err, calls)
	}
	if len(rs[0].Sources) != 1 || rs[0].Sources[0] != ids[0] {
		t.Fatalf("an invented source id must be dropped: %+v", rs[0].Sources)
	}
	if rs, _ := s.ModelsDue(ctx, 0, 2); len(rs) != 0 || calls != 1 {
		t.Fatalf("nothing new: must not refresh (calls=%d)", calls)
	}
	for i := 0; i < 3; i++ {
		store(t, s, StoreReq{Bank: "user", Text: fmt.Sprintf("Extra fact %d: the user enjoys jasmine tea blends number %d", i, i*7)})
	}
	ms, _ := s.Models(ctx)
	if ms[0].Fresh != 3 {
		t.Fatalf("fresh = %d", ms[0].Fresh)
	}
	if rs, err := s.ModelsDue(ctx, 0, 2); err != nil || len(rs) != 1 || calls != 2 || !strings.Contains(rs[0].Body, "version 2") {
		t.Fatalf("3 new facts must trigger a refresh: %+v err=%v calls=%d", rs, err, calls)
	}
	if _, err := s.SaveModel(ctx, id, "Tea habits", "A different question about tea", nil); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.Models(ctx); ms[0].Body != "" || ms[0].RefreshedAt != nil {
		t.Fatalf("a changed question must clear the old answer: %+v", ms[0])
	}
	if err := s.DeleteModel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.Models(ctx); len(ms) != 0 {
		t.Fatal("model not deleted")
	}
}
