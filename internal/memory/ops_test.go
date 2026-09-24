package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUndoMergeAndSplit(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	p1 := store(t, s, StoreReq{Bank: "project:Trip plan", Text: "Flights to Lisbon leave on 3 May"})
	dup1 := store(t, s, StoreReq{Bank: "project:Trip plan", Text: "Budget for the trip is 1500 euros"})
	dup2 := store(t, s, StoreReq{Bank: "project:Trip notes", Text: "Budget for the trip is 1500 euros"})
	n1 := store(t, s, StoreReq{Bank: "project:Trip notes", Text: "Hotel is near Alfama"})
	plan, _ := s.BankBySpec(ctx, "project:Trip plan", "", false)
	notes, _ := s.BankBySpec(ctx, "project:Trip notes", "", false)

	res, err := s.Merge2(ctx, plan.ID, notes.ID, "Lisbon trip")
	if err != nil || res.Dropped != 1 {
		t.Fatalf("merge: %+v %v", res, err)
	}
	ops, _ := s.Ops(ctx)
	if len(ops) != 1 || ops[0].Kind != "merge" || !strings.Contains(ops[0].Summary, "Lisbon trip") {
		t.Fatalf("ops: %+v", ops)
	}
	msg, err := s.Undo(ctx, ops[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(msg)
	if _, err := s.BankBySpec(ctx, "project:Lisbon trip", "", false); err == nil {
		t.Fatal("the merged bank should be gone after undo")
	}
	for spec, want := range map[string][]int64{"project:Trip plan": {p1.ID, dup1.ID}, "project:Trip notes": {n1.ID}} {
		b, err := s.BankBySpec(ctx, spec, "", false)
		if err != nil {
			t.Fatalf("%s did not come back: %v", spec, err)
		}
		fs, _ := s.Facts(ctx, b.ID, "", false, 50, 0)
		got := map[int64]bool{}
		for _, f := range fs {
			got[f.ID] = true
		}
		for _, id := range want {
			if !got[id] {
				t.Errorf("%s lost fact %d", spec, id)
			}
		}
		if spec == "project:Trip notes" && len(fs) != 2 { // the collapsed duplicate is restored as well
			t.Errorf("notes bank should have its duplicate back: %+v", fs)
		}
	}
	if b, _ := s.BankBySpec(ctx, "project:Trip notes", "", false); b.ID != notes.ID {
		t.Errorf("banks come back under their old ids")
	}
	if f, err := s.GetFact(ctx, dup2.ID); err != nil || f.Text != dup2.Text || !f.Embedded {
		t.Fatalf("restored duplicate: %+v %v", f, err)
	}
	if _, err := s.Undo(ctx, ops[0].ID); err == nil {
		t.Fatal("undoing twice")
	}
	if ops, _ = s.Ops(ctx); len(ops) != 0 {
		t.Fatalf("an undone op is not listed: %+v", ops)
	}

	// split, then undo; a bank that already existed stays
	sp, err := s.SplitBank(ctx, plan.ID, "Flights", "air", []int64{p1.ID})
	if err != nil || sp.Moved != 1 {
		t.Fatalf("split: %+v %v", sp, err)
	}
	ops, _ = s.Ops(ctx)
	if len(ops) != 1 || ops[0].Kind != "split" {
		t.Fatalf("ops: %+v", ops)
	}
	if msg, err = s.Undo(ctx, ops[0].ID); err != nil || !strings.Contains(msg, "1 facts moved back") {
		t.Fatalf("undo split: %q %v", msg, err)
	}
	if _, err := s.BankBySpec(ctx, "project:Flights", "", false); err == nil {
		t.Fatal("the bank created by the split should be removed")
	}
	if f, _ := s.GetFact(ctx, p1.ID); f.BankID != plan.ID {
		t.Fatalf("fact not back: %+v", f)
	}
	// into an existing bank: undo keeps that bank
	_, _ = s.EnsureBank(ctx, KindProject, "Existing", "", "")
	store(t, s, StoreReq{Bank: "project:Existing", Text: "Something else entirely"})
	if _, err := s.SplitBank(ctx, plan.ID, "Existing", "", []int64{p1.ID}); err != nil {
		t.Fatal(err)
	}
	ops, _ = s.Ops(ctx)
	if _, err := s.Undo(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BankBySpec(ctx, "project:Existing", "", false); err != nil {
		t.Fatal("a bank that existed before the split must survive its undo")
	}
	// the undo refuses when the world changed under it
	res, _ = s.Merge2(ctx, plan.ID, notes.ID, "Merged")
	_, _ = s.EnsureBank(ctx, KindProject, "Trip plan", "", "")
	ops, _ = s.Ops(ctx)
	if _, err := s.Undo(ctx, ops[0].ID); err == nil || !strings.Contains(err.Error(), "exists again") {
		t.Fatalf("clash: %v", err)
	}
}

// Merge2 merges two banks into a new one (a shorthand for the tests).
func (s *Service) Merge2(ctx context.Context, a, b int64, name string) (*MergeResult, error) {
	return s.MergeBanks(ctx, []int64{a, b}, 0, name)
}

func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	a := store(t, s, StoreReq{Bank: "user", Text: "User drinks green tea in the evening", Tags: []string{"habit"}})
	b := store(t, s, StoreReq{Bank: "project:Party", Text: "Order sencha for the tea party", Origin: "shop-a.com", Confidence: 0.4})
	c := store(t, s, StoreReq{Bank: "domain:Tea", Text: "Sencha is a Japanese green tea"})
	old := store(t, s, StoreReq{Bank: "domain:Tea", Text: "Sencha harvest is in June"})
	if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1`, old.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET supersedes=$2 WHERE id=$1`, c.ID, old.ID)
	if err := s.Link(ctx, a.ID, b.ID, LinkSupports, "same habit", "user", 0.9); err != nil {
		t.Fatal(err)
	}
	if _, err := s.insertConclusion(ctx, mustBank(t, s, "user"), "User likes Japanese green tea", []int64{a.ID, c.ID}, 0.8, nil); err != nil {
		t.Fatal(err)
	}

	d, err := s.Export(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != DumpVersion || len(d.Banks) < 3 || len(d.Facts) != 5 || len(d.Links) < 3 {
		t.Fatalf("dump: banks=%d facts=%d links=%d", len(d.Banks), len(d.Facts), len(d.Links))
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "embedding") {
		t.Fatal("embeddings do not belong in the export")
	}
	if only, _ := s.Export(ctx, []int64{mustBank(t, s, "domain:Tea")}); len(only.Banks) != 1 || len(only.Facts) != 2 || len(only.Links) != 0 {
		t.Fatalf("one bank: %+v", only)
	}

	// importing into the same memory changes nothing but the (already present) links
	var back Dump
	_ = json.Unmarshal(raw, &back)
	r, err := s.Import(ctx, &back)
	if err != nil || r.Facts != 0 || r.Skipped < 4 {
		t.Fatalf("re-import: %+v %v", r, err)
	}

	// wipe and restore
	if _, err := s.db.Exec(ctx, `DELETE FROM memory_banks WHERE kind<>'user'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM memory_facts`); err != nil {
		t.Fatal(err)
	}
	r, err = s.Import(ctx, &back)
	if err != nil || r.Facts != 5 || r.Links < 3 || r.Banks < 3 {
		t.Fatalf("restore: %+v %v", r, err)
	}
	got, _ := s.Export(ctx, nil)
	byText := map[string]DumpFact{}
	for _, f := range got.Facts {
		byText[f.Text] = f
	}
	if len(byText) != 5 {
		t.Fatalf("facts after restore: %d", len(byText))
	}
	restored := byText["Order sencha for the tea party"]
	if len(restored.Origins) != 1 || restored.Origins[0] != "shop-a.com" || restored.Confidence > 0.41 {
		t.Fatalf("provenance lost: %+v", restored)
	}
	oldR, newR := byText["Sencha harvest is in June"], byText["Sencha is a Japanese green tea"]
	if oldR.ValidTo == nil || oldR.SupersededBy == nil || *oldR.SupersededBy != newR.ID || newR.Supersedes == nil || *newR.Supersedes != oldR.ID {
		t.Fatalf("supersede chain lost: %+v / %+v", oldR, newR)
	}
	if k := byText["User likes Japanese green tea"].Kind; k != ConclusionKind {
		t.Fatalf("conclusion kind lost: %s", k)
	}
	if fs, _ := s.Facts(ctx, mustBank(t, s, "user"), "", false, 10, 0); len(fs) == 0 || !fs[0].Embedded {
		t.Fatalf("imported facts are embedded again: %+v", fs)
	}
	// hostile or wrong input
	if _, err := s.Import(ctx, &Dump{Version: 99}); err == nil {
		t.Fatal("unknown version accepted")
	}
	if _, err := s.Import(ctx, &Dump{Version: DumpVersion, Banks: []DumpBank{{ID: 1, Kind: "bogus", Name: "x"}}}); err == nil {
		t.Fatal("unknown bank kind accepted")
	}
	if _, err := s.Import(ctx, nil); err == nil {
		t.Fatal("nil dump accepted")
	}
	r, err = s.Import(ctx, &Dump{Version: DumpVersion, Banks: []DumpBank{{ID: 1, Kind: "user", Name: "ignored"}}, Facts: []DumpFact{{ID: 1, Bank: 1, Text: "   "}, {ID: 2, Bank: 77, Text: "orphan"}}})
	if err != nil || r.Facts != 0 || r.Skipped != 2 {
		t.Fatalf("empty and orphan facts are skipped: %+v %v", r, err)
	}
}

func mustBank(t *testing.T, s *Service, spec string) int64 {
	t.Helper()
	b, err := s.BankBySpec(context.Background(), spec, "", true)
	if err != nil {
		t.Fatal(err)
	}
	return b.ID
}

func TestGraphShowsLinksChainsAndLimits(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t)
	a := store(t, s, StoreReq{Bank: "user", Text: "User drinks green tea in the evening"})
	b := store(t, s, StoreReq{Bank: "project:Party", Text: "Order sencha for the tea party"})
	c := store(t, s, StoreReq{Bank: "domain:Tea", Text: "Sencha is a Japanese green tea"})
	old := store(t, s, StoreReq{Bank: "domain:Tea", Text: "Sencha harvest is in June"})
	lonely := store(t, s, StoreReq{Bank: "domain:Tea", Text: "Completely unrelated fact about volcanoes and lava flows"})
	_, _ = s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now(), superseded_by=$2 WHERE id=$1`, old.ID, c.ID)
	if err := s.Link(ctx, a.ID, b.ID, LinkSupports, "", "user", 0.9); err != nil {
		t.Fatal(err)
	}
	g, err := s.Graph(ctx, 0, true, 0)
	if err != nil || len(g.Nodes) != 5 || g.More != 0 {
		t.Fatalf("graph: %+v %v", g, err)
	}
	kinds := map[string]int{}
	for _, e := range g.Edges {
		kinds[e.Kind]++
	}
	if kinds["supports"] != 1 || kinds["supersedes"] != 1 {
		t.Fatalf("edges: %+v", g.Edges)
	}
	retired := 0
	for _, n := range g.Nodes {
		if n.Retired {
			retired++
		}
	}
	if retired != 1 {
		t.Fatalf("retired nodes: %d", retired)
	}
	// without history the retired fact and its chain edge are left out
	if g, _ = s.Graph(ctx, 0, false, 0); len(g.Nodes) != 4 {
		t.Fatalf("no history: %d nodes", len(g.Nodes))
	}
	for _, e := range g.Edges {
		if e.Kind == "supersedes" {
			t.Fatal("edge to a hidden fact")
		}
	}
	// a limit keeps the linked facts first and reports the rest
	g, _ = s.Graph(ctx, 0, false, 2)
	if len(g.Nodes) != 2 || g.More != 2 || (g.Nodes[0].ID != a.ID && g.Nodes[0].ID != b.ID) {
		t.Fatalf("limit: %+v", g)
	}
	// one bank only
	if g, _ = s.Graph(ctx, mustBank(t, s, "domain:Tea"), true, 0); len(g.Nodes) != 3 || len(g.Edges) != 1 {
		t.Fatalf("bank: %+v", g)
	}
	_ = lonely
}
