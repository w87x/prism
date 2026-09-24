package memory

import (
	"context"
	"testing"
)

// This file is a small, dedicated retrieval-quality regression harness — distinct from the mechanics-level
// unit tests elsewhere in this package (effRank's formula, Store's dedup threshold, fuseRanks in isolation,
// …). Find's ranking is several independently-reasonable pieces of logic composed together (RRF fusion of
// lexical and semantic signal, a relevance floor, rank/decay weighting, confidence weighting, link
// expansion) — a small, locally-sensible tweak to any one constant can quietly break retrieval for a real
// query without any single mechanics-level test noticing. Each case here pins down one retrieval BEHAVIOR a
// user would actually notice regressing (a wrong fact never resurfacing, a pinned fact staying findable, a
// popular-but-irrelevant fact not crowding out the answer) rather than an implementation detail. Add a new
// case here whenever a ranking change is being made, or a real "why didn't it find X" report comes in.
//
// The fake embedding model (testutil.Embed) is a hashed bag-of-words vector, not a real semantic one, so
// these cases lean on lexical/token overlap for "relevance" rather than true synonym recall — that
// limitation is inherent to testing offline and is the same one every other test in this package accepts.

type retrievalCase struct {
	name string
	// seed populates the database and returns a label -> fact id map for the facts want/exclude/wantFirst
	// refer to.
	seed  func(t *testing.T, s *Service) map[string]int64
	banks []string
	query string
	// want: these labels must appear somewhere in the result set.
	want []string
	// exclude: these labels must NOT appear in the result set.
	exclude []string
	// wantFirst, if set, must be the single top-scoring result.
	wantFirst string
	// check, if set, gets the full result set and the label->id map for cases where want/exclude/wantFirst
	// aren't precise enough — e.g. asserting on the actual Score values, not just ordering (ordering among
	// near-tied results is not guaranteed stable, so a case whose whole point IS a small score difference
	// must check the scores directly or it can pass for the wrong reason).
	check func(t *testing.T, res []Fact, ids map[string]int64)
}

func runRetrievalCase(t *testing.T, c retrievalCase) {
	t.Helper()
	s, _ := newSvc(t)
	ctx := context.Background()
	ids := c.seed(t, s)
	res, err := s.Find(ctx, FindReq{Query: c.query, Banks: c.banks, K: 10, NoLinks: true})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	got := map[int64]bool{}
	order := make([]int64, len(res))
	for i, f := range res {
		got[f.ID] = true
		order[i] = f.ID
	}
	for _, w := range c.want {
		id, ok := ids[w]
		if !ok {
			t.Fatalf("bad case: seed() never labeled %q", w)
		}
		if !got[id] {
			t.Errorf("expected %q (fact #%d) in results; got %d results: %v", w, id, len(res), order)
		}
	}
	for _, x := range c.exclude {
		id, ok := ids[x]
		if !ok {
			t.Fatalf("bad case: seed() never labeled %q", x)
		}
		if got[id] {
			t.Errorf("expected %q (fact #%d) excluded from results, but it was returned: %v", x, id, order)
		}
	}
	if c.wantFirst != "" {
		id, ok := ids[c.wantFirst]
		if !ok {
			t.Fatalf("bad case: seed() never labeled %q", c.wantFirst)
		}
		if len(order) == 0 || order[0] != id {
			t.Errorf("expected %q (fact #%d) to rank first; got order %v", c.wantFirst, id, order)
		}
	}
	if c.check != nil {
		c.check(t, res, ids)
	}
}

