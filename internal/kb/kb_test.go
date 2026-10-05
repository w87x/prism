package kb

import (
	"context"
	"strings"
	"testing"

	"prism/internal/memory"
	"prism/internal/settings"
	"prism/internal/testutil"
)

func TestGenerateChangesAndSmartFolder(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fake)
	mem := memory.New(d.Pool, r, st)
	mem.VectorOn = d.VectorOn
	s := &Service{DB: d.Pool, Memory: mem, LLM: r, Settings: st}
	ctx := context.Background()
	for _, f := range []string{"User cooks pasta carbonara without cream", "User loves spicy ramen with soft eggs", "User dislikes cilantro in food"} {
		if _, err := mem.Store(ctx, memory.StoreReq{Bank: "domain:Cooking", Text: f}); err != nil {
			t.Fatal(err)
		}
	}
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		user, _ := ms[len(ms)-1].(map[string]any)["content"].(string)
		switch {
		case strings.Contains(sys, "personal knowledge base in Markdown"):
			if !strings.Contains(user, "#") || !strings.Contains(user, "carbonara") {
				return testutil.Reply{Content: "facts missing from prompt"}
			}
			return testutil.Reply{Content: "## Food\nHe cooks carbonara [#1].\n\n## Open questions\n- desserts"}
		case strings.Contains(sys, "organise a personal knowledge-base folder"):
			return testutil.Reply{Content: `{"pages":[{"title":"Noodles","query":"ramen and pasta preferences"},{"title":"Dislikes","query":"foods the user avoids"}]}`}
		}
		return testutil.Reply{Content: "?"}
	}
	pid, err := s.SavePage(ctx, Page{Title: "Food preferences", Query: "what does the user like to eat", Auto: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Generate(ctx, pid); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetPage(ctx, pid)
	if p.Status != "ready" || !strings.Contains(p.Body, "carbonara") || len(p.Sources) == 0 || p.GeneratedAt == nil {
		t.Fatalf("page: %+v", p)
	}
	if n := s.changes(ctx, p); n != 0 {
		t.Fatalf("nothing changed yet, got %d", n)
	}
	// new relevant facts arrive → changes counted; the sweep respects the configured threshold
	_, _ = mem.Store(ctx, memory.StoreReq{Bank: "domain:Cooking", Text: "What the user likes to eat: eggs for breakfast"})
	_, _ = mem.Store(ctx, memory.StoreReq{Bank: "domain:Cooking", Text: "What the user likes to eat: sushi on Fridays"})
	p, _ = s.GetPage(ctx, pid)
	if n := s.changes(ctx, p); n < 2 {
		t.Fatalf("expected ≥2 relevant changes, got %d", n)
	}
	_ = st.Set(ctx, "kb", Config{Enabled: true, RegenEveryH: 1, MinChanges: 99})
	_, _ = d.Exec(ctx, `UPDATE kb_pages SET generated_at = now() - interval '5 hours'`)
	s.sweep(ctx)
	if q, _ := s.GetPage(ctx, pid); q.GeneratedAt.After(*p.GeneratedAt) {
		t.Fatal("below the change threshold the page must not be regenerated")
	}
	_ = st.Set(ctx, "kb", Config{Enabled: true, RegenEveryH: 1, MinChanges: 2})
	s.sweep(ctx)
	if q, _ := s.GetPage(ctx, pid); !q.GeneratedAt.After(*p.GeneratedAt) {
		t.Fatal("above the threshold and older than the interval the page must be regenerated")
	}

	// smart folder proposes pages once, never duplicates
	fid, _ := s.SaveFolder(ctx, Folder{Name: "Kitchen", Query: "everything about food", Smart: true, MaxPages: 4})
	created, err := s.Expand(ctx, fid)
	if err != nil || len(created) != 2 {
		t.Fatalf("expand: %v %+v", err, created)
	}
	if again, _ := s.Expand(ctx, fid); len(again) != 0 {
		t.Fatalf("second expansion must not duplicate pages: %+v", again)
	}
	tree, _ := s.Tree(ctx)
	if len(tree["pages"].([]Page)) != 3 {
		t.Fatalf("tree: %+v", tree)
	}
	// deleting a folder removes its pages
	_ = s.DeleteFolder(ctx, fid)
	tree, _ = s.Tree(ctx)
	if len(tree["pages"].([]Page)) != 1 {
		t.Fatalf("folder delete should drop its pages: %+v", tree["pages"])
	}
	_ = settings.KeyMemory
}

func TestDropInventedLinks(t *testing.T) {
	facts := "#1 Go 1.27.1 verified at https://go.dev/VERSION.\n"
	body := "1. Siri — https://example.com/iphone-siri\n2. Go — https://go.dev/VERSION."
	out, n := dropInventedLinks(body, facts)
	if n != 1 {
		t.Fatalf("removed %d links, want 1", n)
	}
	if strings.Contains(out, "example.com") {
		t.Fatalf("invented link kept:\n%s", out)
	}
	if !strings.Contains(out, "https://go.dev/VERSION.") || !strings.Contains(out, "(link not in memory)") {
		t.Fatalf("supported link lost or marker missing:\n%s", out)
	}
	if clean, n := dropInventedLinks("see https://go.dev/VERSION", facts); n != 0 || clean != "see https://go.dev/VERSION" {
		t.Fatalf("a supported link was touched: %q (%d)", clean, n)
	}
}

// A page rests on specific facts. When one is retired — or lapses — the page is stale: it is flagged (never
// served as current), rebuilt promptly, and a rebuild re-reads its sources so it does not land stale.
func TestPageGoesStaleWhenASourceFactIsRetired(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fake)
	mem := memory.New(d.Pool, r, st)
	mem.VectorOn = d.VectorOn
	s := &Service{DB: d.Pool, Memory: mem, LLM: r, Settings: st}
	ctx := context.Background()
	var ids []int64
	for _, f := range []string{"The GMKtec EVO-X2 has 128GB of memory", "The GMKtec EVO-X2 costs $1,999 right now"} {
		res, err := mem.Store(ctx, memory.StoreReq{Bank: "domain:Hardware", Text: f, Source: "user"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.Fact.ID)
	}
	var writes int
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		user, _ := ms[len(ms)-1].(map[string]any)["content"].(string)
		writes++
		return testutil.Reply{Content: "## GMKtec\n" + user}
	}
	pid, _ := s.SavePage(ctx, Page{Title: "GMKtec EVO-X2", Query: "GMKtec EVO-X2", Auto: true})
	if err := s.Generate(ctx, pid); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetPage(ctx, pid)
	if p.Stale || len(p.Dropped) != 0 {
		t.Fatalf("a freshly built page is not stale: %+v", p)
	}
	if err := mem.Retract(ctx, "user", ids[1], "the price changed"); err != nil {
		t.Fatal(err)
	}
	p, _ = s.GetPage(ctx, pid)
	if !p.Stale || len(p.Dropped) != 1 || p.Dropped[0] != ids[1] {
		t.Fatalf("retiring a source must make the page stale and name it: %+v", p)
	}
	tree, _ := s.Tree(ctx)
	for _, pg := range tree["pages"].([]Page) {
		if pg.ID == pid && !pg.Stale {
			t.Fatalf("the tree must report the stale page too")
		}
	}
	// the facts the writer sees carry how far to trust them
	if got := factLines([]memory.Fact{{ID: 1, Text: "x", Bank: "b", Confidence: 0.3}}); !strings.Contains(got, "unverified") {
		t.Fatalf("writer lines must carry the trust notes: %q", got)
	}
	// rebuilt from what is true now, the page is fresh again
	if err := s.Generate(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if p, _ = s.GetPage(ctx, pid); p.Stale {
		t.Fatalf("a rebuilt page must be fresh: %+v", p)
	}
	// a volatile source that lapses makes a page stale too
	res, _ := mem.Store(ctx, memory.StoreReq{Bank: "domain:Hardware", Text: "The GMKtec EVO-X2 ships within 3 days", Source: "user", TTLDays: 3})
	_, _ = d.Pool.Exec(ctx, `UPDATE kb_pages SET sources=sources||$2 WHERE id=$1`, pid, []int64{res.Fact.ID})
	if p, _ = s.GetPage(ctx, pid); p.Stale || p.ValidUntil == nil {
		t.Fatalf("an unexpired volatile source gives the page a validity deadline, not staleness: %+v", p)
	}
	_, _ = d.Pool.Exec(ctx, `UPDATE memory_facts SET expires_at=now()-interval '1 hour' WHERE id=$1`, res.Fact.ID)
	_ = mem.ExpireDue(ctx)
	if p, _ = s.GetPage(ctx, pid); !p.Stale {
		t.Fatalf("a lapsed volatile source must make the page stale: %+v", p)
	}
}
