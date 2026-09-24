package memory

import (
	"context"
	"testing"
)

// TestFullGraphMergesFactsEntitiesAndMentions checks the three edge kinds a merged graph needs to be
// useful: a fact-link between two facts, an entity-relation between two entities, and a "mentions" edge
// connecting an entity back to the fact it was extracted from — plus that node ids are unambiguous even
// though fact ids and entity ids come from separate sequences and can numerically collide.
func TestFullGraphMergesFactsEntitiesAndMentions(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)

	f1 := store(t, s, StoreReq{Bank: "user", Text: "User is excited about the AMD AI 395 chip"})
	f2 := store(t, s, StoreReq{Bank: "user", Text: "The AMD AI 395 releases in Q4 2026"})
	if err := s.Link(ctx, f1.ID, f2.ID, LinkRelated, "", "agent", 0.7); err != nil {
		t.Fatal(err)
	}

	e1, _, err := s.upsertEntity(ctx, f1.BankID, "AMD AI 395", "product", 2)
	if err != nil {
		t.Fatal(err)
	}
	e2, _, err := s.upsertEntity(ctx, f1.BankID, "Q4 2026", "event", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkEntities(ctx, e1, e2, "temporal", "releases in", "auto", 0.6); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO memory_entity_mentions(entity_id,fact_id) VALUES($1,$2),($1,$3)`, e1, f1.ID, f2.ID); err != nil {
		t.Fatal(err)
	}

	fg, err := s.FullGraph(ctx, f1.BankID, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fg.Nodes) != 4 {
		t.Fatalf("nodes = %+v, want 4 (2 facts + 2 entities)", fg.Nodes)
	}
	byID := map[string]FullNode{}
	for _, n := range fg.Nodes {
		byID[n.ID] = n
	}
	if n, ok := byID[factNodeID(f1.ID)]; !ok || n.Type != "fact" || n.Text != f1.Text {
		t.Fatalf("fact node missing/wrong: %+v", n)
	}
	if n, ok := byID[entityNodeID(e1)]; !ok || n.Type != "entity" || n.Text != "AMD AI 395" || n.Kind != "product" {
		t.Fatalf("entity node missing/wrong: %+v", n)
	}

	var factLinks, entityLinks, mentions int
	for _, e := range fg.Edges {
		switch e.Kind {
		case "related":
			factLinks++
			if e.A != factNodeID(f1.ID) && e.A != factNodeID(f2.ID) {
				t.Fatalf("fact-link edge has wrong endpoint: %+v", e)
			}
		case "temporal":
			entityLinks++
			if e.Label != "releases in" {
				t.Fatalf("entity-link edge missing its label: %+v", e)
			}
		case "mentions":
			mentions++
			if e.A != entityNodeID(e1) {
				t.Fatalf("mentions edge should originate from the entity: %+v", e)
			}
		}
	}
	if factLinks != 1 || entityLinks != 1 || mentions != 2 {
		t.Fatalf("edge kind counts: fact=%d entity=%d mentions=%d, want 1/1/2 — edges=%+v", factLinks, entityLinks, mentions, fg.Edges)
	}

	// a fact id and an entity id can be numerically equal without ever colliding, because ids are prefixed
	if factNodeID(1) == entityNodeID(1) {
		t.Fatal("prefixed ids must not collide")
	}
}
