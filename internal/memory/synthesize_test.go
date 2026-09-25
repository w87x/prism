package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"prism/internal/testutil"
)

// Level 2 reads level-1 conclusions across banks and cites only them; level 3 reads level 2. Each level is
// capped and discounted, invisible to reflection, and retired when its supports go.
func TestSynthesisLevels(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	ub, _ := s.BankBySpec(ctx, "user", "", true)
	pb, _ := s.BankBySpec(ctx, "project:Trip", "", true)
	var l1 []int64
	for i := 0; i < 6; i++ {
		bank := ub.ID
		if i%2 == 1 {
			bank = pb.ID
		}
		f := store(t, s, StoreReq{Bank: map[bool]string{true: "user", false: "project:Trip"}[bank == ub.ID], Text: fmt.Sprintf("Distinct fact number %d about a topic", i)})
		id, err := s.insertConclusion(ctx, bank, fmt.Sprintf("Belief %d: something holds across facts", i), []int64{f.ID}, 0.9, nil)
		if err != nil {
			t.Fatal(err)
		}
		l1 = append(l1, id)
	}
	ids := func(a ...int) string {
		var p []string
		for _, i := range a {
			p = append(p, fmt.Sprint(l1[i]))
		}
		return strings.Join(p, ",")
	}
	var l2reply string
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		for _, m := range req["messages"].([]any) {
			if c, _ := m.(map[string]any)["content"].(string); strings.Contains(c, "synthesising mind") {
				return testutil.Reply{Content: l2reply}
			}
		}
		return testutil.Reply{Content: `{"items":[]}`}
	}
	l2reply = `{"items":[
		{"action":"new","type":"theme","text":"Alpha runs through many areas.","evidence":[` + ids(0, 1) + `],"confidence":1},
		{"action":"new","type":"cause","text":"Beta probably lies behind the travel plans.","evidence":[` + ids(1, 2) + `],"confidence":0.9},
		{"action":"new","type":"implication","text":"Gamma means the assistant should be concise.","evidence":[` + ids(2, 3) + `],"confidence":0.9},
		{"action":"new","type":"tension","text":"Delta pulls against the budget goal.","evidence":[` + ids(3, 4) + `],"confidence":0.9},
		{"action":"new","type":"theme","text":"Cites a single belief only.","evidence":[` + ids(5) + `],"confidence":0.9}]}`
	r, err := s.Synthesize(ctx, 2, true, 0)
	if err != nil || r.New != 4 {
		t.Fatalf("level 2: %+v %v", r, err)
	}
	l2, _ := s.levelItems(ctx, 2, 50)
	for _, it := range l2 {
		if it.Confidence > maxSynthConf+0.001 || it.Confidence > 0.9*0.91 {
			t.Fatalf("level-2 confidence %.2f not discounted", it.Confidence)
		}
		if len(s.supports(ctx, it.ID, 2)) != 2 {
			t.Fatalf("supports of %d: %v", it.ID, s.supports(ctx, it.ID, 2))
		}
	}
	for _, b := range []int64{ub.ID, pb.ID} {
		if rc, _ := s.conclusions(ctx, b); len(rc) != 6/2 {
			t.Fatalf("reflection sees %d conclusions in bank %d; synthesis must be hidden", len(rc), b)
		}
	}

	l3reply := `{"items":[
		{"action":"new","type":"principle","text":"Keep answers short and grounded.","evidence":[` + fmt.Sprint(l2[0].ID, ",", l2[1].ID) + `],"confidence":0.9},
		{"action":"new","type":"gap","text":"Unknown how strict the budget is.","evidence":[` + fmt.Sprint(l2[2].ID) + `],"confidence":0.9}]}`
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		for _, m := range req["messages"].([]any) {
			if c, _ := m.(map[string]any)["content"].(string); strings.Contains(c, "deepest layer") {
				return testutil.Reply{Content: l3reply}
			}
		}
		return testutil.Reply{Content: `{"items":[]}`}
	}
	r3, err := s.Synthesize(ctx, 3, true, 0)
	if err != nil || r3.New != 1 {
		t.Fatalf("level 3: %+v %v", r3, err)
	}
	pr, _ := s.levelItems(ctx, 3, 10)
	if len(pr) != 1 || pr[0].Confidence > maxPrincipleConf+0.001 {
		t.Fatalf("principles: %+v", pr)
	}
	pv, err := s.Provenance(ctx, pr[0].ID)
	if err != nil || len(pv.Evidence) != 2 {
		t.Fatalf("provenance evidence: %+v %v", pv.Evidence, err)
	}

	// retire the level-1 beliefs shared by two syntheses: they lose its supports and goes, the principle then loses one
	for _, id := range l1[3:4] {
		s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now() WHERE id=$1`, id)
	}
	if n := s.pruneSynth(ctx, 2); n != 2 {
		t.Fatal("a synthesis whose beliefs were retired must be pruned")
	}
	if n := s.pruneSynth(ctx, 3); n != 1 {
		t.Fatalf("principle should lose support, pruned %d", n)
	}
}

// A manual reflect over all banks reports every bank, with the reason for each skip, instead of returning nothing.
func TestReflectAllReportsSkips(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	store(t, s, StoreReq{Bank: "user", Text: "Only one fact here"})
	rs, err := s.ReflectAll(ctx, true, 6)
	if err != nil || len(rs) == 0 || rs[0].Skipped == "" {
		t.Fatalf("%+v %v", rs, err)
	}
}
