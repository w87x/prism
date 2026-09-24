package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// IMAP is PRISM's own IMAP/SMTP client for one account.
type IMAP struct {
	A Account
	// Timeout bounds one operation (default 60 s).
	Timeout time.Duration
	// TLSConfig overrides certificate checking (tests).
	TLSConfig *tls.Config
}

func (b *IMAP) timeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return 60 * time.Second
}

func (b *IMAP) port() int {
	if b.A.IMAPPort != 0 {
		return b.A.IMAPPort
	}
	if b.A.IMAPSecurity == "none" || b.A.IMAPSecurity == "starttls" {
		return 143
	}
	return 993
}

// dial connects and logs in. The caller must close the client.
func (b *IMAP) dial(ctx context.Context) (*imapclient.Client, error) {
	if b.A.IMAPHost == "" {
		return nil, errors.New("the account has no IMAP server")
	}
	addr := net.JoinHostPort(b.A.IMAPHost, strconv.Itoa(b.port()))
	d := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(b.timeout()))
	tlsCfg := b.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: b.A.IMAPHost}
	}
	var c *imapclient.Client
	switch b.A.IMAPSecurity {
	case "none":
		c = imapclient.New(conn, nil)
	case "starttls":
		c, err = imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: tlsCfg})
	default: // "tls" and empty
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS to %s failed: %w", addr, err)
		}
		c = imapclient.New(tc, nil)
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("IMAP handshake with %s failed: %w", addr, err)
	}
	if err := c.Login(b.A.User, b.A.Password).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("the server refused the login for %s: %w", b.A.User, err)
	}
	return c, nil
}

func (b *IMAP) with(ctx context.Context, fn func(c *imapclient.Client) error) error {
	c, err := b.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Logout().Wait(); c.Close() }()
	return fn(c)
}

var roleByAttr = map[imap.MailboxAttr]string{
	imap.MailboxAttrDrafts: "drafts", imap.MailboxAttrSent: "sent", imap.MailboxAttrTrash: "trash",
	imap.MailboxAttrJunk: "junk", imap.MailboxAttrArchive: "archive",
}

func (b *IMAP) Folders(ctx context.Context) ([]Folder, error) {
	var out []Folder
	err := b.with(ctx, func(c *imapclient.Client) error {
		list, err := c.List("", "*", &imap.ListOptions{ReturnStatus: &imap.StatusOptions{NumMessages: true, NumUnseen: true}}).Collect()
		if err != nil {
			return err
		}
		for _, m := range list {
			f := Folder{Name: m.Mailbox}
			if strings.EqualFold(m.Mailbox, "INBOX") {
				f.Role = "inbox"
			}
			for _, a := range m.Attrs {
				if r := roleByAttr[a]; r != "" {
					f.Role = r
				}
			}
			if m.Status != nil {
				if m.Status.NumMessages != nil {
					f.Total = int(*m.Status.NumMessages)
				}
				if m.Status.NumUnseen != nil {
					f.Unread = int(*m.Status.NumUnseen)
				}
			}
			out = append(out, f)
		}
		return nil
	})
	return out, err
}

func (b *IMAP) inbox() string {
	if b.A.Inbox != "" {
		return b.A.Inbox
	}
	return "INBOX"
}

func imapAddrs(as []imap.Address) []Addr {
	out := make([]Addr, 0, len(as))
	for _, a := range as {
		out = append(out, Addr{Name: a.Name, Email: a.Addr()})
	}
	return out
}

func (b *IMAP) Search(ctx context.Context, q Query) ([]Envelope, error) {
	folder := q.Folder
	if folder == "" {
		folder = b.inbox()
	}
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	crit := &imap.SearchCriteria{Since: q.Since, Before: q.Before}
	for k, v := range map[string]string{"From": q.From, "To": q.To, "Subject": q.Subject} {
		if v != "" {
			crit.Header = append(crit.Header, imap.SearchCriteriaHeaderField{Key: k, Value: v})
		}
	}
	if q.Text != "" {
		crit.Text = []string{q.Text}
	}
	if q.Unread {
		crit.NotFlag = []imap.Flag{imap.FlagSeen}
	}
	var out []Envelope
	err := b.with(ctx, func(c *imapclient.Client) error {
		if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("cannot open folder %q: %w", folder, err)
		}
		data, err := c.UIDSearch(crit, nil).Wait()
		if err != nil {
			return err
		}
		uids := data.AllUIDs()
		if len(uids) > limit { // newest last: keep the tail
			uids = uids[len(uids)-limit:]
		}
		if len(uids) == 0 {
			return nil
		}
		msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, Envelope: true, Flags: true}).Collect()
		if err != nil {
			return err
		}
		for i := len(msgs) - 1; i >= 0; i-- { // newest first
			m := msgs[i]
			e := Envelope{ID: strconv.FormatUint(uint64(m.UID), 10), Folder: folder, Unread: true}
			if m.Envelope != nil {
				e.From, e.To = imapAddrs(m.Envelope.From), imapAddrs(m.Envelope.To)
				e.Subject, e.Date, e.MessageID = m.Envelope.Subject, m.Envelope.Date, m.Envelope.MessageID
			}
			for _, f := range m.Flags {
				switch f {
				case imap.FlagSeen:
					e.Unread = false
				case imap.FlagFlagged:
					e.Flagged = true
				}
			}
			out = append(out, e)
		}
		return nil
	})
	return out, err
}

func parseUID(id string) (imap.UID, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(id), 10, 32)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("%q is not a message id (take it from mail_search)", id)
	}
	return imap.UID(n), nil
}

func (b *IMAP) Fetch(ctx context.Context, folder, id string) ([]byte, error) {
	uid, err := parseUID(id)
	if err != nil {
		return nil, err
	}
	if folder == "" {
		folder = b.inbox()
	}
	var raw []byte
	err = b.with(ctx, func(c *imapclient.Client) error {
		if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("cannot open folder %q: %w", folder, err)
		}
		sect := &imap.FetchItemBodySection{Peek: true} // reading must not silently mark the mail as read
		msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sect}}).Collect()
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return fmt.Errorf("no message %s in %s", id, folder)
		}
		raw = msgs[0].FindBodySection(sect)
		if raw == nil {
			return errors.New("the server returned no message body")
		}
		return nil
	})
	return raw, err
}

func (b *IMAP) Append(ctx context.Context, folder string, raw []byte, flags ...string) error {
	if folder == "" {
		return errors.New("no folder to store the message in")
	}
	return b.with(ctx, func(c *imapclient.Client) error {
		var fl []imap.Flag
		for _, f := range flags {
			fl = append(fl, imap.Flag(f))
		}
		cmd := c.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: fl, Time: time.Now()})
		if _, err := cmd.Write(raw); err != nil {
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		_, err := cmd.Wait()
		return err
	})
}

func (b *IMAP) SetSeen(ctx context.Context, folder, id string, seen bool) error {
	uid, err := parseUID(id)
	if err != nil {
		return err
	}
	if folder == "" {
		folder = b.inbox()
	}
	return b.with(ctx, func(c *imapclient.Client) error {
		if _, err := c.Select(folder, nil).Wait(); err != nil {
			return err
		}
		op := imap.StoreFlagsAdd
		if !seen {
			op = imap.StoreFlagsDel
		}
		return c.Store(imap.UIDSetNum(uid), &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close()
	})
}

// Send delivers a raw message over SMTP.
func (b *IMAP) Send(ctx context.Context, from string, to []string, raw []byte) error {
	return sendSMTP(ctx, b.A, b.TLSConfig, from, to, bytes.NewReader(raw))
}
