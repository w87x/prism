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
