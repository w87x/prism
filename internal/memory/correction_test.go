package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/testutil"
)

// A near-duplicate that is actually a correction (same sentence, changed number) must not be silently
// reinforced as a duplicate before the classifier ever sees it — that would keep the stale fact and drop
// the correction. High lexical similarity alone must not short-circuit into "same".
func TestNearDuplicateCorrectionIsSupersededNotReinforced(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	// many shared tokens with only the price differing: lexically (and, via the hashed bag-of-words fake
	// embedder, semantically) this sits well above the old immediate-duplicate threshold (jaccard ~0.95,
	// cosine ~0.97) despite being a correction, not a repeat — exactly the case the fast path got wrong.
	words := make([]string, 40)
	for i := range words {
		words[i] = fmt.Sprintf("landmark%d", i)
	}
	prefix := strings.Join(words, " ") + " the ShopA price per liter of milk is "
	old := store(t, s, StoreReq{Bank: "project:Price check", Text: prefix + "1.20"})

	fake.Handler = func(req map[string]any, n int) testutil.Reply {
		return testutil.Reply{Content: fmt.Sprintf(`{"relations":[{"id":%d,"relation":"update"}]}`, old.ID)}
	}
	res, err := s.Store(ctx, StoreReq{Bank: "project:Price check", Text: prefix + "1.35"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Duplicate {
		t.Fatalf("a corrected price must not be treated as a duplicate: %+v", res)
	}
	if len(res.Superseded) != 1 || res.Superseded[0] != old.ID {
		t.Fatalf("expected the old price to be superseded, got %+v", res)
	}
	stale, err := s.GetFact(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.ValidTo == nil || stale.SupersededBy == nil || *stale.SupersededBy != res.Fact.ID {
		t.Fatalf("old fact should be retired and point at the new one: %+v", stale)
	}
	live, err := s.Find(ctx, FindReq{Query: "ShopA milk price per liter", Banks: []string{"project:Price check"}, NoLinks: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range live {
		if f.ID == old.ID {
			t.Fatal("the superseded (stale) price must not appear in a live search")
		}
	}
}

// Exact repeats (the common case: the same fact mentioned again) are still merged automatically without a
// model call — the fast path stays for genuine duplicates, just not for merely-similar text.
func TestExactRepeatStillDedupesWithoutAModelCall(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	a := store(t, s, StoreReq{Bank: "user", Text: "User prefers tea over coffee"})
	fake.Handler = func(map[string]any, int) testutil.Reply {
		t.Fatal("an exact repeat must not need the classifier")
		return testutil.Reply{}
	}
	// case/whitespace differences still count as the same fact
	res, err := s.Store(ctx, StoreReq{Bank: "user", Text: "  user PREFERS   tea over coffee  "})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Duplicate || res.Fact.ID != a.ID {
		t.Fatalf("expected an exact-match duplicate of %d, got %+v", a.ID, res)
	}
}

// A model that names a fact id it was never shown (hallucinated, or from a different bank entirely) must
// not be able to retire that fact — only candidates actually offered to the classifier are eligible.
func TestSupersedeIgnoresFactIDsOutsideTheOfferedCandidates(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	// a real fact that was never offered as a candidate (different bank, unrelated text)
	victim := store(t, s, StoreReq{Bank: "user", Text: "User's passport expires in March 2027"})
	related := store(t, s, StoreReq{Bank: "project:Price check", Text: "Milk costs 1.20 at ShopA"})

	fake.Handler = func(map[string]any, int) testutil.Reply {
		// the model claims to update the candidate it WAS shown, but also tries to retire an unrelated fact
		return testutil.Reply{Content: fmt.Sprintf(`{"relations":[{"id":%d,"relation":"update"},{"id":%d,"relation":"contradiction"}]}`, related.ID, victim.ID)}
	}
	res, err := s.Store(ctx, StoreReq{Bank: "project:Price check", Text: "Milk costs 1.35 at ShopA"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Superseded) != 1 || res.Superseded[0] != related.ID {
		t.Fatalf("only the offered candidate may be superseded: %+v", res)
	}
	v, err := s.GetFact(ctx, victim.ID)
	if err != nil || v.ValidTo != nil {
		t.Fatalf("a fact outside the candidate set must survive untouched: %+v err=%v", v, err)
	}
}

// Several offered candidates can be superseded by one new fact in one Store call; every one of them gets
// retired and points at the new fact (the insert and every update happen in one transaction).
func TestStoreSupersedesMultipleCandidatesAtomically(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	a := store(t, s, StoreReq{Bank: "domain:Go", Text: "The project now targets Go 1.21"})
	b := store(t, s, StoreReq{Bank: "domain:Go", Text: "The project now targets Go 1.22"})

	fake.Handler = func(map[string]any, int) testutil.Reply {
		return testutil.Reply{Content: fmt.Sprintf(`{"relations":[{"id":%d,"relation":"update"},{"id":%d,"relation":"update"}]}`, a.ID, b.ID)}
	}
	res, err := s.Store(ctx, StoreReq{Bank: "domain:Go", Text: "The project now targets Go 1.23"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Superseded) != 2 {
		t.Fatalf("expected both candidates superseded, got %+v", res)
	}
	for _, id := range []int64{a.ID, b.ID} {
		f, err := s.GetFact(ctx, id)
		if err != nil || f.ValidTo == nil || f.SupersededBy == nil || *f.SupersededBy != res.Fact.ID {
			t.Fatalf("fact %d not properly superseded: %+v err=%v", id, f, err)
		}
	}
}

// Without a fast model to classify, a merely-similar candidate must not be guessed at either way — the new
// fact is stored on its own rather than risking a wrong auto-merge.
func TestNoClassifierStoresRatherThanGuessing(t *testing.T) {
	ctx := context.Background()
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	fake.Handler = func(map[string]any, int) testutil.Reply {
		t.Fatal("no fast model should be configured for this test")
		return testutil.Reply{}
	}
	// RoleRef("fast") falls back to "chat" and then to any registered chat-kind model, so the only way to make
	// it genuinely unavailable is to register no chat model at all — only an embedding one (so near-neighbour
	// detection still runs; it is the classifier specifically that must be unreachable).
	st := settings.New(d.Pool)
	r := llm.NewRouter(d.Pool, st)
	pid, err := r.Store().SaveProvider(ctx, llm.Provider{Name: "fake", Kind: "custom", BaseURL: fake.URL + "/v1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store().SaveModel(ctx, llm.Model{Name: "embed", ProviderID: pid, ModelID: "fake-embed", Kind: "embedding", ContextWindow: 512}); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, settings.KeyModelRoles, settings.ModelRoles{Embedding: "embed"}); err != nil {
		t.Fatal(err)
	}
	s := New(d.Pool, r, st)
	s.VectorOn = d.VectorOn
	a := store(t, s, StoreReq{Bank: "project:Price check", Text: "Milk costs 1.20 at ShopA"})
	res, err := s.Store(ctx, StoreReq{Bank: "project:Price check", Text: "Milk costs 1.35 at ShopA"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Duplicate || res.Fact.ID == a.ID {
		t.Fatalf("without a classifier, a similar-but-different fact must not be merged: %+v", res)
	}
	if len(res.Superseded) != 0 {
		t.Fatalf("without a classifier, nothing should be superseded either: %+v", res)
	}
}

func TestNormFactText(t *testing.T) {
	if normFactText("  User  PREFERS Tea ") != normFactText("user prefers tea") {
		t.Fatal("normalization should ignore case and extra whitespace")
	}
	if normFactText("User prefers tea") == normFactText("User prefers coffee") {
		t.Fatal("different words must not normalize the same")
	}
}

var _ = strings.TrimSpace
