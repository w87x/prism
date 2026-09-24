-- Mail accounts: several mailboxes on different servers, each with a tag agents use ("work", "personal").
CREATE TABLE mail_accounts (
  id            bigserial PRIMARY KEY,
  tag           text UNIQUE NOT NULL,
  backend       text NOT NULL DEFAULT 'imap',     -- imap | himalaya
  enabled       boolean NOT NULL DEFAULT true,
  himalaya      text NOT NULL DEFAULT '',          -- account name in himalaya's own config
  imap_host     text NOT NULL DEFAULT '',
  imap_port     int NOT NULL DEFAULT 0,
  imap_security text NOT NULL DEFAULT 'tls',       -- tls | starttls | none
  mail_user     text NOT NULL DEFAULT '',
  password      text NOT NULL DEFAULT '',
  smtp_host     text NOT NULL DEFAULT '',
  smtp_port     int NOT NULL DEFAULT 0,
  smtp_security text NOT NULL DEFAULT 'tls',
  smtp_user     text NOT NULL DEFAULT '',
  smtp_password text NOT NULL DEFAULT '',
  from_addr     text NOT NULL DEFAULT '',
  inbox         text NOT NULL DEFAULT '',
  drafts        text NOT NULL DEFAULT '',
  sent          text NOT NULL DEFAULT ''
);
