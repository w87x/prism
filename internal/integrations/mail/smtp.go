package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// sendSMTP delivers one message: implicit TLS (465), STARTTLS (587) or plain (tests, local relays).
func sendSMTP(ctx context.Context, a Account, tlsCfg *tls.Config, from string, to []string, msg io.Reader) error {
	if a.SMTPHost == "" {
		return errors.New("the account has no SMTP server: add one in Settings → Integrations → Mail")
	}
	port := a.SMTPPort
	if port == 0 {
		switch a.SMTPSecurity {
		case "none", "starttls":
			port = 587
		default:
			port = 465
		}
	}
	addr := net.JoinHostPort(a.SMTPHost, strconv.Itoa(port))
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: a.SMTPHost}
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
	if a.SMTPSecurity != "none" && a.SMTPSecurity != "starttls" {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return fmt.Errorf("TLS to %s failed: %w", addr, err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, a.SMTPHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP greeting from %s failed: %w", addr, err)
	}
	defer c.Close()
	if a.SMTPSecurity == "starttls" {
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS with %s failed: %w", addr, err)
		}
	}
	user, pass := a.SMTPUser, a.SMTPPassword
	if user == "" {
		user = a.User
	}
	if pass == "" {
		pass = a.Password
	}
	if user != "" && pass != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(loginAuth(user, pass, a.SMTPSecurity == "none")); err != nil {
				return fmt.Errorf("the SMTP server refused the login: %w", err)
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("the server refused the sender %s: %w", from, err)
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("the server refused the recipient %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// loginAuth is PLAIN authentication. The connection is encrypted (TLS/STARTTLS) unless the user chose "none"
// for a local relay: net/smtp's own PlainAuth would refuse that, so this one leaves the choice to the account.
type plain struct {
	user, pass string
}

func loginAuth(user, pass string, _ bool) smtp.Auth { return plain{user, pass} }

func (p plain) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + p.user + "\x00" + p.pass), nil
}
func (p plain) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}
