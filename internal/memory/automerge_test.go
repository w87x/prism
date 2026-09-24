package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"prism/internal/testutil"
)

func TestAutoMergeBanksGroupsSameTopicAndLeavesOthersAlone(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	store(t, s, StoreReq{Bank: "domain:GLM", Text: "GLM is a family of bilingual LLMs from Zhipu AI"})
	store(t, s, StoreReq{Bank: "domain:GLM", Text: "GLM has an earlier release history than GLM-4"})
	store(t, s, StoreReq{Bank: "domain:GLM-4.6", Text: "GLM-4.6 quantized at Q4 fits in about 65GB"})
	store(t, s, StoreReq{Bank: "domain:GLM-5.3", Text: "GLM-5.3 adds native tool calling support"})
	store(t, s, StoreReq{Bank: "domain:Cooking", Text: "Salt pasta water generously"})
	store(t, s, StoreReq{Bank: "project:Trip plan", Text: "Flights to Lisbon leave on 3 May"})
	store(t, s, StoreReq{Bank: "project:Trip notes", Text: "Hotel is near Alfama"})

	glm, _ := s.BankBySpec(ctx, "domain:GLM", "", false)
	glm46, _ := s.BankBySpec(ctx, "domain:GLM-4.6", "", false)
	glm53, _ := s.BankBySpec(ctx, "domain:GLM-5.3", "", false)
	cooking, _ := s.BankBySpec(ctx, "domain:Cooking", "", false)
	plan, _ := s.BankBySpec(ctx, "project:Trip plan", "", false)
	notes, _ := s.BankBySpec(ctx, "project:Trip notes", "", false)

	fake.Handler = func(req map[string]any, n int) testutil.Reply {
		// the system prompt itself mentions "GLM" as an example, so route on the LAST message (the actual bank list) only
		msgs, _ := req["messages"].([]any)
		body := ""
		if len(msgs) > 0 {
			if last, ok := msgs[len(msgs)-1].(map[string]any); ok {
				body = fmt.Sprint(last["content"])
			}
		}
		switch {
		case strings.Contains(body, "GLM"):
			return testutil.Reply{Content: fmt.Sprintf(`{"groups":[{"name":"GLM","ids":[%d,%d,%d,999999]}]}`, glm.ID, glm46.ID, glm53.ID)}
		case strings.Contains(body, "Trip"):
			return testutil.Reply{Content: fmt.Sprintf(`{"groups":[{"name":"Trip","ids":[%d,%d]}]}`, plan.ID, notes.ID)}
		}
		return testutil.Reply{Content: `{"groups":[]}`}
	}

	rs, err := s.AutoMergeBanks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("expected 2 merges (domain + project), got %+v", rs)
	}
	// the 2 GLM version banks collapsed into GLM (the one with the most facts), Cooking untouched
	if _, err := s.BankBySpec(ctx, "domain:GLM-4.6", "", false); err == nil {
		t.Fatal("domain:GLM-4.6 should have been merged away")
	}
	if _, err := s.BankBySpec(ctx, "domain:GLM-5.3", "", false); err == nil {
		t.Fatal("domain:GLM-5.3 should have been merged away")
	}
	merged, err := s.BankBySpec(ctx, "domain:GLM", "", false)
	if err != nil {
		t.Fatalf("the primary bank (most facts) should keep its name: %v", err)
	}
	fs, _ := s.Facts(ctx, merged.ID, "", false, 20, 0)
	if len(fs) != 4 {
		t.Fatalf("expected 4 facts in the merged GLM bank, got %d: %+v", len(fs), fs)
	}
	if _, err := s.BankBySpec(ctx, "domain:Cooking", "", false); err != nil {
		t.Fatal("an unrelated bank must not be touched")
	}
	_ = cooking
	if _, err := s.BankBySpec(ctx, "project:Trip notes", "", false); err == nil {
		t.Fatal("project:Trip notes should have been merged away")
	}
	if _, err := s.BankBySpec(ctx, "project:Trip plan", "", false); err != nil {
		t.Fatal("project:Trip plan (more facts) should be the survivor")
	}

	// undoable, same as a manual merge
	ops, err := s.Ops(ctx)
	if err != nil || len(ops) != 2 {
		t.Fatalf("auto-merges must be logged for undo: %+v err=%v", ops, err)
	}
}

func TestSuggestBankMergesRejectsInventedAndSingletonGroups(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	store(t, s, StoreReq{Bank: "domain:A", Text: "fact a"})
	store(t, s, StoreReq{Bank: "domain:B", Text: "fact b"})
	a, _ := s.BankBySpec(ctx, "domain:A", "", false)

	fake.Handler = func(map[string]any, int) testutil.Reply {
		return testutil.Reply{Content: fmt.Sprintf(`{"groups":[{"name":"lonely","ids":[%d]},{"name":"ghost","ids":[999999,999998]}]}`, a.ID)}
	}
	gs, err := s.SuggestBankMerges(ctx, KindDomain)
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 0 {
		t.Fatalf("a single real bank and a group of invented ids must both be dropped: %+v", gs)
	}
	if _, err := s.SuggestBankMerges(ctx, "bogus"); err == nil {
		t.Fatal("unknown kind accepted")
	}
	// fewer than 2 banks of a kind: no model call, no error
	s2, fake2 := newSvc(t)
	fake2.Handler = func(map[string]any, int) testutil.Reply {
		t.Fatal("model called with fewer than 2 banks")
		return testutil.Reply{}
	}
	if gs, err := s2.SuggestBankMerges(ctx, KindProject); err != nil || len(gs) != 0 {
		t.Fatalf("expected no-op: %+v %v", gs, err)
	}
}
