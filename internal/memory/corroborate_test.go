package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"prism/internal/tools"
)

func TestCorroborationCurve(t *testing.T) {
	for n, want := range map[int]float64{0: 0, 1: 0.4, 2: 0.64, 3: 0.784} {
		if got := corroborated(n); got < want-0.001 || got > want+0.001 {
			t.Errorf("corroborated(%d) = %.3f want %.3f", n, got, want)
		}
	}
	if corroborated(50) > maxWebConfidence {
		t.Fatal("confidence must stay capped")
	}
}

func TestSameFactFromIndependentSitesGainsTrust(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	web := func(origin string) *StoreResult {
		r, err := s.Store(ctx, StoreReq{Bank: "domain:Market", Text: "ShopA sells milk for 1.20 euros", Tags: []string{"unverified"}, Source: "agent:Scout (tainted)", Confidence: 0.4, Origin: origin})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := web("shop-a.com")
	if first.Fact.Confidence > 0.41 || len(first.Fact.Origins) != 1 {
		t.Fatalf("first report: %+v", first.Fact)
	}
	if r := web("shop-a.com"); !r.Duplicate || r.Corroborated || r.Fact.Confidence > 0.41 {
		t.Fatalf("the same site again is not a second witness: %+v", r)
	}
	if r := web(""); r.Corroborated {
		t.Fatal("an unattributed report proves nothing")
	}
	second := web("price-watch.org")
	if !second.Corroborated || second.Fact.ID != first.Fact.ID || second.Fact.Confidence < 0.63 || len(second.Fact.Origins) != 2 {
		t.Fatalf("second site: %+v", second)
	}
	for _, tag := range second.Fact.Tags {
		if tag == "unverified" {
			t.Fatal("a fact over the line loses its unverified tag")
		}
	}
	if third := web("dairy.example.net"); third.Fact.Confidence < 0.78 {
		t.Fatalf("third site: %+v", third.Fact)
	}

	// a trusted confirmation (the user) lifts a web-learned fact at once
	web2, _ := s.Store(ctx, StoreReq{Bank: "domain:Market", Text: "ShopB closes at 9 pm", Tags: []string{"unverified"}, Confidence: 0.4, Origin: "shop-b.com"})
	conf, err := s.Store(ctx, StoreReq{Bank: "domain:Market", Text: "ShopB closes at 9 pm", Confidence: 0.95, Source: "user"})
	if err != nil || !conf.Corroborated || conf.Fact.ID != web2.Fact.ID || conf.Fact.Confidence < 0.94 {
		t.Fatalf("trusted confirmation: %+v %v", conf, err)
	}
	// and a trusted fact is not touched by web reports
	trusted, _ := s.Store(ctx, StoreReq{Bank: "user", Text: "User lives in Berlin", Confidence: 0.9})
	again, _ := s.Store(ctx, StoreReq{Bank: "user", Text: "User lives in Berlin", Confidence: 0.4, Origin: "spam.biz"})
	if again.Corroborated || again.Fact.ID != trusted.Fact.ID || len(again.Fact.Origins) != 0 || again.Fact.Confidence < 0.89 {
		t.Fatalf("trusted fact changed by a web report: %+v", again.Fact)
	}
}

// memory_store only accepts a source_url the agent really saw, and only matters in a tainted turn.
func TestMemoryStoreToolChecksTheSourceURL(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	reg := tools.NewRegistry(nil)
	RegisterTools(reg, s, func(context.Context, string) []string { return []string{"user"} })
	store, _ := reg.Get("memory_store")
	seen := &tools.Sources{}
	seen.Note("https://shop-a.com/milk", "https://price-watch.org/list")
	call := func(url string) (string, error) {
		args := map[string]any{"text": "Milk costs 1.20 at ShopA", "bank": "domain:Market"}
		if url != "" {
			args["source_url"] = url
		}
		b, _ := json.Marshal(args)
		return store.Run(ctx, &tools.Env{Agent: "Scout", Tainted: true, Sources: seen}, b)
	}
	if _, err := call("https://invented.example.com/x"); err == nil || !strings.Contains(err.Error(), "read in this task") {
		t.Fatalf("a source that was never seen must be refused: %v", err)
	}
	if out, err := call("https://www.shop-a.com/other"); err != nil || !strings.Contains(out, "Stored as #") {
		t.Fatalf("first: %q %v", out, err)
	}
	out, err := call("https://price-watch.org/list")
	if err != nil || !strings.Contains(out, "second source raised its confidence to 64%") {
		t.Fatalf("second: %q %v", out, err)
	}
	// the search result shows the earned trust
	find, _ := reg.Get("memory_find")
	b, _ := json.Marshal(map[string]any{"query": "milk ShopA", "banks": []string{"domain:Market"}})
	got, _ := find.Run(ctx, &tools.Env{Agent: "Scout"}, b)
	if !strings.Contains(got, "confirmed by 2 sites") || strings.Contains(got, "unverified") {
		t.Fatalf("find: %q", got)
	}
}
