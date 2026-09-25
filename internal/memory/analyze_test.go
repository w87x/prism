package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"prism/internal/testutil"
)

func analysisReply(t *testing.T, s string) func(map[string]any, int) testutil.Reply {
	return func(req map[string]any, _ int) testutil.Reply {
		for _, m := range req["messages"].([]any) {
			if c, _ := m.(map[string]any)["content"].(string); strings.Contains(c, "analytical mind") {
				return testutil.Reply{Content: s}
			}
		}
		return testutil.Reply{Content: `{"relations":[]}`}
	}
}

// One analysis pass: a pattern with enough evidence is stored as a tagged insight, an under-evidenced one is
// dropped, a hypothesis is capped, a contradiction becomes a review-inbox link, the weaker duplicate is
// retired, the card is saved and reaches the guidance shown to the memory model, and the watermark stops a
// re-run.
func TestAnalyzeDerivesInsightsContradictionsDuplicatesAndCard(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	var ids []int64
	texts := []string{
		"User drinks green tea every morning before work",
		"User bought a new ceramic teapot last week",
		"User asked for tea shop recommendations in Berlin",
		"User moved to Berlin in March",
		"User lives in Munich",
		"User lives in Munich, Germany",
	}
	for _, x := range texts {
		ids = append(ids, store(t, s, StoreReq{Bank: "user", Text: x}).ID)
	}
	bank, _ := s.BankBySpec(ctx, "user", "", false)
	e := func(a ...int) string {
		var p []string
		for _, i := range a {
			p = append(p, fmt.Sprint(ids[i]))
		}
		return strings.Join(p, ",")
	}
	fake.Handler = analysisReply(t, `{"insights":[
		{"action":"new","type":"pattern","text":"User is a dedicated tea drinker.","evidence":[`+e(0, 1, 2)+`],"confidence":0.8},
		{"action":"new","type":"pattern","text":"User likes gadgets.","evidence":[`+e(0, 1)+`],"confidence":0.8},
		{"action":"new","type":"hypothesis","text":"User probably relocated for work.","evidence":[`+e(3, 2)+`],"confidence":0.95},
		{"action":"new","type":"nonsense","text":"Bogus type.","evidence":[`+e(0, 1, 2)+`],"confidence":0.9}],
		"contradictions":[{"a":`+e(3)+`,"b":`+e(5)+`,"note":"moved to Berlin vs lives in Munich"}],
		"duplicates":[{"keep":`+e(5)+`,"drop":[`+e(4)+`]}],
		"card":"Danil lives in Germany and loves tea."}`)
	res, err := s.Analyze(ctx, bank.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Insights != 2 || res.Contradictions != 1 || res.Duplicates != 1 || !res.CardUpdated {
		t.Fatalf("result = %+v", res)
	}
	ins, _ := s.analysisInsights(ctx, bank.ID)
	if len(ins) != 2 {
		t.Fatalf("insights = %+v", ins)
	}
	for _, in := range ins {
		if insightType(in.Tags) == "" || in.Source != "analysis" {
			t.Fatalf("insight not tagged: %+v", in)
		}
		if insightType(in.Tags) == "hypothesis" && in.Confidence > maxHypothesisConf+0.001 {
			t.Fatalf("hypothesis confidence %.2f exceeds the cap", in.Confidence)
		}
	}
	if rc, _ := s.conclusions(ctx, bank.ID); len(rc) != 0 {
		t.Fatalf("reflection must not see analysis insights: %+v", rc)
	}
	rv, _ := s.Review(ctx, 10)
	if len(rv.Contradictions) != 1 {
		t.Fatalf("contradiction missing from review: %+v", rv.Contradictions)
	}
	if f, _ := s.GetFact(ctx, ids[4]); f.ValidTo == nil {
		t.Fatal("duplicate fact should have been retired")
	}
	if !strings.Contains(s.guidance(ctx), "Germany and loves tea") {
		t.Fatal("the user's card must reach the guidance")
	}

	fake.Handler = func(map[string]any, int) testutil.Reply {
		t.Fatal("model called again with nothing new")
		return testutil.Reply{}
	}
	if r, _ := s.Analyze(ctx, bank.ID, false, 0); r.Skipped == "" {
		t.Fatalf("the watermark must skip the second pass: %+v", r)
	}
	h, _ := s.Health(ctx)
	if len(h) == 0 || h[0].Insights != 2 || h[0].Card == "" {
		t.Fatalf("health = %+v", h)
	}
}

func TestSameBankName(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"Futurama Torrent", "FuturamaTorrent", true},
		{"Futurama Torent", "Futurama Torrent", true},
		{"GLM-4.6", "GLM-5.3", false},
		{"GLM", "GLN", false},
		{"Trip planning", "Trip plans", false},
		{"Home server setup", "Home server setpu", true},
	} {
		if got := sameBankName(c.a, c.b); got != c.want {
			t.Errorf("sameBankName(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDedupeBanksMergesTypoNames(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	store(t, s, StoreReq{Bank: "project:Futurama Torrent", Text: "Wants season 14 of Futurama as torrent"})
	store(t, s, StoreReq{Bank: "project:Futurama Torent", Text: "Prefers Russian dubbing for Futurama"})
	store(t, s, StoreReq{Bank: "project:Other thing", Text: "Something else entirely"})
	rs, err := s.DedupeBanks(ctx)
	if err != nil || len(rs) != 1 || rs[0].Moved != 1 {
		t.Fatalf("merge result = %+v err=%v", rs, err)
	}
	bs, _ := s.Banks(ctx)
	n := 0
	for _, b := range bs {
		if b.Kind == KindProject {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 project banks left, got %d", n)
	}
}

func TestReviewOpenInsightsAndUnverifiedFactsCanBeResolved(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	web := store(t, s, StoreReq{Bank: "user", Text: "The Futurama season 14 torrent exists on site X", Confidence: 0.4, Origin: "x.example"})
	var ids []int64
	for i := 0; i < 3; i++ {
		ids = append(ids, store(t, s, StoreReq{Bank: "user", Text: fmt.Sprintf("Distinct trusted fact %d about gardening tools", i)}).ID)
	}
	bank, _ := s.BankBySpec(ctx, "user", "", false)
	hyp, err := s.insertDerived(ctx, bank.ID, "User probably wants a bigger greenhouse.", ids[:2], 0.7, nil, "analysis", []string{"insight", "hypothesis"}, maxHypothesisConf)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := s.insertDerived(ctx, bank.ID, "Which garden size does the user have?", ids[:1], 0.5, nil, "analysis", []string{"insight", "question"}, maxHypothesisConf)
	rv, _ := s.Review(ctx, 20)
	if len(rv.Open) != 2 || len(rv.Unverified) != 1 || rv.Unverified[0].ID != web.ID {
		t.Fatalf("review = open %d unverified %+v", len(rv.Open), rv.Unverified)
	}
	if err := s.ConfirmFact(ctx, web.ID); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.GetFact(ctx, web.ID); f.Confidence < 0.9 {
		t.Fatalf("confirmed fact confidence = %v", f.Confidence)
	}
	if _, err := s.ResolveInsight(ctx, q, "answer", ""); err == nil {
		t.Fatal("an empty answer must be refused")
	}
	made, err := s.ResolveInsight(ctx, q, "answer", "About 50 square metres.")
	if err != nil || made == nil || !strings.Contains(made.Text, "50 square metres") || made.Confidence < 0.9 {
		t.Fatalf("answer -> %+v err=%v", made, err)
	}
	if _, err := s.ResolveInsight(ctx, hyp, "reject", ""); err != nil {
		t.Fatal(err)
	}
	rv, _ = s.Review(ctx, 20)
	if len(rv.Open) != 0 || len(rv.Unverified) != 0 {
		t.Fatalf("everything was resolved: %+v", rv)
	}
}

// The research brief for a fact, an entity and a topic names the subject and what memory already holds.
func TestEnrichBriefDescribesTheSubjectAndWhatIsKnown(t *testing.T) {
	ctx := context.Background()
	s, fake := newSvc(t)
	f1 := store(t, s, StoreReq{Bank: "project:Trip", Text: "The Berlin trip is planned for May 2026"})
	store(t, s, StoreReq{Bank: "project:Trip", Text: "Hotel near Alexanderplatz costs about 120 euro per night"})
	bank, _ := s.BankBySpec(ctx, "project:Trip", "", false)
	title, in, err := s.EnrichBrief(ctx, EnrichReq{FactID: f1.ID, Note: "focus on prices"})
	if err != nil || !strings.HasPrefix(title, "Research: The Berlin trip") || !strings.Contains(in, "May 2026") || !strings.Contains(in, "project:Trip") || !strings.Contains(in, "focus on prices") {
		t.Fatalf("fact brief: %q %q err=%v", title, in, err)
	}
	if _, in, err := s.EnrichBrief(ctx, EnrichReq{BankID: bank.ID}); err != nil || !strings.Contains(in, "Alexanderplatz") {
		t.Fatalf("bank brief: %q err=%v", in, err)
	}
	if _, in, err := s.EnrichBrief(ctx, EnrichReq{Topic: "Berlin hotel prices"}); err != nil || !strings.Contains(in, "SUBJECT: Berlin hotel prices") {
		t.Fatalf("topic brief: %q err=%v", in, err)
	}
	if _, _, err := s.EnrichBrief(ctx, EnrichReq{}); err == nil {
		t.Fatal("an empty request must be refused")
	}
	_ = fake
	fake.Handler = func(map[string]any, int) testutil.Reply {
		return testutil.Reply{Content: entityReply(`{"name":"Berlin","kind":"place","facts":[`+fmt.Sprint(f1.ID)+`]}`, "").Content}
	}
	if _, err := s.ExtractEntities(ctx, bank.ID, true); err != nil {
		t.Fatal(err)
	}
	g, _ := s.EntityGraph(ctx, bank.ID, false, 0)
	if len(g.Nodes) == 0 {
		t.Fatal("no entity")
	}
	if _, in, err := s.EnrichBrief(ctx, EnrichReq{EntityID: g.Nodes[0].ID}); err != nil || !strings.Contains(in, "Berlin (place)") || !strings.Contains(in, "May 2026") {
		t.Fatalf("entity brief: %q err=%v", in, err)
	}
}

func TestDigestSumsUpWhatMemoryDid(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	old := store(t, s, StoreReq{Bank: "user", Text: "An old fact about kayaking on the Spree"})
	if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET created_at=now()-interval '30 days' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 3; i++ {
		ids = append(ids, store(t, s, StoreReq{Bank: "project:Trip", Text: fmt.Sprintf("Trip fact %d about hotel number %d in Berlin", i, i*13)}).ID)
	}
	bank, _ := s.BankBySpec(ctx, "project:Trip", "", false)
	if _, err := s.insertDerived(ctx, bank.ID, "The user probably prefers central hotels.", ids[:2], 0.7, nil, "analysis", []string{"insight", "hypothesis"}, maxHypothesisConf); err != nil {
		t.Fatal(err)
	}
	if _, err := s.insertDerived(ctx, bank.ID, "Which month does the trip start?", ids[:1], 0.5, nil, "analysis", []string{"insight", "question"}, maxHypothesisConf); err != nil {
		t.Fatal(err)
	}
	d, err := s.Digest(ctx, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if d.Empty() || d.NewFacts != 3 || len(d.Insights) != 2 || len(d.Questions) != 1 {
		t.Fatalf("digest = %+v", d)
	}
	md := d.Markdown()
	for _, want := range []string{"3 new facts", "project:Trip", "[hypothesis] The user probably prefers central hotels.", "Which month does the trip start?", "Memory would like to know"} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "kayaking") {
		t.Fatal("old facts must not appear")
	}
	if e, _ := s.Digest(ctx, time.Now().Add(time.Hour)); !e.Empty() {
		t.Fatalf("a digest of the future is empty: %+v", e)
	}
}

// A verdict from a checker: a confirmation needs two different sites, makes a web fact trusted, and turns a
// confirmed hypothesis into a fact; a contradiction retires; an unclear result is not queued again for a while.
func TestVerifyVerdictsApplyAndQueueRespectsTries(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	web := store(t, s, StoreReq{Bank: "user", Text: "The AMD AI 395 launches in Q4 2026", Confidence: 0.4, Origin: "a.example"})
	bad := store(t, s, StoreReq{Bank: "user", Text: "The Berlin wall fell in 1991", Confidence: 0.4, Origin: "b.example"})
	vague := store(t, s, StoreReq{Bank: "user", Text: "Some rumour about a product name change", Confidence: 0.4, Origin: "c.example"})
	var ids []int64
	for i := 0; i < 3; i++ {
		ids = append(ids, store(t, s, StoreReq{Bank: "user", Text: fmt.Sprintf("Trusted fact %d about gardening tools %d", i, i*9)}).ID)
	}
	bank, _ := s.BankBySpec(ctx, "user", "", false)
	hyp, _ := s.insertDerived(ctx, bank.ID, "The user probably plans a greenhouse.", ids[:2], 0.7, nil, "analysis", []string{"insight", "hypothesis"}, maxHypothesisConf)

	q, err := s.VerifyQueue(ctx, 10)
	if err != nil || len(q) != 4 || q[0].Kind != "fact" || q[len(q)-1].Kind != "hypothesis" {
		t.Fatalf("queue = %+v err=%v", q, err)
	}
	if _, err := s.ApplyVerdict(ctx, web.ID, "confirmed", "", []string{"only.example"}); err == nil {
		t.Fatal("one site is not a confirmation")
	}
	if _, err := s.ApplyVerdict(ctx, web.ID, "confirmed", "", []string{"x.example", "y.example"}); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.GetFact(ctx, web.ID); f.Confidence < 0.5 || slices.Contains(f.Tags, "unverified") {
		t.Fatalf("confirmed fact = %+v", f)
	}
	if _, err := s.ApplyVerdict(ctx, bad.ID, "contradicted", "wrong year", nil); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.GetFact(ctx, bad.ID); f.ValidTo == nil {
		t.Fatal("contradicted fact must be retired")
	}
	if _, err := s.ApplyVerdict(ctx, vague.ID, "unclear", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyVerdict(ctx, hyp, "confirmed", "", []string{"x.example", "z.example"}); err != nil {
		t.Fatal(err)
	}
	if rv, _ := s.Review(ctx, 20); len(rv.Open) != 0 {
		t.Fatalf("the confirmed hypothesis must be closed: %+v", rv.Open)
	}
	q, _ = s.VerifyQueue(ctx, 10)
	if len(q) != 0 {
		t.Fatalf("everything was handled or tried recently: %+v", q)
	}
	title, in := s.VerifyBrief([]VerifyItem{{ID: 7, Text: "A claim", Kind: "fact", Bank: "user"}})
	if !strings.Contains(title, "1 claim") || !strings.Contains(in, "#7") || !strings.Contains(in, "memory_verify") {
		t.Fatalf("brief: %q %q", title, in)
	}
}

func TestUsedFactIDsAreReadFromToolOutput(t *testing.T) {
	out := "#12 (user, 2026-01-02) The user likes tea\n#40 (project:Trip, 2026-02-03) [outdated since 2026-03-01] Old plan\nnot a fact line #99 (x)\n"
	if got := UsedFactIDs(out); len(got) != 2 || got[0] != 12 || got[1] != 40 {
		t.Fatalf("find output: %v", got)
	}
	model := "## Tea\nQ: q\nAnswer text\n(from facts [3 8 3], refreshed 2026-09-01)\n"
	if got := UsedFactIDs(model); len(got) != 2 || got[0] != 3 || got[1] != 8 {
		t.Fatalf("model output: %v", got)
	}
}
