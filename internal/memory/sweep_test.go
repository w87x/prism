package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"prism/internal/testutil"
)

// A paraphrase is retired in favour of the better-worded fact, facts that only share a template stay apart, a dry run changes nothing, the
// judged pairs are not asked again, and the merge can be undone.
func TestSweepMergesParaphrasesNotLookAlikes(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	a := store(t, s, StoreReq{Bank: "user", Text: "User prefers BDRip releases over WEB-DL for movies"})
	b := store(t, s, StoreReq{Bank: "user", Text: "User prefers BDRip releases over WEB-DL for movies, always"})
	c := store(t, s, StoreReq{Bank: "user", Text: "Power outage in Ivanovka village on Monday morning"})
	d := store(t, s, StoreReq{Bank: "user", Text: "Power outage in Petrovka village on Monday morning"})
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		for _, m := range req["messages"].([]any) {
			cc, _ := m.(map[string]any)["content"].(string)
			if !strings.Contains(cc, "PAIR 1") {
				continue
			}
			var out []string
			for _, blk := range strings.Split(cc, "PAIR ")[1:] {
				n := blk[:strings.Index(blk, " ")]
				if strings.Contains(blk, "BDRip") {
					out = append(out, fmt.Sprintf(`{"n":%s,"verdict":"same","keep":%d,"note":"paraphrase"}`, n, a.ID))
				} else {
					out = append(out, fmt.Sprintf(`{"n":%s,"verdict":"distinct"}`, n))
				}
			}
			return testutil.Reply{Content: `{"pairs":[` + strings.Join(out, ",") + `]}`}
		}
		return testutil.Reply{Content: `{}`}
	}
	live := func(id int64) bool {
		var v bool
		_ = s.db.QueryRow(ctx, `SELECT valid_to IS NULL FROM memory_facts WHERE id=$1`, id).Scan(&v)
		return v
	}
	dry, err := s.Sweep(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Skipped != "" || len(dry.Items) != 1 || !live(b.ID) {
		t.Fatalf("a dry run reports the merge and changes nothing: %+v", dry)
	}
	r, err := s.Sweep(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Merged != 1 || live(b.ID) || !live(a.ID) || !live(c.ID) || !live(d.ID) {
		t.Fatalf("result %+v (a live %v, b live %v, c live %v, d live %v)", r, live(a.ID), live(b.ID), live(c.ID), live(d.ID))
	}
	if again, _ := s.Sweep(ctx, false); again.Skipped == "" {
		t.Fatalf("judged pairs must not be asked again: %+v", again)
	}
	ops, _ := s.Ops(ctx)
	if len(ops) == 0 || ops[0].Kind != "sweep" {
		t.Fatalf("the merge should be in the undo log: %+v", ops)
	}
	if _, err := s.Undo(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if !live(b.ID) {
		t.Fatal("undo should bring the fact back")
	}
	if again, _ := s.Sweep(ctx, false); again.Merged != 0 {
		t.Fatalf("an undone pair must not be merged again: %+v", again)
	}
}

func TestFadeExpiresSoonAndSpareThePinned(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	f := store(t, s, StoreReq{Bank: "user", Text: "User once glanced at a weather widget"})
	p := store(t, s, StoreReq{Bank: "user", Text: "User is allergic to penicillin"})
	_ = s.SetPinned(ctx, p.ID, true)
	if err := s.Fade(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	var soon bool
	var rank float64
	_ = s.db.QueryRow(ctx, `SELECT expires_at < now()+interval '8 days' AND expires_at > now()+interval '6 days', rank FROM memory_facts WHERE id=$1`, f.ID).Scan(&soon, &rank)
	if !soon || rank > 0.06 {
		t.Fatalf("fade should expire in a week and rank last: soon=%v rank=%v", soon, rank)
	}
	if s.Fade(ctx, p.ID) == nil {
		t.Fatal("a pinned fact must be refused")
	}
}

func TestHealConclusionsRepairsOrRetires(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	f1 := store(t, s, StoreReq{Bank: "user", Text: "User keeps movies in per-title folders"})
	f2 := store(t, s, StoreReq{Bank: "user", Text: "User keeps series in per-season folders"})
	f3 := store(t, s, StoreReq{Bank: "user", Text: "User keeps music in per-album folders"})
	f4 := store(t, s, StoreReq{Bank: "user", Text: "User keeps photos in per-year folders"})
	bank, _ := s.BankBySpec(ctx, "user", "", false)
	mk := func(text string, ev ...int64) int64 {
		id, err := s.insertDerived(ctx, bank.ID, text, ev, 0.7, nil, "analysis", nil, maxConclusionConf)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	healthy := mk("User files everything by a natural unit", f1.ID, f2.ID, f3.ID) // one of three dies: dropped
	doomed := mk("User files things by title", f1.ID, f4.ID)                      // one of two dies: retired
	_ = s.MarkOutdated(ctx, f1.ID)
	r, err := s.HealConclusions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Dropped != 1 || r.Retired != 1 {
		t.Fatalf("result %+v", r)
	}
	live := func(id int64) bool {
		var v bool
		_ = s.db.QueryRow(ctx, `SELECT valid_to IS NULL FROM memory_facts WHERE id=$1`, id).Scan(&v)
		return v
	}
	if !live(healthy) || live(doomed) {
		t.Fatalf("healthy live=%v doomed live=%v", live(healthy), live(doomed))
	}
	var stale bool
	_ = s.db.QueryRow(ctx, `SELECT `+staleConclusionExpr("f")+` FROM memory_facts f WHERE id=$1`, healthy).Scan(&stale)
	if stale {
		t.Fatal("the surviving conclusion should no longer be stale")
	}
}

// The charter is a core block with a longer limit and its own prompt section: it is not repeated in the core text.
func TestCharterLivesInCoreMemory(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	long := strings.Repeat("- never download without asking. ", 40) // > 800, < 1600
	if err := s.SetCoreBlock(ctx, "charter", long, "house rules", "user"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCoreBlock(ctx, "preferences", long, "", "user"); err == nil {
		t.Fatal("an ordinary block keeps its 800-character limit")
	}
	_ = s.SetCoreBlock(ctx, "preferences", "Answers in Russian", "", "user")
	if got := s.CharterText(ctx); !strings.Contains(got, "never download") {
		t.Fatalf("charter text: %q", got)
	}
	if core := s.CoreText(ctx); strings.Contains(core, "download") || !strings.Contains(core, "Russian") {
		t.Fatalf("core text must carry the other blocks only: %q", core)
	}
}
