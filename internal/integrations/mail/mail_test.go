package mail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

type lit struct{ *bytes.Reader }

func (l lit) Size() int64 { return int64(l.Reader.Len()) }

// imapFixture is an in-process IMAP server with one user and an INBOX/Drafts pair.
type imapFixture struct {
	Host string
	Port int
	User *imapmemserver.User
}

func startIMAP(t *testing.T) *imapFixture {
	t.Helper()
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("ann", "s3cret")
	for _, m := range []string{"INBOX", "Drafts", "Sent"} {
		if err := u.Create(m, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(u)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapListStatus: {}, imap.CapESearch: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &imapFixture{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: u}
}

func (f *imapFixture) add(t *testing.T, folder, raw string, seen bool) {
	t.Helper()
	var flags []imap.Flag
	if seen {
		flags = append(flags, imap.FlagSeen)
	}
	if _, err := f.User.Append(folder, lit{bytes.NewReader([]byte(strings.ReplaceAll(raw, "\n", "\r\n")))}, &imap.AppendOptions{Flags: flags, Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func (f *imapFixture) backend(smtpPort int) *IMAP {
	return &IMAP{A: Account{Tag: "work", Backend: "imap", IMAPHost: f.Host, IMAPPort: f.Port, IMAPSecurity: "none", User: "ann", Password: "s3cret",
		SMTPHost: "127.0.0.1", SMTPPort: smtpPort, SMTPSecurity: "none", From: "Ann <ann@example.com>"}, Timeout: 10 * time.Second}
}

const (
	plainMail = `From: Bob Builder <bob@example.org>
To: ann@example.com
Subject: Lunch on Friday?
Date: Mon, 21 Sep 2026 09:00:00 +0000
Message-ID: <lunch-1@example.org>
Content-Type: text/plain; charset=utf-8

Are you free for lunch on Friday? Let me know.
`
	htmlMail = `From: "Shop" <news@shop.example>
To: ann@example.com
Subject: =?utf-8?q?Sale_=E2=80=94_50%_off?=
Date: Tue, 22 Sep 2026 10:00:00 +0000
Message-ID: <sale-1@shop.example>
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="B"

--B
Content-Type: text/html; charset=utf-8

<html><head><style>p{color:red}</style></head><body><h1>Big sale</h1><p>Everything is <b>50% off</b>. <a href="https://shop.example/deals">See deals</a></p><script>steal()</script></body></html>
--B--
`
	attachMail = `From: carol@example.net
To: ann@example.com
Subject: Contract
Date: Wed, 23 Sep 2026 11:00:00 +0000
Message-ID: <contract-1@example.net>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="M"

--M
Content-Type: text/plain; charset=utf-8

Please sign and return.
--M
Content-Type: application/pdf; name="contract.pdf"
Content-Disposition: attachment; filename="contract.pdf"
Content-Transfer-Encoding: base64

JVBERi0xLjQKJSUlIGZha2UgcGRmIGJ5dGVzCg==
--M--
`
	// "Привет, как дела?" in windows-1251, base64
	cyrMail = `From: =?windows-1251?B?yOLg7Q==?= <ivan@example.ru>
To: ann@example.com
Subject: =?windows-1251?B?z/Do4uXy?=
Date: Thu, 24 Sep 2026 12:00:00 +0000
Message-ID: <cyr-1@example.ru>
MIME-Version: 1.0
Content-Type: text/plain; charset=windows-1251
Content-Transfer-Encoding: base64

z/Do4uXyLCDq4Oog5OXr4D8=
`
)

func TestNativeIMAPSearchReadAndFlags(t *testing.T) {
	f := startIMAP(t)
	f.add(t, "INBOX", plainMail, false)
	f.add(t, "INBOX", htmlMail, true)
	f.add(t, "INBOX", attachMail, false)
	f.add(t, "INBOX", cyrMail, false)
	b := f.backend(0)
	ctx := context.Background()

	folders, err := b.Folders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]Folder{}
	for _, fo := range folders {
		names[fo.Name] = fo
	}
	if names["INBOX"].Role != "inbox" || names["INBOX"].Total != 4 || names["INBOX"].Unread != 3 || names["Drafts"].Name == "" {
		t.Fatalf("folders: %+v", folders)
	}

	all, err := b.Search(ctx, Query{})
	if err != nil || len(all) != 4 || all[0].Subject == "" {
		t.Fatalf("search all: %d %v", len(all), err)
	}
	if all[0].ID < all[1].ID && len(all[0].ID) == len(all[1].ID) {
		t.Fatalf("newest must come first: %s before %s", all[0].ID, all[1].ID)
	}
	byFrom, _ := b.Search(ctx, Query{From: "bob@example.org"})
	if len(byFrom) != 1 || byFrom[0].Subject != "Lunch on Friday?" || !byFrom[0].Unread || byFrom[0].From[0].Name != "Bob Builder" {
		t.Fatalf("by sender: %+v", byFrom)
	}
	unread, _ := b.Search(ctx, Query{Unread: true})
	if len(unread) != 3 {
		t.Fatalf("unread: %d", len(unread))
	}
	if lim, _ := b.Search(ctx, Query{Limit: 2}); len(lim) != 2 {
		t.Fatalf("limit: %d", len(lim))
	}
	if subj, _ := b.Search(ctx, Query{Subject: "contract"}); len(subj) != 1 {
		t.Fatalf("subject: %d", len(subj))
	}
	if none, err := b.Search(ctx, Query{From: "nobody@nowhere"}); err != nil || len(none) != 0 {
		t.Fatalf("no match: %v %v", none, err)
	}

	// reading: plain, HTML-only (flattened, scripts dropped), attachments, and a Cyrillic message
	raw, err := b.Fetch(ctx, "INBOX", byFrom[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil || !strings.Contains(m.Text, "Are you free for lunch on Friday?") || m.MessageID != "lunch-1@example.org" {
		t.Fatalf("plain: %+v %v", m, err)
	}
	// reading must not mark the message as read
	if again, _ := b.Search(ctx, Query{From: "bob@example.org"}); !again[0].Unread {
		t.Fatal("fetching a message must leave it unread")
	}
	sale, _ := b.Search(ctx, Query{From: "shop.example"})
	raw, _ = b.Fetch(ctx, "INBOX", sale[0].ID)
	hm, _ := Parse(raw)
	if hm.Subject != "Sale — 50% off" || !strings.Contains(hm.Text, "Everything is 50% off. See deals <https://shop.example/deals>") || strings.Contains(hm.Text, "steal") || strings.Contains(hm.Text, "color:red") {
		t.Fatalf("html: %q / %q", hm.Subject, hm.Text)
	}
	con, _ := b.Search(ctx, Query{Subject: "Contract"})
	raw, _ = b.Fetch(ctx, "INBOX", con[0].ID)
	cm, _ := Parse(raw)
	if len(cm.Attachments) != 1 || cm.Attachments[0].Name != "contract.pdf" || cm.Attachments[0].MIME != "application/pdf" || !cm.HasAttach || !strings.Contains(cm.Text, "Please sign") {
		t.Fatalf("attachment: %+v", cm)
	}
	ru, _ := b.Search(ctx, Query{From: "ivan@example.ru"})
	raw, _ = b.Fetch(ctx, "INBOX", ru[0].ID)
	rm, _ := Parse(raw)
	if rm.Subject != "Привет" || !strings.Contains(rm.Text, "Привет, как дела?") || rm.From[0].Name != "Иван" {
		t.Fatalf("cyrillic: %q %q %+v", rm.Subject, rm.Text, rm.From)
	}

	// mark read / unread
	if err := b.SetSeen(ctx, "INBOX", byFrom[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if after, _ := b.Search(ctx, Query{From: "bob@example.org"}); after[0].Unread {
		t.Fatal("should now be read")
	}
	_ = b.SetSeen(ctx, "INBOX", byFrom[0].ID, false)
	if after, _ := b.Search(ctx, Query{From: "bob@example.org"}); !after[0].Unread {
		t.Fatal("should be unread again")
	}

	// errors are readable
	if _, err := b.Search(ctx, Query{Folder: "Nope"}); err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("unknown folder: %v", err)
	}
	if _, err := b.Fetch(ctx, "INBOX", "abc"); err == nil || !strings.Contains(err.Error(), "mail_search") {
		t.Fatalf("bad id: %v", err)
	}
	bad := f.backend(0)
	bad.A.Password = "wrong"
	if _, err := bad.Folders(ctx); err == nil || !strings.Contains(err.Error(), "refused the login") {
		t.Fatalf("wrong password: %v", err)
	}
	dead := f.backend(0)
	dead.A.IMAPPort = 1
	if _, err := dead.Folders(ctx); err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestDraftsAreStoredNotSent(t *testing.T) {
	f := startIMAP(t)
	b := f.backend(0)
	ctx := context.Background()
	raw, err := Compose(Draft{From: b.A.From, To: []string{"bob@example.org"}, Subject: "Ужин / Dinner", Body: "Привет!\nSee you at 19:00.", InReplyTo: "lunch-1@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Append(ctx, "Drafts", raw, "\\Draft", "\\Seen"); err != nil {
		t.Fatal(err)
	}
	got, err := b.Search(ctx, Query{Folder: "Drafts"})
	if err != nil || len(got) != 1 || got[0].Subject != "Ужин / Dinner" {
		t.Fatalf("draft not found: %+v %v", got, err)
	}
	stored, _ := b.Fetch(ctx, "Drafts", got[0].ID)
	m, _ := Parse(stored)
	if !strings.Contains(m.Text, "Привет!") || m.InReplyTo != "<lunch-1@example.org>" || m.To[0].Email != "bob@example.org" {
		t.Fatalf("round trip: %+v", m)
	}
}

func TestComposeRefusesHeaderInjectionAndBadAddresses(t *testing.T) {
	from := "Ann <ann@example.com>"
	ok := Draft{From: from, To: []string{"bob@example.org"}, Subject: "hi", Body: "x"}
	if _, err := Compose(ok); err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]Draft{
		"newline in subject": {From: from, To: []string{"bob@example.org"}, Subject: "hi\r\nBcc: evil@x.com", Body: "x"},
		"newline in to":      {From: from, To: []string{"bob@example.org\r\nBcc: evil@x.com"}, Subject: "hi", Body: "x"},
		"not an address":     {From: from, To: []string{"not an address"}, Subject: "hi", Body: "x"},
		"bad from":           {From: "", To: []string{"bob@example.org"}, Subject: "hi", Body: "x"},
		"newline in cc":      {From: from, To: []string{"bob@example.org"}, Cc: []string{"a@b.c\nBcc: x@y.z"}, Subject: "hi", Body: "x"},
	} {
		if raw, err := Compose(d); err == nil {
			t.Errorf("%s must be refused; got:\n%s", name, raw)
		}
	}
	// a body starting with header-looking text stays a body
	raw, _ := Compose(Draft{From: from, To: []string{"bob@example.org"}, Subject: "s", Body: "Bcc: evil@x.com\n\nreal text"})
	m, _ := Parse(raw)
	if len(m.To) != 1 || !strings.Contains(m.Text, "Bcc: evil@x.com") {
		t.Fatalf("%+v", m)
	}
	if rc, err := Recipients(Draft{To: []string{"Bob <bob@example.org>"}, Cc: []string{"carol@example.net"}}); err != nil || len(rc) != 2 {
		t.Fatalf("recipients: %v %v", rc, err)
	}
	if _, err := Recipients(Draft{}); err == nil {
		t.Fatal("no recipients")
	}
}

// fakeSMTP records one delivery.
type fakeSMTP struct {
	mu     sync.Mutex
	Port   int
	From   string
	Rcpts  []string
	Data   string
	AuthOK string
	Refuse string // recipient to refuse
}

func startSMTP(t *testing.T) *fakeSMTP {
	f := &fakeSMTP{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.Port = ln.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				say := func(s string) { fmt.Fprintf(c, "%s\r\n", s) }
				say("220 fake ESMTP")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"):
						say("250-fake")
						say("250 AUTH PLAIN")
					case strings.HasPrefix(cmd, "AUTH PLAIN"):
						parts := strings.Fields(line)
						b, _ := base64.StdEncoding.DecodeString(parts[len(parts)-1])
						f.mu.Lock()
						f.AuthOK = strings.ReplaceAll(string(b), "\x00", "|")
						f.mu.Unlock()
						say("235 ok")
					case strings.HasPrefix(cmd, "MAIL FROM:"):
						f.mu.Lock()
						f.From = strings.Trim(strings.TrimSpace(line[10:]), "<>")
						f.mu.Unlock()
						say("250 ok")
					case strings.HasPrefix(cmd, "RCPT TO:"):
						rc := strings.Trim(strings.TrimSpace(line[8:]), "<>")
						if f.Refuse != "" && rc == f.Refuse {
							say("550 no such user")
							continue
						}
						f.mu.Lock()
						f.Rcpts = append(f.Rcpts, rc)
						f.mu.Unlock()
						say("250 ok")
					case cmd == "DATA":
						say("354 go")
						var sb strings.Builder
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
							sb.WriteString(l)
						}
						f.mu.Lock()
						f.Data = sb.String()
						f.mu.Unlock()
						say("250 queued")
					case cmd == "QUIT":
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}()
		}
	}()
	return f
}

func TestSMTPDelivery(t *testing.T) {
	smtpd := startSMTP(t)
	f := startIMAP(t)
	b := f.backend(smtpd.Port)
	ctx := context.Background()
	raw, _ := Compose(Draft{From: b.A.From, To: []string{"bob@example.org"}, Cc: []string{"carol@example.net"}, Subject: "Report", Body: "Attached soon."})
	rcpts, _ := Recipients(Draft{To: []string{"bob@example.org"}, Cc: []string{"carol@example.net"}})
	if err := b.Send(ctx, "ann@example.com", rcpts, raw); err != nil {
		t.Fatal(err)
	}
	smtpd.mu.Lock()
	defer smtpd.mu.Unlock()
	if smtpd.From != "ann@example.com" || strings.Join(smtpd.Rcpts, ",") != "bob@example.org,carol@example.net" || !strings.Contains(smtpd.Data, "Subject: Report") || smtpd.AuthOK != "|ann|s3cret" {
		t.Fatalf("delivery: from=%q rcpts=%v auth=%q data=%q", smtpd.From, smtpd.Rcpts, smtpd.AuthOK, smtpd.Data)
	}
	if strings.Contains(smtpd.Data, "\r\n.\r\n") {
		t.Fatal("the terminator must not be part of the data")
	}

	// a refused recipient is reported by name, and no port means a clear error
	smtpd.Refuse = "bob@example.org"
	smtpd.mu.Unlock()
	err := b.Send(ctx, "ann@example.com", []string{"bob@example.org"}, raw)
	smtpd.mu.Lock()
	if err == nil || !strings.Contains(err.Error(), "bob@example.org") {
		t.Fatalf("refused recipient: %v", err)
	}
	none := f.backend(0)
	none.A.SMTPHost = ""
	if err := none.Send(ctx, "ann@example.com", []string{"x@y.z"}, raw); err == nil || !strings.Contains(err.Error(), "no SMTP server") {
		t.Fatalf("no smtp: %v", err)
	}
}
