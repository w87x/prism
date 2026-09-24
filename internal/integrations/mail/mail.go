// Package mail gives agents mail access across several accounts, each tagged ("work", "personal"…).
// Two backends do the transport: PRISM's own IMAP/SMTP client (tunable per account) and the himalaya CLI
// (which brings OAuth and every server quirk it already handles). Both return raw RFC 5322 messages, and
// one MIME layer parses and composes them, so what an agent sees does not depend on the backend.
//
// Mail is the classic route for prompt injection: everything read here is untrusted, and sending always
// asks the user.
package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // decode windows-1251, koi8-r, iso-8859-x… bodies and headers
	gomail "github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
)

// Account is one mailbox the user connected.
type Account struct {
	ID      int64  `json:"id"`
	Tag     string `json:"tag"`     // what agents call it: work, personal…
	Backend string `json:"backend"` // "imap" (PRISM's own client) or "himalaya"
	Enabled bool   `json:"enabled"`
	// himalaya backend: the account name in himalaya's own config.
	Himalaya string `json:"himalaya"`
	// imap backend
	IMAPHost     string `json:"imap_host"`
	IMAPPort     int    `json:"imap_port"`
	IMAPSecurity string `json:"imap_security"` // tls | starttls | none
	User         string `json:"user"`
	Password     string `json:"password,omitempty"` // never sent to the UI; empty on save keeps the stored one
	HasPassword  bool   `json:"has_password"`
	// sending (imap backend; himalaya uses its own config)
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPSecurity string `json:"smtp_security"`
	SMTPUser     string `json:"smtp_user"`               // empty → same as User
	SMTPPassword string `json:"smtp_password,omitempty"` // empty → same as Password
	From         string `json:"from"`                    // the address mail is sent as: "Ann <ann@example.com>"
	// folders; empty → detected (special-use) or the common names
	Inbox  string `json:"inbox"`
	Drafts string `json:"drafts"`
	Sent   string `json:"sent"`
}

type Addr struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

func (a Addr) String() string {
	if a.Name != "" {
		return fmt.Sprintf("%s <%s>", a.Name, a.Email)
	}
	return a.Email
}

func addrList(as []Addr) string {
	var s []string
	for _, a := range as {
		s = append(s, a.String())
	}
	return strings.Join(s, ", ")
}

// Envelope is the list view of a message.
type Envelope struct {
	ID        string    `json:"id"` // backend id (IMAP UID), stable within a folder
	Folder    string    `json:"folder"`
	From      []Addr    `json:"from"`
	To        []Addr    `json:"to"`
	Subject   string    `json:"subject"`
	Date      time.Time `json:"date"`
	Unread    bool      `json:"unread"`
	Flagged   bool      `json:"flagged"`
	MessageID string    `json:"message_id"`
	HasAttach bool      `json:"has_attachment"`
}

type Attachment struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Size int    `json:"size"`
}

