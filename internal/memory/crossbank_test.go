package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"prism/internal/testutil"
)

// Facts that sit in different banks are compared with each other: a contradiction becomes a review link, a duplicate
// is merged (the keeper turns up in the other bank too), a connection must rest on facts from two banks, and the
// watermark stops a re-run.
func TestAnalyzeAcrossBanks(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	a := store(t, s, StoreReq{Bank: "user", Text: "User lives in Munich Germany with family"})
	store(t, s, StoreReq{Bank: "project:Trip", Text: "User lives in Berlin Germany with family"})
	c := store(t, s, StoreReq{Bank: "user", Text: "User prefers window seats on long flights"})
	d := store(t, s, StoreReq{Bank: "project:Trip", Text: "User prefers window seats on long flights always"})
	store(t, s, StoreReq{Bank: "user", Text: "Unrelated note about gardening tools and soil"})
	// the model answers by pair number; work out which pair is which from the prompt it receives
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		for _, m := range req["messages"].([]any) {
			cc, _ := m.(map[string]any)["content"].(string)
			if !strings.Contains(cc, "PAIR 1") {
				continue
			}
			var out []string
			for _, blk := range strings.Split(cc, "PAIR ")[1:] {
				n := blk[:strings.Index(blk, " ")]
				switch {
				case strings.Contains(blk, "Munich") && strings.Contains(blk, "Berlin"):
					out = append(out, fmt.Sprintf(`{"n":%s,"verdict":"contradiction","note":"Munich vs Berlin"}`, n))
				case strings.Contains(blk, "window seats"):
					out = append(out, fmt.Sprintf(`{"n":%s,"verdict":"duplicate","keep":%d}`, n, c.ID))
				}
			}
			return testutil.Reply{Content: fmt.Sprintf(`{"pairs":[%s],"connections":[
				{"text":"User travels with family and cares about seat comfort.","evidence":[%d,%d],"confidence":0.8},
				{"text":"Single bank claim.","evidence":[%d,%d],"confidence":0.8}]}`, strings.Join(out, ","), a.ID, d.ID, a.ID, c.ID)}
		}
		return testutil.Reply{Content: `{}`}
	}
	r, err := s.AnalyzeAcross(ctx, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Contradictions != 1 || r.Duplicates != 1 || r.Connections != 1 {
		t.Fatalf("result %+v", r)
	}
	if ls, _ := s.Links(ctx, a.ID); !hasKind(ls, LinkContradicts) {
		t.Fatalf("contradiction link missing: %+v", ls)
	}
	pb, _ := s.BankBySpec(ctx, "project:Trip", "", false)
	var visible bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_fact_banks WHERE fact_id=$1 AND bank_id=$2)`, c.ID, pb.ID).Scan(&visible)
	if !visible {
		t.Fatal("the kept duplicate should also be visible in the other bank")
	}
	var gone bool
	_ = s.db.QueryRow(ctx, `SELECT valid_to IS NOT NULL FROM memory_facts WHERE id=$1`, d.ID).Scan(&gone)
	if !gone {
		t.Fatal("the duplicate copy should be retired")
	}
	again, _ := s.AnalyzeAcross(ctx, false, 0)
	if again.Skipped == "" {
		t.Fatalf("watermark should stop a re-run: %+v", again)
	}
}

func hasKind(ls []Linked, kind string) bool {
	for _, l := range ls {
		if l.LinkKind == kind {
			return true
		}
	}
	return false
}
