package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"prism/internal/testutil"
)

func entityReply(entities, relations string) testutil.Reply {
	return testutil.Reply{Content: fmt.Sprintf(`{"entities":[%s],"relations":[%s]}`, entities, relations)}
}

// TestExtractEntitiesBuildsGraphAndIsIdempotent covers the whole entity-extraction path: two facts about
// the same product yield an entity with two mentions, a relation between two entities becomes a typed
// edge, a relation naming an entity the model never listed is dropped rather than crashing, and running
// extraction again on the same facts does not duplicate the entity or the edge.
func TestExtractEntitiesBuildsGraphAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)

	f1 := store(t, s, StoreReq{Bank: "user", Text: "User is excited about the AMD AI 395 chip for a new build"})
	f2 := store(t, s, StoreReq{Bank: "user", Text: "The AMD AI 395 releases in Q4 2026"})
	bank, err := s.BankBySpec(ctx, "user", "", false)
	if err != nil {
		t.Fatal(err)
	}

	fake.Handler = func(map[string]any, int) testutil.Reply {
		return entityReply(
			fmt.Sprintf(`{"name":"AMD AI 395","kind":"product","facts":[%d,%d]}`, f1.ID, f2.ID)+
				`,{"name":"Q4 2026","kind":"event","facts":[`+fmt.Sprint(f2.ID)+`]}`,
			`{"a":"AMD AI 395","b":"Q4 2026","kind":"temporal","label":"releases in","facts":[`+fmt.Sprint(f2.ID)+`]}`+
				`,{"a":"AMD AI 395","b":"Ghost Entity","kind":"related","label":"bogus","facts":[]}`,
		)
	}
	res, err := s.ExtractEntities(ctx, bank.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entities != 2 || res.Relations != 1 {
		t.Fatalf("first pass: %+v", res)
	}

	g, err := s.EntityGraph(ctx, bank.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 2 {
		t.Fatalf("nodes = %+v", g.Nodes)
	}
	var amdID int64
	for _, n := range g.Nodes {
		if n.Name == "AMD AI 395" {
			amdID = n.ID
			if n.Kind != "product" || n.Mentions != 2 {
				t.Fatalf("amd node = %+v, want kind=product mentions=2", n)
			}
		}
	}
	if amdID == 0 {
		t.Fatal("AMD AI 395 entity not found")
	}
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %+v, want exactly 1 (the bogus relation must be dropped)", g.Edges)
	}
	if g.Edges[0].Kind != "temporal" || g.Edges[0].Label != "releases in" {
		t.Fatalf("edge = %+v", g.Edges[0])
	}

	facts, err := s.EntityFacts(ctx, amdID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("entity facts = %+v, want 2", facts)
	}

	// re-running on the same (already-processed) facts without force should skip: nothing new since the watermark
	fake.Handler = func(map[string]any, int) testutil.Reply {
		t.Fatal("model called again with no new facts and force=false")
		return testutil.Reply{}
	}
	res2, err := s.ExtractEntities(ctx, bank.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Skipped == "" {
		t.Fatalf("expected a skip on the second pass, got %+v", res2)
	}

	// forcing a re-pass over the same facts must not duplicate the entity or the edge
	fake.Handler = func(map[string]any, int) testutil.Reply {
		return entityReply(
			fmt.Sprintf(`{"name":"AMD AI 395","kind":"product","facts":[%d,%d]}`, f1.ID, f2.ID)+
				`,{"name":"Q4 2026","kind":"event","facts":[`+fmt.Sprint(f2.ID)+`]}`,
			`{"a":"AMD AI 395","b":"Q4 2026","kind":"temporal","label":"releases in","facts":[`+fmt.Sprint(f2.ID)+`]}`,
		)
	}
	if _, err := s.ExtractEntities(ctx, bank.ID, true); err != nil {
		t.Fatal(err)
	}
	g2, err := s.EntityGraph(ctx, bank.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(g2.Nodes) != 2 || len(g2.Edges) != 1 {
		t.Fatalf("re-extraction duplicated nodes/edges: %+v / %+v", g2.Nodes, g2.Edges)
	}
}

// Entity extraction runs on its own once a bank has gathered enough new facts, and not before — and the
// watermark keeps it from re-running until another batch arrives.
func TestEntitiesDueWaitsForEnoughNewFacts(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	calls := 0
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		for _, m := range req["messages"].([]any) { // storing a fact also asks the model about relations: count only extraction
			if c, _ := m.(map[string]any)["content"].(string); strings.Contains(c, "You extract entities and relations") {
				calls++
			}
		}
		return entityReply("", "")
	}
	for i := 0; i < 3; i++ {
		store(t, s, StoreReq{Bank: "user", Text: fmt.Sprintf("Distinct fact number %d about tea and coffee brewing methods", i)})
	}
	if rs, err := s.EntitiesDue(ctx, 4, 2); err != nil || len(rs) != 0 || calls != 0 {
		t.Fatalf("3 facts is below the threshold of 4: %+v err=%v calls=%d", rs, err, calls)
	}
	store(t, s, StoreReq{Bank: "user", Text: "Another unrelated fact about mountain hiking trails in Norway"})
	rs, err := s.EntitiesDue(ctx, 4, 2)
	if err != nil || len(rs) != 1 || calls != 1 {
		t.Fatalf("4 facts must trigger one pass: %+v err=%v calls=%d", rs, err, calls)
	}
	if rs, _ := s.EntitiesDue(ctx, 4, 2); len(rs) != 0 || calls != 1 {
		t.Fatalf("the watermark must stop an immediate re-run: %+v calls=%d", rs, calls)
	}
}