// Message is a fully read message.
type Message struct {
	Envelope
	Cc          []Addr       `json:"cc,omitempty"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	InReplyTo   string       `json:"in_reply_to,omitempty"`
	References  string       `json:"references,omitempty"`
}

// Query narrows a search. Empty fields do not filter.
type Query struct {
	Folder  string
	From    string
	To      string
	Subject string
	Text    string // anywhere in the message body
	Unread  bool
	Since   time.Time
	Before  time.Time
	Limit   int
}

// Folder is a mailbox on the server.
type Folder struct {
	Name   string `json:"name"`
	Role   string `json:"role,omitempty"` // inbox | drafts | sent | trash | junk | archive
	Unread int    `json:"unread"`
	Total  int    `json:"total"`
}

// Backend is the transport of one account. Implementations connect per call and hold nothing between calls.
type Backend interface {
	Folders(ctx context.Context) ([]Folder, error)
	Search(ctx context.Context, q Query) ([]Envelope, error)
	Fetch(ctx context.Context, folder, id string) ([]byte, error)
	// Append stores a raw message in a folder with the given flags (drafts: "\\Draft").
	Append(ctx context.Context, folder string, raw []byte, flags ...string) error
	Send(ctx context.Context, from string, to []string, raw []byte) error
	// SetSeen marks a message read or unread.
	SetSeen(ctx context.Context, folder, id string, seen bool) error
}

// ── reading ─────────────────────────────────────────────────────────────────

const maxBody = 16000

func addrs(hs []*gomail.Address) []Addr {
	out := make([]Addr, 0, len(hs))
	for _, a := range hs {
		out = append(out, Addr{Name: a.Name, Email: a.Address})
	}
	return out
}

// Parse reads a raw RFC 5322 message: headers, the readable body (text/plain preferred, HTML flattened) and
// the attachment list. Bad encodings degrade to what can be read; a message is never lost to a decoder error.
func Parse(raw []byte) (*Message, error) {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return nil, fmt.Errorf("unreadable message: %w", err)
	}
	if mr == nil {
		return nil, fmt.Errorf("unreadable message: %w", err)
	}
	defer mr.Close()
	m := &Message{}
	h := mr.Header
	if d, err := h.Date(); err == nil {
		m.Date = d
	}
	m.Subject, _ = h.Subject()
	if l, err := h.AddressList("From"); err == nil {
		m.From = addrs(l)
	}
	if l, err := h.AddressList("To"); err == nil {
		m.To = addrs(l)
	}
	if l, err := h.AddressList("Cc"); err == nil {
		m.Cc = addrs(l)
	}
	m.MessageID, _ = h.MessageID()
	m.InReplyTo = strings.TrimSpace(h.Get("In-Reply-To"))
	m.References = strings.TrimSpace(h.Get("References"))

	var plain, htmlBody string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
			break
		}
		if p == nil {
			continue
		}
		switch ph := p.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := ph.ContentType()
			b, _ := io.ReadAll(io.LimitReader(p.Body, 4<<20))
			switch ct {
			case "text/plain":
				if plain == "" {
					plain = string(b)
				}
			case "text/html":
				if htmlBody == "" {
					htmlBody = string(b)
				}
			}
		case *gomail.AttachmentHeader:
			name, _ := ph.Filename()
			ct, _, _ := ph.ContentType()
			b, _ := io.Copy(io.Discard, io.LimitReader(p.Body, 64<<20))
			m.Attachments = append(m.Attachments, Attachment{Name: name, MIME: ct, Size: int(b)})
		}
	}
	body := plain
	if strings.TrimSpace(body) == "" && htmlBody != "" {
		body = HTMLText(htmlBody)
	}
	body = strings.ReplaceAll(strings.TrimSpace(body), "\r\n", "\n")
	if r := []rune(body); len(r) > maxBody {
		body = string(r[:maxBody]) + fmt.Sprintf("\n…[%d more characters not shown]", len(r)-maxBody)
	}
	m.Text = body
	m.HasAttach = len(m.Attachments) > 0
	return m, nil
}

var (
	spaceRun         = regexp.MustCompile(`[ \t]+`)
	spaceBeforePunct = regexp.MustCompile(` ([.,;:!?)])`)
)

// HTMLText flattens an HTML mail body to readable text (links keep their address).
func HTMLText(s string) string {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head", "title":
				return
			case "br":
				sb.WriteString("\n")
			case "a":
				for _, a := range n.Attr {
					if a.Key == "href" && strings.HasPrefix(a.Val, "http") {
						defer sb.WriteString(" <" + a.Val + ">")
					}
				}
			}
		}
		if n.Type == html.TextNode {
			sb.WriteString(strings.Join(strings.Fields(n.Data), " ") + " ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "tr", "li", "h1", "h2", "h3", "h4", "table", "blockquote":
				sb.WriteString("\n")
			}
		}
	}
	walk(doc)
	var out []string
	blank := false
	for _, l := range strings.Split(sb.String(), "\n") {
		l = strings.TrimSpace(spaceRun.ReplaceAllString(l, " "))
		l = spaceBeforePunct.ReplaceAllString(l, "$1") // "off ." → "off."
		if l == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ── composing ───────────────────────────────────────────────────────────────

// Draft is what an agent writes.
type Draft struct {
	From       string
	To, Cc     []string
	Subject    string
	Body       string
	InReplyTo  string // Message-ID of the message being answered
	References string
}

// ParseAddresses validates a list of addresses ("Ann <ann@x.com>, bob@y.org"), rejecting anything that could
// smuggle a header (newlines) or is not an address.
func ParseAddresses(list ...string) ([]*mail.Address, error) {
	var out []*mail.Address
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.ContainsAny(s, "\r\n") {
			return nil, errors.New("addresses cannot contain line breaks")
		}
		l, err := mail.ParseAddressList(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid address: %v", s, err)
		}
		out = append(out, l...)
	}
	return out, nil
}

func cleanHeader(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n") {
		return "", errors.New("header values cannot contain line breaks")
	}
	return strings.TrimSpace(s), nil
}

// Compose builds a plain-text RFC 5322 message (UTF-8, quoted-printable). Header injection is impossible:
// addresses are parsed and re-rendered, other headers may not contain line breaks.
func Compose(d Draft) ([]byte, error) {
	from, err := ParseAddresses(d.From)
	if err != nil || len(from) != 1 {
		return nil, errors.New("the account has no valid From address")
	}
	to, err := ParseAddresses(d.To...)
	if err != nil {
		return nil, err
	}
	cc, err := ParseAddresses(d.Cc...)
	if err != nil {
		return nil, err
	}
	subj, err := cleanHeader(d.Subject)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	line := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	render := func(as []*mail.Address) string {
		var s []string
		for _, a := range as {
			s = append(s, a.String())
		}
		return strings.Join(s, ", ")
	}
	line("From", from[0].String())
	if len(to) > 0 {
		line("To", render(to))
	}
	if len(cc) > 0 {
		line("Cc", render(cc))
	}
	line("Subject", mime.QEncoding.Encode("utf-8", subj))
	line("Date", time.Now().Format(time.RFC1123Z))
	host := "prism.local"
	if i := strings.LastIndex(from[0].Address, "@"); i >= 0 {
		host = from[0].Address[i+1:]
	}
	line("Message-ID", fmt.Sprintf("<%d.%d@%s>", time.Now().UnixNano(), time.Now().Nanosecond()%9973, host))
	if r, err := cleanHeader(d.InReplyTo); err == nil && r != "" {
		if !strings.HasPrefix(r, "<") {
			r = "<" + strings.Trim(r, "<>") + ">"
		}
		line("In-Reply-To", r)
		refs := strings.TrimSpace(d.References)
		if strings.ContainsAny(refs, "\r\n") {
			refs = ""
		}
		line("References", strings.TrimSpace(refs+" "+r))
	}
	line("MIME-Version", "1.0")
	line("Content-Type", "text/plain; charset=utf-8")
	line("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qw := quotedprintable.NewWriter(&b)
	_, _ = qw.Write([]byte(strings.ReplaceAll(d.Body, "\r\n", "\n")))
	_ = qw.Close()
	b.WriteString("\r\n")
	return b.Bytes(), nil
}

// Recipients returns the bare addresses of To and Cc (for the SMTP envelope).
func Recipients(d Draft) ([]string, error) {
	as, err := ParseAddresses(append(append([]string{}, d.To...), d.Cc...)...)
	if err != nil {
		return nil, err
	}
	if len(as) == 0 {
		return nil, errors.New("no recipients")
	}
	var out []string
	for _, a := range as {
		out = append(out, a.Address)
	}
	sort.Strings(out)
	return out, nil
}

// Preview renders a draft for the confirmation shown to the user.
func Preview(d Draft) string {
	body := strings.TrimSpace(d.Body)
	if r := []rune(body); len(r) > 500 {
		body = string(r[:500]) + "…"
	}
	s := "To: " + strings.Join(d.To, ", ")
	if len(d.Cc) > 0 {
		s += "\nCc: " + strings.Join(d.Cc, ", ")
	}
	return s + "\nSubject: " + d.Subject + "\n\n" + body
}
