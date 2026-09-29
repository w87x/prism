package scheduler

import (
	"context"
	"strings"
	"testing"
)

// The mail predicate mirrors rss: the first check baselines whatever is already in the mailbox (no fire),
// and later checks fire only on genuinely new messages, filtered by from/subject when given.
func TestMailPredicateTracksNewMessagesOnly(t *testing.T) {
	items := []MailItem{{ID: "work:INBOX:1", Subject: "Old one", From: "boss@x.com"}, {ID: "work:INBOX:2", Subject: "Also old", From: "ann@x.com"}}
	calls := 0
	env := Env{MailNew: func(ctx context.Context, account, folder string) ([]MailItem, error) {
		calls++
		if account != "work" || folder != "INBOX" {
			t.Fatalf("account/folder not passed through: %q %q", account, folder)
		}
		return items, nil
	}}
	p := &Predicate{Kind: "mail", Account: "work", Folder: "INBOX"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}

	r, err := p.Eval(context.Background(), env)
	if err != nil || r.Fired || !strings.Contains(r.Progress, "baseline: 2") {
		t.Fatalf("baseline check: %+v %v", r, err)
	}

	r, err = p.Eval(context.Background(), env)
	if err != nil || r.Fired || !strings.Contains(r.Progress, "no new mail") {
		t.Fatalf("no-change check: %+v %v", r, err)
	}

	items = append(items, MailItem{ID: "work:INBOX:3", Subject: "Invoice due", From: "billing@vendor.com"})
	r, err = p.Eval(context.Background(), env)
	if err != nil || !r.Fired || !strings.Contains(r.Evidence, "Invoice due") || strings.Contains(r.Evidence, "Old one") {
		t.Fatalf("new-mail check: %+v %v", r, err)
	}

	// does not re-fire on the same message next time
	r, err = p.Eval(context.Background(), env)
	if err != nil || r.Fired {
		t.Fatalf("must not re-fire on an already-seen message: %+v %v", r, err)
	}
	if calls != 4 {
		t.Fatalf("calls = %d", calls)
	}
}

// from/subject filter which new arrivals actually fire the intent, without affecting what counts as "seen".
func TestMailPredicateFiltersBySenderAndSubject(t *testing.T) {
	items := []MailItem{}
	env := Env{MailNew: func(ctx context.Context, account, folder string) ([]MailItem, error) { return items, nil }}
	p := &Predicate{Kind: "mail", From: "boss@"}
	if _, err := p.Eval(context.Background(), env); err != nil {
		t.Fatal(err) // baseline: empty inbox
	}

	items = []MailItem{{ID: "1", Subject: "lunch?", From: "ann@x.com"}}
	r, err := p.Eval(context.Background(), env)
	if err != nil || r.Fired {
		t.Fatalf("a non-matching sender must not fire: %+v %v", r, err)
	}

	items = append(items, MailItem{ID: "2", Subject: "re: deadline", From: "boss@x.com"})
	r, err = p.Eval(context.Background(), env)
	if err != nil || !r.Fired || !strings.Contains(r.Evidence, "deadline") || strings.Contains(r.Evidence, "lunch") {
		t.Fatalf("matching sender should fire, and only for itself: %+v %v", r, err)
	}
}

func TestMailPredicateUnavailableWithoutEnv(t *testing.T) {
	p := &Predicate{Kind: "mail"}
	if _, err := p.Eval(context.Background(), Env{}); err == nil {
		t.Fatal("expected an error when mail is unavailable")
	}
}