func TestRetrievalQuality(t *testing.T) {
	cases := []retrievalCase{
		{
			name: "a clearly relevant fact outranks an unrelated one in the same bank",
			seed: func(t *testing.T, s *Service) map[string]int64 {
				flight := store(t, s, StoreReq{Bank: "project:Trip", Text: "User's flight to Lisbon departs March 3rd from seat 14C"})
				dentist := store(t, s, StoreReq{Bank: "project:Trip", Text: "User's dentist appointment is Tuesday at 9am"})
				return map[string]int64{"flight": flight.ID, "dentist": dentist.ID}
			},
			banks:     []string{"project:Trip"},
			query:     "what seat is the Lisbon flight",
			want:      []string{"flight"},
			exclude:   []string{"dentist"},
			wantFirst: "flight",
		},
		{
			name: "a corrected fact's old text never resurfaces; only its replacement does",
			seed: func(t *testing.T, s *Service) map[string]int64 {
				old := store(t, s, StoreReq{Bank: "user", Text: "User's WiFi password is oldpass123"})
				newText := "User's WiFi password is newpass456"
				r, err := s.UpdateFact(context.Background(), old.ID, &newText, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				if r.ID == old.ID {
					t.Fatal("bad case: UpdateFact was expected to supersede with a new row")
				}
				return map[string]int64{"old": old.ID, "new": r.ID}
			},
			banks:     []string{"user"},
			query:     "wifi password",
			want:      []string{"new"},
			exclude:   []string{"old"},
			wantFirst: "new",
		},
		{
			name: "Find never leaks a match from a bank outside the requested list",
			seed: func(t *testing.T, s *Service) map[string]int64 {
				a := store(t, s, StoreReq{Bank: "project:OfficeA", Text: "The printer on the 2nd floor jams every Friday afternoon"})
				b := store(t, s, StoreReq{Bank: "project:OfficeB", Text: "The printer on the 2nd floor jams every Friday afternoon"})
				if a.ID == b.ID {
					t.Fatal("bad case: expected two distinct facts, one per bank")
				}
				return map[string]int64{"officeA": a.ID, "officeB": b.ID}
			},
			banks:   []string{"project:OfficeA"},
			query:   "printer jams on Friday",
			want:    []string{"officeA"},
			exclude: []string{"officeB"},
		},
		{
			name: "a popular-but-irrelevant fact must not crowd out the actually relevant answer",
			seed: func(t *testing.T, s *Service) map[string]int64 {
				relevant := store(t, s, StoreReq{Bank: "domain:Cooking", Text: "Simmer the tomato sauce for twenty minutes before adding basil"})
				popular := store(t, s, StoreReq{Bank: "domain:Cooking", Text: "User's favorite programming language is Go"})
				// give the irrelevant fact heavy usage — high rank/hits must never substitute for actual
				// relevance; the relevance floor (MinRel) is supposed to filter it out regardless.
				if _, err := s.db.Exec(context.Background(), `UPDATE memory_facts SET rank=5, hits=50, last_used=now() WHERE id=$1`, popular.ID); err != nil {
					t.Fatal(err)
				}
				return map[string]int64{"relevant": relevant.ID, "popular": popular.ID}
			},
			banks:     []string{"domain:Cooking"},
			query:     "how long should the tomato sauce simmer",
			want:      []string{"relevant"},
			exclude:   []string{"popular"},
			wantFirst: "relevant",
		},
		{
			// Text is deliberately IDENTICAL between the two facts (in different banks, so Store's own
			// per-bank dedup can't merge them) — that makes their lexical and semantic relevance exactly
			// equal, so rank alone (via effRank's decay/pin exemption) decides the order. An earlier version
			// of this case used two differently-worded facts and passed even with the pin exemption disabled
			// (verified by deliberately breaking effRank) — the wording difference, not the pin, had been
			// deciding it. Equal text closes that gap.
			name: "a pinned fact stays findable after aging past decay while an equally-relevant unpinned one has faded",
			seed: func(t *testing.T, s *Service) map[string]int64 {
				const text = "User's blood type is O negative, tell responders in an emergency"
				pinned := store(t, s, StoreReq{Bank: "user", Text: text})
				unpinned := store(t, s, StoreReq{Bank: "domain:Health", Text: text})
				if pinned.ID == unpinned.ID {
					t.Fatal("bad case: expected two distinct facts")
				}
				if err := s.SetPinned(context.Background(), pinned.ID, true); err != nil {
					t.Fatal(err)
				}
				// backdate both past the decay half-life so only pinned's exemption keeps it competitive
				if _, err := s.db.Exec(context.Background(), `UPDATE memory_facts SET created_at=now()-interval '400 days', last_used=NULL WHERE id=ANY($1)`,
					[]int64{pinned.ID, unpinned.ID}); err != nil {
					t.Fatal(err)
				}
				return map[string]int64{"pinned": pinned.ID, "unpinned": unpinned.ID}
			},
			banks:     []string{"user", "domain:Health"},
			query:     "what is the blood type for emergencies",
			want:      []string{"pinned", "unpinned"},
			wantFirst: "pinned",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runRetrievalCase(t, c) })
	}
}

// At equal textual relevance, higher confidence must score higher — checked in BOTH storage orders. With
// literally identical text the two facts tie exactly on relevance, and a tie in reciprocal-rank fusion
// (rank.go's fuseRanks, sort.SliceStable) breaks by candidate order rather than value — so a single-order
// case can pass for the wrong reason (whichever fact happened to be stored/indexed first wins the tie,
// independent of confidence). Running both orders and requiring confidence to win either way rules that out:
// this was verified empirically before writing the assertion (both orders logged higher scores for the
// higher-confidence fact under the real code, ~0.92 vs ~0.80) and confirmed to break in the disadvantaged
// order when confidence weighting was temporarily neutralized during development.
func TestRetrievalQualityConfidenceWeightingIsOrderIndependent(t *testing.T) {
	const text = "The conference room projector needs a new HDMI adapter"
	for _, swapOrder := range []bool{false, true} {
		s, _ := newSvc(t)
		ctx := context.Background()
		var confident, unsure Fact
		if !swapOrder {
			confident = store(t, s, StoreReq{Bank: "project:ConfRoom", Text: text, Confidence: 0.9})
			unsure = store(t, s, StoreReq{Bank: "project:ConfRoom2", Text: text, Confidence: 0.3})
		} else {
			unsure = store(t, s, StoreReq{Bank: "project:ConfRoom2", Text: text, Confidence: 0.3})
			confident = store(t, s, StoreReq{Bank: "project:ConfRoom", Text: text, Confidence: 0.9})
		}
		res, err := s.Find(ctx, FindReq{Query: "conference room projector HDMI adapter",
			Banks: []string{"project:ConfRoom", "project:ConfRoom2"}, K: 10, NoLinks: true})
		if err != nil {
			t.Fatalf("swapOrder=%v: find: %v", swapOrder, err)
		}
		byID := map[int64]Fact{}
		for _, f := range res {
			byID[f.ID] = f
		}
		cf, uf := byID[confident.ID], byID[unsure.ID]
		if cf.Score <= uf.Score {
			t.Errorf("swapOrder=%v: expected the higher-confidence fact to score higher: confident=%v unsure=%v", swapOrder, cf.Score, uf.Score)
		}
	}
}
