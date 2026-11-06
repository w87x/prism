// Package integrationarchive exports only integration configuration in an authenticated encrypted archive.
package integrationarchive

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const magic = "PRISM-INTEGRATIONS\x00\x01"
const iterations = 600000
const maxSize = 32 << 20

var tables = []string{"providers", "provider_keys", "models", "model_lists", "mcp_servers", "mail_accounts", "tool_settings"}
var settingKeys = []string{"models.roles", "model_actions", "model_checks", "context", "browser", "web", "telegram", "obsidian", "elevenlabs", "consult", "guardrails", "tool_selector", "skills", "macos"}

type payload struct {
	Version    int                        `json:"version"`
	Migrations []string                   `json:"migrations"`
	Tables     map[string]json.RawMessage `json:"tables"`
	Settings   json.RawMessage            `json:"settings"`
}

func derive(pass string, salt []byte) ([]byte, error) {
	if len(pass) < 12 {
		return nil, errors.New("passphrase must be at least 12 bytes")
	}
	return pbkdf2.Key(sha256.New, pass, salt, iterations, 32)
}

// Seal encrypts and authenticates the payload; all metadata except the format marker is random.
func Seal(data []byte, pass string) ([]byte, error) {
	if len(data) > maxSize {
		return nil, errors.New("archive exceeds size limit")
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	key, err := derive(pass, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := append([]byte(magic), salt...)
	out = append(out, nonce...)
	return g.Seal(out, nonce, data, []byte(magic)), nil
}

// Open authenticates before returning any plaintext.
func Open(data []byte, pass string) ([]byte, error) {
	if len(data) > maxSize+1024 || len(data) < len(magic)+16+12+16 || string(data[:len(magic)]) != magic {
		return nil, errors.New("invalid integration archive")
	}
	salt := data[len(magic) : len(magic)+16]
	nonce := data[len(magic)+16 : len(magic)+28]
	key, err := derive(pass, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := g.Open(nil, nonce, data[len(magic)+28:], []byte(magic))
	if err != nil {
		return nil, errors.New("archive authentication failed: wrong passphrase or corrupted file")
	}
	return plain, nil
}
func migrations(ctx context.Context, tx pgx.Tx) ([]string, error) {
	var versions []string
	err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(version ORDER BY version), ARRAY[]::text[]) FROM schema_migrations`).Scan(&versions)
	return versions, err
}

// Export reads a consistent snapshot and returns encrypted bytes. It never includes chat or memory data.
func Export(ctx context.Context, db *pgxpool.Pool, pass string) ([]byte, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	p := payload{Version: 1, Tables: map[string]json.RawMessage{}}
	if p.Migrations, err = migrations(ctx, tx); err != nil {
		return nil, err
	}
	for _, t := range tables {
		var raw []byte
		query := `SELECT coalesce(jsonb_agg(to_jsonb(t)), '[]'::jsonb) FROM ` + pgx.Identifier{t}.Sanitize() + ` t`
		if err = tx.QueryRow(ctx, query).Scan(&raw); err != nil {
			return nil, err
		}
		p.Tables[t] = raw
	}
	if err = tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('key',key,'value',value)), '[]'::jsonb) FROM settings WHERE key=ANY($1)`, settingKeys).Scan(&p.Settings); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return Seal(raw, pass)
}

// Import refuses to replace any existing integration configuration and applies everything in one transaction.
func Import(ctx context.Context, db *pgxpool.Pool, archive []byte, pass string) error {
	raw, err := Open(archive, pass)
	if err != nil {
		return err
	}
	var p payload
	if err = json.Unmarshal(raw, &p); err != nil || p.Version != 1 {
		return errors.New("unsupported integration archive")
	}
	if len(p.Tables) != len(tables) {
		return errors.New("invalid integration table list")
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	versions, err := migrations(ctx, tx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(versions, p.Migrations) {
		return errors.New("database schema versions differ; update both applications first")
	}
	names := append(append([]string{}, tables...), "settings")
	for i := range names {
		names[i] = pgx.Identifier{names[i]}.Sanitize()
	}
	if _, err = tx.Exec(ctx, "LOCK TABLE "+strings.Join(names, ",")+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		return err
	}
	for _, t := range tables {
		var count int64
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{t}.Sanitize()).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("target integration table %s is not empty", t)
		}
		rows, ok := p.Tables[t]
		if !ok || !json.Valid(rows) {
			return fmt.Errorf("missing integration table %s", t)
		}
		q := fmt.Sprintf("INSERT INTO %s SELECT * FROM jsonb_populate_recordset(NULL::%s,$1::jsonb)", pgx.Identifier{t}.Sanitize(), pgx.Identifier{t}.Sanitize())
		if _, err = tx.Exec(ctx, q, rows); err != nil {
			return fmt.Errorf("import %s: %w", t, err)
		}
	}
	var count int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM settings WHERE key=ANY($1)`, settingKeys).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("target already has integration settings")
	}
	if !json.Valid(p.Settings) {
		return errors.New("invalid settings")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO settings(key,value) SELECT key, CASE WHEN key='telegram' THEN jsonb_set(value,'{enabled}','false'::jsonb,true) ELSE value END FROM jsonb_to_recordset($1::jsonb) AS s(key text,value jsonb) WHERE key=ANY($2)`, p.Settings, settingKeys); err != nil {
		return err
	}
	for _, t := range tables {
		if t == "tool_settings" {
			continue
		}
		var seq *string
		if err = tx.QueryRow(ctx, `SELECT pg_get_serial_sequence($1,'id')`, t).Scan(&seq); err != nil {
			return err
		}
		if seq != nil {
			q := fmt.Sprintf("SELECT setval($1,coalesce((SELECT max(id) FROM %s),1),(SELECT count(*)>0 FROM %s))", pgx.Identifier{t}.Sanitize(), pgx.Identifier{t}.Sanitize())
			if _, err = tx.Exec(ctx, q, *seq); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
