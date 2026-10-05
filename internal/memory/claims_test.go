package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLifecycleStatusConfirmAndAudit(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	a, err := s.Store(ctx, StoreReq{Bank: "user", Text: "The user's NAS is a Synology DS923", Source: "user", Agent: "Atlas"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Fact.Status != StatusActive || a.Fact.Confirmation != ConfirmNone {
		t.Fatalf("new fact must be active/unconfirmed: %+v", a.Fact)
	}

	// user confirmation: trusted, slower forgetting, reversible
	if err := s.ConfirmByUser(ctx, "user", a.Fact.ID); err != nil {
		t.Fatal(err)
	}
	f, _ := s.GetFact(ctx, a.Fact.ID)
	if f.Confirmation != ConfirmUser || f.Confidence < 0.949 {
		t.Fatalf("confirm: %+v", f)
	}
	if err := s.Unconfirm(ctx, "user", a.Fact.ID); err != nil {
		t.Fatal(err)
	}
	if f, _ = s.GetFact(ctx, a.Fact.ID); f.Confirmation != ConfirmNone {
		t.Fatalf("unconfirm: %+v", f)
	}

	// contested is derived from a live contradiction and clears when the conflict is resolved
	b, _ := s.Store(ctx, StoreReq{Bank: "user", Text: "The user keeps backups on a Backblaze B2 bucket", Source: "user"})
	if err := s.Link(ctx, a.Fact.ID, b.Fact.ID, LinkContradicts, "test", "user", 0.8); err != nil {
		t.Fatal(err)
	}
	if f, _ = s.GetFact(ctx, a.Fact.ID); f.Status != StatusContested {
		t.Fatalf("a contradicted fact must be contested, got %q", f.Status)
	}
	if err := s.Retract(ctx, "user", b.Fact.ID, "it was the other way round"); err != nil {
		t.Fatal(err)
	}
	if f, _ = s.GetFact(ctx, a.Fact.ID); f.Status != StatusActive {
		t.Fatalf("contest must clear once the other side is retired, got %q", f.Status)
	}
	if f, _ = s.GetFact(ctx, b.Fact.ID); f.Status != StatusRetracted || f.ValidTo == nil {
		t.Fatalf("retract: %+v", f)
	}
	if err := s.Retract(ctx, "user", b.Fact.ID, "again"); err == nil {
		t.Fatalf("retracting twice must fail")
	}

	// a correction supersedes: status superseded, audited
	c, err := s.UpdateFact(ctx, a.Fact.ID, strp("The user's NAS is a Synology DS1522+"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if old, _ := s.GetFact(ctx, a.Fact.ID); old.Status != StatusSuperseded {
		t.Fatalf("old must be superseded: %q", old.Status)
	}
	log, _ := s.Audit(ctx, a.Fact.ID, 20)
	seen := map[string]bool{}
	for _, e := range log {
		seen[e.Action] = true
	}
	for _, want := range []string{"retain", "confirm", "unconfirm"} {
		if !seen[want] {
			t.Errorf("audit trail of the first fact lacks %q: %+v", want, log)
		}
	}
	if cl, _ := s.Audit(ctx, c.ID, 5); len(cl) == 0 {
		t.Errorf("the corrected fact must have an audit entry")
	}
}

func TestProposedFactsAreNeverRetrieved(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	r, _ := s.Store(ctx, StoreReq{Bank: "user", Text: "The user's router is a Ubiquiti Dream Machine", Source: "user"})
	q := FindReq{Query: "Ubiquiti router", Banks: []string{"user"}, K: 5, NoLinks: true}
	if res, _ := s.Find(ctx, q); !hasID(res, r.Fact.ID) {
		t.Fatalf("active fact must be found")
	}
	if _, err := s.db.Exec(ctx, `UPDATE memory_facts SET status='proposed' WHERE id=$1`, r.Fact.ID); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Find(ctx, q); hasID(res, r.Fact.ID) {
		t.Fatalf("a proposed fact must not be retrieved")
	}
}

func strp(s string) *string { return &s }

func TestEvidenceLedgerCountsIndependentGroups(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	text := "The GMKtec EVO-X2 ships with 128GB of unified memory"
	a, err := s.Store(ctx, StoreReq{Bank: "domain:hw", Text: text, Source: "agent:Scout", Confidence: 0.4, Origin: "gmktec.com", SourceRef: "https://gmktec.com/evo-x2"})
	if err != nil {
		t.Fatal(err)
	}
	id := a.Fact.ID
	ev, _ := s.Evidence(ctx, id)
	if len(ev) != 1 || ev[0].SourceRef != "https://gmktec.com/evo-x2" || ev[0].Group != "gmktec.com" || !ev[0].Supports {
		t.Fatalf("a web-learned fact must start with one supporting entry: %+v", ev)
	}
	if f, _ := s.GetFact(ctx, id); f.Confirmation != ConfirmNone {
		t.Fatalf("one source is not a confirmation: %q", f.Confirmation)
	}
	// the same site again (another page) is the same group
	_ = s.AddEvidence(ctx, Evidence{FactID: id, SourceRef: "https://gmktec.com/specs", Group: "gmktec.com", Supports: true})
	if f, _ := s.GetFact(ctx, id); f.Confirmation != ConfirmNone {
		t.Fatalf("two pages of one site are one group, got %q", f.Confirmation)
	}
	// a second independent site confirms
	if _, err := s.Store(ctx, StoreReq{Bank: "domain:hw", Text: text, Source: "agent:Scout", Confidence: 0.4, Origin: "notebookcheck.net", SourceRef: "https://notebookcheck.net/evo-x2"}); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.GetFact(ctx, id); f.Confirmation != ConfirmMulti {
		t.Fatalf("two independent sites must confirm, got %q", f.Confirmation)
	}
	// an independent refutation takes the confirmation back, and is recorded
	_ = s.AddEvidence(ctx, Evidence{FactID: id, SourceRef: "https://forum.example/thread", Group: "forum.example", Supports: false, Note: "owner reports 64GB"})
	if f, _ := s.GetFact(ctx, id); f.Confirmation != ConfirmNone {
		t.Fatalf("refuting evidence must withdraw multi-source confirmation, got %q", f.Confirmation)
	}
	ev, _ = s.Evidence(ctx, id)
	if len(ev) != 4 {
		t.Fatalf("ledger should hold 4 entries, has %d", len(ev))
	}
	// a user's own confirmation is never downgraded by evidence bookkeeping
	_ = s.ConfirmByUser(ctx, "user", id)
	_ = s.AddEvidence(ctx, Evidence{FactID: id, SourceRef: "https://other.example/x", Group: "other.example", Supports: false})
	if f, _ := s.GetFact(ctx, id); f.Confirmation != ConfirmUser {
		t.Fatalf("user confirmation must survive, got %q", f.Confirmation)
	}
}

func TestProposalsStayOutOfRecallUntilPromoted(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	old, _ := s.Store(ctx, StoreReq{Bank: "user", Text: "The user's phone is an iPhone 15", Source: "user"})
	p, err := s.Store(ctx, StoreReq{Bank: "user", Text: "The user's phone is an iPhone 17 Pro", Source: "agent:Scout", Agent: "Scout", Propose: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Fact.Status != StatusProposed {
		t.Fatalf("status %q", p.Fact.Status)
	}
	if o, _ := s.GetFact(ctx, old.Fact.ID); o.ValidTo != nil {
		t.Fatalf("a proposal must not retire live facts before approval")
	}
	q := FindReq{Query: "user's phone", Banks: []string{"user"}, K: 5, NoLinks: true}
	if res, _ := s.Find(ctx, q); hasID(res, p.Fact.ID) {
		t.Fatalf("a proposal must not be retrieved")
	}
	if list, _ := s.Proposals(ctx, 10); len(list) != 1 || list[0].ID != p.Fact.ID {
		t.Fatalf("proposals list: %+v", list)
	}
	rv, _ := s.Review(ctx, 10)
	if len(rv.Proposed) != 1 {
		t.Fatalf("review must list the proposal: %+v", rv.Proposed)
	}
	if err := s.Promote(ctx, "user", p.Fact.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Find(ctx, q); !hasID(res, p.Fact.ID) {
		t.Fatalf("a promoted fact must be retrieved")
	}
	if err := s.Promote(ctx, "user", p.Fact.ID, ""); err == nil {
		t.Fatalf("promoting twice must fail")
	}
	r2, _ := s.Store(ctx, StoreReq{Bank: "user", Text: "The user's phone is a Nokia 3310", Source: "agent:Scout", Agent: "Scout", Propose: true})
	if err := s.RejectProposal(ctx, "user", r2.Fact.ID, "not true"); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.GetFact(ctx, r2.Fact.ID); f.Status != StatusRetracted {
		t.Fatalf("rejected proposal must be retracted history, got %q", f.Status)
	}
	log, _ := s.Audit(ctx, r2.Fact.ID, 10)
	if len(log) < 2 {
		t.Fatalf("propose + reject must both be audited: %+v", log)
	}
}

func TestFactFlagsSayHowMuchToTrustAFact(t *testing.T) {
	exp := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	gone := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		f    Fact
		want []string
		not  []string
	}{
		"plain trusted":   {Fact{Confidence: 0.8, Status: StatusActive}, nil, []string{"["}},
		"disputed":        {Fact{Confidence: 0.8, Status: StatusContested}, []string{"disputed"}, nil},
		"unverified":      {Fact{Confidence: 0.4, Status: StatusActive}, []string{"unverified"}, nil},
		"user vouches":    {Fact{Confidence: 0.95, Confirmation: ConfirmUser}, []string{"confirmed by the user"}, []string{"unverified"}},
		"independent":     {Fact{Confidence: 0.7, Confirmation: ConfirmMulti}, []string{"independent sources"}, nil},
		"volatile":        {Fact{Confidence: 0.7, ExpiresAt: &exp}, []string{"may go stale after 2026-11-01"}, nil},
		"replaced":        {Fact{Confidence: 0.7, Status: StatusSuperseded, ValidTo: &gone}, []string{"replaced since 2026-09-01"}, nil},
		"expired":         {Fact{Confidence: 0.7, Status: StatusExpired, ValidTo: &gone}, []string{"expired since 2026-09-01"}, []string{"may go stale"}},
		"retracted":       {Fact{Confidence: 0.7, Status: StatusRetracted, ValidTo: &gone}, []string{"outdated since"}, nil},
		"waiting approve": {Fact{Confidence: 0.7, Status: StatusProposed}, []string{"not yet approved"}, nil},
	} {
		got := FactFlags(c.f)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", name, got, w)
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("%s: %q must not contain %q", name, got, w)
			}
		}
	}
}

func TestBundleFactsMergesOnlyComparableStatements(t *testing.T) {
	mk := func(id int64, bank, obj string, conf float64) Fact {
		return Fact{ID: id, Bank: bank, Text: "The user owns " + obj, Subject: "the user", Predicate: "owns", Object: obj, Confidence: conf, Status: StatusActive}
	}
	facts := []Fact{
		mk(1, "user", "a Synology DS923", 0.9),
		{ID: 2, Bank: "user", Text: "The user dislikes cilantro", Confidence: 0.9}, // free text stays alone
		mk(3, "user", "a Mac Studio", 0.9),
		mk(4, "user", "an iPhone 17", 0.9),
		mk(5, "user", "a drone", 0.4),        // unverified: its own line
		mk(6, "project:home", "a rack", 0.9), // other bank: its own line
	}
	got := BundleFacts(facts)
	if len(got) != 4 {
		t.Fatalf("want 4 lines, got %d: %+v", len(got), got)
	}
	line := got[0].Line()
	if !strings.Contains(line, "facts #1 #3 #4") || !strings.Contains(line, "the user owns a Synology DS923, a Mac Studio and an iPhone 17") {
		t.Fatalf("bundle line: %q", line)
	}
	if one := got[1].Line(); !strings.Contains(one, "[fact #2 ·") || !strings.Contains(one, "cilantro") {
		t.Fatalf("free text must render as before: %q", one)
	}
	if !strings.Contains(got[2].Line(), "unverified") {
		t.Fatalf("a weak fact must not hide in a strong one's bundle: %q", got[2].Line())
	}
}

func TestStructuredStatementPersistsAndSurvivesExport(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	r, err := s.Store(ctx, StoreReq{Bank: "user", Text: "The user owns a Synology DS923", Source: "user",
		Subject: "the user", Predicate: "owns", Object: "Synology DS923", Qualifiers: map[string]string{"since": "2024"}})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := s.GetFact(ctx, r.Fact.ID)
	if f.Subject != "the user" || f.Predicate != "owns" || f.Object != "Synology DS923" || f.Qualifiers["since"] != "2024" {
		t.Fatalf("structure lost: %+v", f)
	}
	// an incomplete statement stays free text
	r2, _ := s.Store(ctx, StoreReq{Bank: "user", Text: "The user likes tea", Source: "user", Subject: "the user", Predicate: "likes"})
	if g, _ := s.GetFact(ctx, r2.Fact.ID); g.Subject != "" {
		t.Fatalf("subject without object must not be stored: %+v", g)
	}
	// lifecycle and structure survive an export / wipe / import
	_ = s.ConfirmByUser(ctx, "user", r.Fact.ID)
	d, _ := s.Export(ctx, nil)
	raw, _ := json.Marshal(d)
	var back Dump
	_ = json.Unmarshal(raw, &back)
	if _, err := s.db.Exec(ctx, `DELETE FROM memory_facts`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, &back); err != nil {
		t.Fatal(err)
	}
	fs, _ := s.Facts(ctx, 0, "Synology", false, 10, 0)
	if len(fs) != 1 || fs[0].Confirmation != ConfirmUser || fs[0].Predicate != "owns" || fs[0].Status != StatusActive {
		t.Fatalf("after import: %+v", fs)
	}
}

func TestSharedFactsAreVisibleInEveryBankWithoutCopies(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	r, err := s.Store(ctx, StoreReq{Bank: "domain:Physics", Text: "Fire is hot enough to ignite paper", Source: "user"})
	if err != nil {
		t.Fatal(err)
	}
	id := r.Fact.ID
	q := FindReq{Query: "fire ignite paper", Banks: []string{"domain:Cooking"}, K: 5, NoLinks: true}
	if _, err := s.EnsureBank(ctx, "domain", "Cooking", "", ""); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Find(ctx, q); hasID(res, id) {
		t.Fatalf("not shared yet: must not be found in Cooking")
	}
	if err := s.ShareFact(ctx, "Oneiros", id, "domain:Cooking"); err != nil {
		t.Fatal(err)
	}
	if err := s.ShareFact(ctx, "Oneiros", id, "domain:Cooking"); err == nil {
		t.Fatalf("sharing twice must fail")
	}
	if err := s.ShareFact(ctx, "Oneiros", id, "domain:Physics"); err == nil {
		t.Fatalf("sharing into the home bank must fail")
	}
	if res, _ := s.Find(ctx, q); !hasID(res, id) {
		t.Fatalf("a shared fact must be found through the other bank")
	}
	cook, _ := s.BankBySpec(ctx, "domain:Cooking", "", false)
	if fs, _ := s.Facts(ctx, cook.ID, "", false, 10, 0); len(fs) != 1 || fs[0].ID != id || len(fs[0].AlsoIn) != 1 {
		t.Fatalf("listing the other bank: %+v", fs)
	}
	home, _ := s.GetFact(ctx, id)
	if home.Bank != "domain:Physics" || len(home.AlsoIn) != 1 || home.AlsoIn[0] != "domain:Cooking" {
		t.Fatalf("home bank / also_in: %+v", home)
	}
	var n int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM memory_facts WHERE text LIKE 'Fire is hot%'`).Scan(&n)
	if n != 1 {
		t.Fatalf("sharing must not copy the fact, found %d rows", n)
	}
	if err := s.UnshareFact(ctx, "Oneiros", id, "domain:Cooking"); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Find(ctx, q); hasID(res, id) {
		t.Fatalf("after unshare it must be gone from Cooking")
	}
	if err := s.UnshareFact(ctx, "Oneiros", id, "domain:Physics"); err == nil {
		t.Fatalf("the home bank cannot be detached")
	}
	// moving a fact into a bank it was shared into makes that its home and drops the shelf entry
	_ = s.ShareFact(ctx, "Oneiros", id, "domain:Cooking")
	if err := s.MoveFact(ctx, id, cook.ID); err != nil {
		t.Fatal(err)
	}
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM memory_fact_banks WHERE fact_id=$1`, id).Scan(&n)
	if n != 0 {
		t.Fatalf("move must clear the membership, %d left", n)
	}
}
