package memory

import (
	"context"
	"testing"
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
