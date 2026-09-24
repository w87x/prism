package mail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs the real himalaya CLI against the in-process IMAP/SMTP servers, so the wrapper is checked against the
// actual command line and JSON output rather than a guess of them. Skipped when himalaya is not installed.
func TestHimalayaBackendAgainstRealCLI(t *testing.T) {
	if !HimalayaAvailable() {
		t.Skip("himalaya is not installed")
	}
	f := startIMAP(t)
	smtpd := startSMTP(t)
	f.add(t, "INBOX", plainMail, false)
	f.add(t, "INBOX", htmlMail, true)
	f.add(t, "INBOX", attachMail, false)
	cfg := filepath.Join(t.TempDir(), "himalaya.toml")
	toml := fmt.Sprintf(`[accounts.work]
default = true

[accounts.work.imap]
server = "imap://127.0.0.1:%d"

[accounts.work.imap.sasl.plain]
authcid = "ann"
passwd.raw = "s3cret"

[accounts.work.smtp]
server = "smtp://127.0.0.1:%d"

[accounts.work.smtp.sasl.plain]
authcid = "ann"
passwd.raw = "s3cret"
`, f.Port, smtpd.Port)
	if err := os.WriteFile(cfg, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Himalaya{Account: "work", Config: cfg, Timeout: 30 * time.Second}
	ctx := context.Background()

	folders, err := h.Folders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var inbox Folder
	for _, fo := range folders {
		if fo.Role == "inbox" {
			inbox = fo
		}
	}
	if inbox.Name == "" || inbox.Total != 3 || inbox.Unread != 2 {
		t.Fatalf("folders: %+v", folders)
	}

	all, err := h.Search(ctx, Query{})
	if err != nil || len(all) != 3 {
		t.Fatalf("list: %d %v", len(all), err)
	}
	// search DSL: a sender, a subject with spaces, unread only
	bob, err := h.Search(ctx, Query{From: "bob@example.org"})
	if err != nil || len(bob) != 1 || bob[0].Subject != "Lunch on Friday?" || !bob[0].Unread {
		t.Fatalf("from: %+v %v", bob, err)
	}
	if lunch, err := h.Search(ctx, Query{Subject: "Lunch on Friday"}); err != nil || len(lunch) != 1 {
		t.Fatalf("subject with spaces: %+v %v", lunch, err)
	}
	if un, err := h.Search(ctx, Query{Unread: true}); err != nil || len(un) != 2 {
		t.Fatalf("unread: %d %v", len(un), err)
	}
	if both, err := h.Search(ctx, Query{Unread: true, From: "carol@example.net"}); err != nil || len(both) != 1 || both[0].Subject != "Contract" {
		t.Fatalf("combined: %+v %v", both, err)
	}

	raw, err := h.Fetch(ctx, "INBOX", bob[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil || !strings.Contains(m.Text, "Are you free for lunch on Friday?") {
		t.Fatalf("read: %+v %v", m, err)
	}

	if err := h.SetSeen(ctx, "INBOX", bob[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if after, _ := h.Search(ctx, Query{From: "bob@example.org"}); len(after) != 1 || after[0].Unread {
		t.Fatalf("mark seen: %+v", after)
	}

	draft, _ := Compose(Draft{From: "Ann <ann@example.com>", To: []string{"bob@example.org"}, Subject: "Re: Lunch", Body: "Friday works!"})
	if err := h.Append(ctx, "Drafts", draft, "\\Draft"); err != nil {
		t.Fatal(err)
	}
	if d, err := h.Search(ctx, Query{Folder: "Drafts"}); err != nil || len(d) != 1 || d[0].Subject != "Re: Lunch" {
		t.Fatalf("draft: %+v %v", d, err)
	}

	if err := h.Send(ctx, "ann@example.com", []string{"bob@example.org"}, draft); err != nil {
		t.Fatal(err)
	}
	smtpd.mu.Lock()
	defer smtpd.mu.Unlock()
	if !strings.Contains(smtpd.From, "ann@example.com") || len(smtpd.Rcpts) != 1 || smtpd.Rcpts[0] != "bob@example.org" || !strings.Contains(smtpd.Data, "Friday works!") {
		t.Fatalf("send: from=%q rcpts=%v", smtpd.From, smtpd.Rcpts)
	}

	// failures read as sentences
	bad := &Himalaya{Account: "nope", Config: cfg}
	if _, err := bad.Folders(ctx); err == nil {
		t.Fatal("an unknown himalaya account must be an error")
	}
	if _, err := h.Fetch(ctx, "INBOX", "--raw"); err == nil {
		t.Fatal("option-looking ids must be refused")
	}
}

func TestHimalayaQueryTranslation(t *testing.T) {
	q := himalayaQuery(Query{From: "bob", Subject: "hello world", Unread: true, Since: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)})
	got := strings.Join(q, "|")
	want := "from|bob|and|subject|hello|and|subject|world|and|not|flag|seen|and|after|2026-09-20"
	if got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
	if got := strings.Join(himalayaQuery(Query{Subject: "cats and (dogs) or birds"}), "|"); got != "subject|cats|and|subject|dogs|and|subject|birds" {
		t.Fatalf("keywords must not leak into patterns: %s", got)
	}
	if len(himalayaQuery(Query{})) != 0 {
		t.Fatal("an empty query has no conditions")
	}
}
