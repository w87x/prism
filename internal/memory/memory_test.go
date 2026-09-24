package memory

import (
	"context"
	"slices"
	"strings"
	"testing"

	"prism/internal/testutil"
)

func TestStoreFindSupersedeAndRaw(t *testing.T) {
	ctx := context.Background()
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fake)
	s := New(d.Pool, r, st)
	s.VectorOn = d.VectorOn
	t.Logf("pgvector on: %v", d.VectorOn)

	// 1. store + duplicate detection
	a, err := s.Store(ctx, StoreReq{Bank: "user", Text: "User likes coffee in the morning"})
	if err != nil || a.Duplicate {
		t.Fatalf("store: %v dup=%v", err, a != nil && a.Duplicate)
	}
	dup, err := s.Store(ctx, StoreReq{Bank: "user", Text: "User likes coffee in the morning"})
	if err != nil || !dup.Duplicate || dup.Fact.ID != a.Fact.ID {
		t.Fatalf("expected duplicate of %d, got %+v err=%v", a.Fact.ID, dup, err)
	}
	if _, err := s.Store(ctx, StoreReq{Bank: "project:Price check", Text: "Milk costs 1.20 at ShopA"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store(ctx, StoreReq{Bank: "profile", Agent: "Scout", Text: "Prefer DuckDuckGo for quick lookups"}); err != nil {
		t.Fatal(err)
	}

	// 2. retrieval ranks the relevant fact first and across banks
	got, err := s.Find(ctx, FindReq{Query: "what does the user drink in the morning coffee", Banks: []string{"user", "project:Price check"}})
	if err != nil || len(got) == 0 || got[0].ID != a.Fact.ID {
		t.Fatalf("find: %+v err=%v", got, err)
	}
	if got[0].Hits != 0 { // Find bumps hits after read; the returned row is post-update
		// acceptable either way; ensure hits were persisted
	}
	f, _ := s.GetFact(ctx, a.Fact.ID)
	if f.Hits != 1 || f.LastUsed == nil {
		t.Fatalf("usage not recorded: %+v", f)
	}

	// 3. supersession: the fake chat model answers "update" for the old fact
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		return testutil.Reply{Content: `{"relations":[{"id":` + itoa(a.Fact.ID) + `,"relation":"update"}]}`}
	}
	nw, err := s.Store(ctx, StoreReq{Bank: "user", Text: "User now prefers tea in the morning instead of coffee"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nw.Superseded) != 1 || nw.Superseded[0] != a.Fact.ID {
		t.Fatalf("expected supersession of %d, got %+v", a.Fact.ID, nw)
	}
	old, _ := s.GetFact(ctx, a.Fact.ID)
	if old.ValidTo == nil || old.SupersededBy == nil || *old.SupersededBy != nw.Fact.ID {
		t.Fatalf("old fact should be retired: %+v", old)
	}
	live, _ := s.Find(ctx, FindReq{Query: "morning drink coffee tea", Banks: []string{"user"}})
	for _, f := range live {
		if f.ID == a.Fact.ID {
			t.Fatal("superseded fact returned without history flag")
		}
	}
	hist, _ := s.Find(ctx, FindReq{Query: "morning drink coffee tea", Banks: []string{"user"}, History: true})
	seen := false
	for _, f := range hist {
		seen = seen || f.ID == a.Fact.ID
	}
	if !seen {
		t.Fatal("history search should include the retired fact")
	}

	// 4. raw bank → facts, then cleanup
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		return testutil.Reply{Content: "```json\n" + `{"facts":[{"text":"User lives in Berlin","bank":"user","tags":["home"],"confidence":0.9},{"text":"Rust ownership rules","bank":"domain:Programming"}]}` + "\n```"}
	}
	for i := 0; i < 3; i++ {
		_ = s.AddRaw(ctx, RawMsg{From: "user", To: "Atlas", Text: "I live in Berlin"})
	}
	n, err := s.Process(ctx, 40, true)
	if err != nil || n != 2 {
		t.Fatalf("process: n=%d err=%v", n, err)
	}
	if s.RawCount(ctx) != 0 {
		t.Fatal("raw messages should be deleted after processing")
	}
	bs, _ := s.Banks(ctx)
	var labels []string
	for _, b := range bs {
		labels = append(labels, b.Label())
	}
	if !strings.Contains(strings.Join(labels, ","), "domain:Programming") {
		t.Fatalf("domain bank not created: %v", labels)
	}
}

func itoa(i int64) string {
	return fmtInt(i)
}
func fmtInt(i int64) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestSettingARankByHandIsRemembered(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	f := store(t, s, StoreReq{Bank: "user", Text: "User likes strong espresso in the morning"})
	same := f.Rank
	if g, err := s.UpdateFact(ctx, f.ID, nil, nil, &same); err != nil || slices.Contains(g.Tags, "user-rank") {
		t.Fatalf("an unchanged rank is not a rerank: %+v err=%v", g, err)
	}
	r := 4.0
	g, err := s.UpdateFact(ctx, f.ID, nil, nil, &r)
	if err != nil || g.Rank != 4 || !slices.Contains(g.Tags, "user-rank") {
		t.Fatalf("hand-set rank: %+v err=%v", g, err)
	}
	r2 := 2.0
	txt := "User likes strong espresso in the morning, no sugar"
	h, err := s.UpdateFact(ctx, f.ID, &txt, g.Tags, &r2)
	if err != nil || !slices.Contains(h.Tags, "user-rank") || h.Rank != 2 {
		t.Fatalf("a rerank survives a text edit: %+v err=%v", h, err)
	}
}
