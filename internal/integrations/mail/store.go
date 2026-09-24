package mail

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store keeps the accounts (passwords included) in PostgreSQL, like the rest of PRISM's credentials.
type Store struct{ DB *pgxpool.Pool }

var tagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,23}$`)

const cols = `id,tag,backend,enabled,himalaya,imap_host,imap_port,imap_security,mail_user,password,smtp_host,smtp_port,smtp_security,smtp_user,smtp_password,from_addr,inbox,drafts,sent`

func scan(row interface{ Scan(...any) error }) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Tag, &a.Backend, &a.Enabled, &a.Himalaya, &a.IMAPHost, &a.IMAPPort, &a.IMAPSecurity, &a.User, &a.Password,
		&a.SMTPHost, &a.SMTPPort, &a.SMTPSecurity, &a.SMTPUser, &a.SMTPPassword, &a.From, &a.Inbox, &a.Drafts, &a.Sent)
	a.HasPassword = a.Password != ""
	return a, err
}

// List returns every account with the secrets removed (what the UI and the model may see).
func (s *Store) List(ctx context.Context) ([]Account, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+cols+` FROM mail_accounts ORDER BY tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		a.Password, a.SMTPPassword = "", ""
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get returns one account by tag with its secrets (for connecting).
func (s *Store) Get(ctx context.Context, tag string) (Account, error) {
	a, err := scan(s.DB.QueryRow(ctx, `SELECT `+cols+` FROM mail_accounts WHERE tag=$1`, strings.ToLower(strings.TrimSpace(tag))))
	if err != nil {
		return a, fmt.Errorf("no mail account tagged %q (see mail_accounts)", tag)
	}
	return a, nil
}

// Save creates or updates an account. Empty password fields keep the stored ones.
func (s *Store) Save(ctx context.Context, a Account) (int64, error) {
	a.Tag = strings.ToLower(strings.TrimSpace(a.Tag))
	if !tagRe.MatchString(a.Tag) {
		return 0, errors.New("the tag must be 1–24 characters: lowercase letters, digits, - or _ (e.g. work, personal2)")
	}
	switch a.Backend {
	case "imap":
		if strings.TrimSpace(a.IMAPHost) == "" || strings.TrimSpace(a.User) == "" {
			return 0, errors.New("the IMAP server and user name are required")
		}
	case "himalaya":
		if strings.TrimSpace(a.Himalaya) == "" {
			return 0, errors.New("choose the himalaya account to use")
		}
	default:
		return 0, errors.New("backend must be imap or himalaya")
	}
	for _, sec := range []string{a.IMAPSecurity, a.SMTPSecurity} {
		if sec != "" && sec != "tls" && sec != "starttls" && sec != "none" {
			return 0, errors.New("security must be tls, starttls or none")
		}
	}
	if a.IMAPSecurity == "" {
		a.IMAPSecurity = "tls"
	}
	if a.SMTPSecurity == "" {
		a.SMTPSecurity = "tls"
	}
	if a.From != "" {
		if _, err := ParseAddresses(a.From); err != nil {
			return 0, fmt.Errorf("the From address is not valid: %v", err)
		}
	}
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO mail_accounts(id,tag,backend,enabled,himalaya,imap_host,imap_port,imap_security,mail_user,password,smtp_host,smtp_port,smtp_security,smtp_user,smtp_password,from_addr,inbox,drafts,sent)
		VALUES(COALESCE(NULLIF($1,0), nextval('mail_accounts_id_seq')),$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		ON CONFLICT (id) DO UPDATE SET tag=EXCLUDED.tag, backend=EXCLUDED.backend, enabled=EXCLUDED.enabled, himalaya=EXCLUDED.himalaya, imap_host=EXCLUDED.imap_host,
			imap_port=EXCLUDED.imap_port, imap_security=EXCLUDED.imap_security, mail_user=EXCLUDED.mail_user,
			password=CASE WHEN EXCLUDED.password='' THEN mail_accounts.password ELSE EXCLUDED.password END,
			smtp_host=EXCLUDED.smtp_host, smtp_port=EXCLUDED.smtp_port, smtp_security=EXCLUDED.smtp_security, smtp_user=EXCLUDED.smtp_user,
			smtp_password=CASE WHEN EXCLUDED.smtp_password='' THEN mail_accounts.smtp_password ELSE EXCLUDED.smtp_password END,
			from_addr=EXCLUDED.from_addr, inbox=EXCLUDED.inbox, drafts=EXCLUDED.drafts, sent=EXCLUDED.sent
		RETURNING id`,
		a.ID, a.Tag, a.Backend, a.Enabled, a.Himalaya, strings.TrimSpace(a.IMAPHost), a.IMAPPort, a.IMAPSecurity, strings.TrimSpace(a.User), a.Password,
		strings.TrimSpace(a.SMTPHost), a.SMTPPort, a.SMTPSecurity, strings.TrimSpace(a.SMTPUser), a.SMTPPassword, strings.TrimSpace(a.From),
		strings.TrimSpace(a.Inbox), strings.TrimSpace(a.Drafts), strings.TrimSpace(a.Sent)).Scan(&id)
	if err != nil && strings.Contains(err.Error(), "mail_accounts_tag_key") {
		return 0, fmt.Errorf("an account tagged %q already exists", a.Tag)
	}
	return id, err
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM mail_accounts WHERE id=$1`, id)
	return err
}

// NewBackend builds the transport for an account.
func NewBackend(a Account) (Backend, error) {
	switch a.Backend {
	case "himalaya":
		return &Himalaya{Account: a.Himalaya}, nil
	case "imap", "":
		return &IMAP{A: a}, nil
	}
	return nil, fmt.Errorf("unknown mail backend %q", a.Backend)
}
