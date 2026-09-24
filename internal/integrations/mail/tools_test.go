package mail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"prism/internal/testutil"
	"prism/internal/tools"
)

func setupTools(t *testing.T) (*Service, *imapFixture, *imapFixture, *fakeSMTP) {
	t.Helper()
	d := testutil.DB(t)
	work, personal := startIMAP(t), startIMAP(t)
	smtpd := startSMTP(t)
	st := &Store{DB: d.Pool}
	ctx := context.Background()
	for tag, f := range map[string]*imapFixture{"work": work, "personal": personal} {
		if _, err := st.Save(ctx, Account{Tag: tag, Backend: "imap", Enabled: true, IMAPHost: f.Host, IMAPPort: f.Port, IMAPSecurity: "none", User: "ann", Password: "s3cret",
			SMTPHost: "127.0.0.1", SMTPPort: smtpd.Port, SMTPSecurity: "none", From: "Ann <ann@" + tag + ".example>"}); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{Store: st}, work, personal, smtpd
}

func call(t *testing.T, reg *tools.Registry, name string, args any, env *tools.Env) (string, error) {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	b, _ := json.Marshal(args)
	if env == nil {
		env = &tools.Env{Agent: "Atlas"}
	}
	return tool.Run(context.Background(), env, b)
}

func TestMailToolsAcrossTaggedAccounts(t *testing.T) {
	s, work, personal, smtpd := setupTools(t)
	work.add(t, "INBOX", plainMail, false)   // Mon 21 Sep
	work.add(t, "INBOX", attachMail, false)  // Wed 23 Sep
	personal.add(t, "INBOX", htmlMail, true) // Tue 22 Sep
	personal.add(t, "INBOX", cyrMail, false) // Thu 24 Sep
	reg := tools.NewRegistry(nil)
	RegisterTools(reg, s)
	ctx := context.Background()

	// the tools see tags and addresses, never passwords
	out, err := call(t, reg, "mail_accounts", map[string]any{}, nil)
	if err != nil || !strings.Contains(out, "work: Ann <ann@work.example>") || !strings.Contains(out, "personal:") || strings.Contains(out, "s3cret") {
		t.Fatalf("accounts: %q %v", out, err)
	}
	if list, _ := s.Store.List(ctx); len(list) != 2 || list[0].Password != "" || !list[0].HasPassword {
		t.Fatalf("List must hide secrets: %+v", list)
	}

	// no account → every inbox at once, newest first, each line tagged
	out, err = call(t, reg, "mail_search", map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 4 || !strings.Contains(lines[0], "[personal]") || !strings.Contains(lines[0], "Привет") || !strings.Contains(lines[1], "[work]") || !strings.Contains(lines[1], "Contract") || !strings.Contains(lines[3], "Lunch on Friday?") {
		t.Fatalf("merged search:\n%s", out)
	}
	if out, _ := call(t, reg, "mail_search", map[string]any{"account": "work", "unread": true}, nil); strings.Count(out, "\n") != 1 || strings.Contains(out, "[personal]") {
		t.Fatalf("one account, unread:\n%s", out)
	}
	if out, _ := call(t, reg, "mail_search", map[string]any{"from": "shop.example", "since_days": 3650}, nil); !strings.Contains(out, "Sale") || strings.Count(out, "[") < 1 {
		t.Fatalf("filters: %s", out)
	}
	if _, err := call(t, reg, "mail_search", map[string]any{"account": "nope"}, nil); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown tag: %v", err)
	}

	// reading: content is returned but the message stays unread unless asked
	ids := func(acct, from string) string {
		out, _ := call(t, reg, "mail_search", map[string]any{"account": acct, "from": from}, nil)
		i := strings.Index(out, "id=") + 3
		return strings.Fields(out[i:])[0]
	}
	id := ids("work", "bob@example.org")
	out, err = call(t, reg, "mail_read", map[string]any{"account": "work", "id": id}, nil)
	if err != nil || !strings.Contains(out, "Subject: Lunch on Friday?") || !strings.Contains(out, "Are you free for lunch") {
		t.Fatalf("read: %q %v", out, err)
	}
	if out, _ := call(t, reg, "mail_search", map[string]any{"account": "work", "from": "bob@example.org"}, nil); !strings.Contains(out, "[unread]") {
		t.Fatalf("reading must not mark as read: %s", out)
	}
	_, _ = call(t, reg, "mail_read", map[string]any{"account": "work", "id": id, "mark_read": true}, nil)
	if out, _ := call(t, reg, "mail_search", map[string]any{"account": "work", "from": "bob@example.org"}, nil); strings.Contains(out, "[unread]") {
		t.Fatalf("mark_read: %s", out)
	}
	if out, _ := call(t, reg, "mail_read", map[string]any{"account": "work", "id": ids("work", "carol")}, nil); !strings.Contains(out, "Attachment: contract.pdf") {
		t.Fatalf("attachments listed: %s", out)
	}

	// a reply draft: recipient, subject and threading come from the original; nothing is sent
	out, err = call(t, reg, "mail_draft", map[string]any{"account": "work", "reply_to": id, "body": "Friday works for me."}, nil)
	if err != nil || !strings.Contains(out, "Subject: Re: Lunch on Friday?") || !strings.Contains(out, "bob@example.org") || !strings.Contains(out, "not sent") {
		t.Fatalf("draft: %q %v", out, err)
	}
	smtpd.mu.Lock()
	if smtpd.Data != "" {
		t.Fatal("a draft must not be sent")
	}
	smtpd.mu.Unlock()
	if out, _ := call(t, reg, "mail_search", map[string]any{"account": "work", "folder": "Drafts"}, nil); !strings.Contains(out, "Re: Lunch on Friday?") {
		t.Fatalf("draft folder: %s", out)
	}

	// sending: always asks, shows the message, and only then delivers and files a copy in Sent
	var asked []string
	answer := "deny"
	env := &tools.Env{Agent: "Atlas", Ask: func(ctx context.Context, q tools.Question) (string, error) {
		asked = append(asked, q.Text)
		return answer, nil
	}}
	send := map[string]any{"account": "work", "to": []string{"bob@example.org"}, "subject": "Re: Lunch on Friday?", "body": "See you there.", "reply_to": id}
	if _, err := call(t, reg, "mail_send", send, env); err == nil || !strings.Contains(err.Error(), "did not approve") {
		t.Fatalf("denied send: %v", err)
	}
	smtpd.mu.Lock()
	sentNothing := smtpd.Data == ""
	smtpd.mu.Unlock()
	if !sentNothing || len(asked) != 1 || !strings.Contains(asked[0], "ann@work.example") || !strings.Contains(asked[0], "See you there.") || !strings.Contains(asked[0], "bob@example.org") {
		t.Fatalf("the question must show account, recipient and text: %v", asked)
	}
	answer = "allow"
	out, err = call(t, reg, "mail_send", send, env)
	if err != nil || !strings.Contains(out, "Sent to bob@example.org") {
		t.Fatalf("send: %q %v", out, err)
	}
	smtpd.mu.Lock()
	ok := smtpd.From == "ann@work.example" && strings.Contains(smtpd.Data, "See you there.") && strings.Contains(smtpd.Data, "In-Reply-To: <lunch-1@example.org>")
	smtpd.mu.Unlock()
	if !ok {
		t.Fatalf("delivery: from=%q data=%q", smtpd.From, smtpd.Data)
	}
	if out, _ := call(t, reg, "mail_search", map[string]any{"account": "work", "folder": "Sent"}, nil); !strings.Contains(out, "Re: Lunch on Friday?") {
		t.Fatalf("a copy belongs in Sent: %s", out)
	}
	// nobody to ask → refuse; header injection is refused before the user is even asked
	if _, err := call(t, reg, "mail_send", send, &tools.Env{Agent: "Atlas"}); err == nil {
		t.Fatal("without a way to ask, sending must be refused")
	}
	asked = nil
	bad := map[string]any{"account": "work", "to": []string{"bob@example.org"}, "subject": "hi\r\nBcc: evil@x.com", "body": "x"}
	if _, err := call(t, reg, "mail_send", bad, env); err == nil || len(asked) != 0 {
		t.Fatalf("injection must be refused up front: %v asked=%v", err, asked)
	}
}

func TestAccountStoreRules(t *testing.T) {
	d := testutil.DB(t)
	st := &Store{DB: d.Pool}
	ctx := context.Background()
	base := Account{Tag: "Work", Backend: "imap", Enabled: true, IMAPHost: "imap.example.com", User: "ann@example.com", Password: "pw1", From: "Ann <ann@example.com>"}
	id, err := st.Save(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	// editing with an empty password keeps the stored one; a new one replaces it
	edit := base
	edit.ID, edit.Password, edit.IMAPHost = id, "", "imap2.example.com"
	if _, err := st.Save(ctx, edit); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Get(ctx, "work") // tags are case-insensitive
	if got.Password != "pw1" || got.IMAPHost != "imap2.example.com" || got.IMAPSecurity != "tls" {
		t.Fatalf("%+v", got)
	}
	edit.Password = "pw2"
	_, _ = st.Save(ctx, edit)
	if got, _ = st.Get(ctx, "work"); got.Password != "pw2" {
		t.Fatalf("password change: %+v", got)
	}
	for name, a := range map[string]Account{
		"bad tag":       {Tag: "My Work!", Backend: "imap", IMAPHost: "x", User: "u"},
		"no imap host":  {Tag: "a", Backend: "imap", User: "u"},
		"no himalaya":   {Tag: "b", Backend: "himalaya"},
		"bad backend":   {Tag: "c", Backend: "pigeon"},
		"bad security":  {Tag: "d", Backend: "imap", IMAPHost: "x", User: "u", IMAPSecurity: "ssl3"},
		"bad from":      {Tag: "e", Backend: "imap", IMAPHost: "x", User: "u", From: "not an address"},
		"duplicate tag": {Tag: "work", Backend: "imap", IMAPHost: "x", User: "u"},
	} {
		if _, err := st.Save(ctx, a); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if _, err := st.Save(ctx, Account{Tag: "personal2", Backend: "himalaya", Himalaya: "gmail", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// a switched-off account is invisible to agents
	off, _ := st.Get(ctx, "personal2")
	off.Enabled = false
	_, _ = st.Save(ctx, off)
	svc := &Service{Store: st}
	if _, _, err := svc.open(ctx, "personal2"); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("disabled: %v", err)
	}
	if as, _ := svc.enabled(ctx); len(as) != 1 || as[0].Tag != "work" {
		t.Fatalf("enabled: %+v", as)
	}
}
