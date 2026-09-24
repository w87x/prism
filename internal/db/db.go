// Package db wraps the pgx pool, applies embedded migrations and can create
// the target database on first run (onboarding).
package db

import (
	"context"
	"embed"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type DB struct {
	*pgxpool.Pool
	Vector   bool // pgvector is available on the server
	VectorOn bool // pgvector is enabled in this database and vec columns exist
}

// Open connects to the database named in dsn and runs migrations.
func Open(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 16
	cfg.ConnConfig.ConnectTimeout = 8 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, err
	}
	d := &DB{Pool: pool}
	if err := d.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	d.Vector = d.detectVector(ctx)
	if d.Vector {
		if err := d.enableVector(ctx); err != nil {
			log.Printf("pgvector present but could not be enabled (%v) — using in-process similarity", err)
		} else {
			d.VectorOn = true
		}
	}
	return d, nil
}

// enableVector creates the extension and the vec columns, then backfills them
// from the portable bytea embeddings (which stay the source of truth).
func (d *DB) enableVector(ctx context.Context) error {
	if _, err := d.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return err
	}
	for _, t := range []string{"memory_facts", "doc_chunks"} {
		if _, err := d.Exec(ctx, `ALTER TABLE `+t+` ADD COLUMN IF NOT EXISTS vec vector`); err != nil {
			return err
		}
		for {
			rows, err := d.Query(ctx, `SELECT id, embedding FROM `+t+` WHERE vec IS NULL AND embedding IS NOT NULL LIMIT 500`)
			if err != nil {
				return err
			}
			type row struct {
				id  int64
				emb []byte
			}
			var batch []row
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.id, &r.emb); err != nil {
					rows.Close()
					return err
				}
				batch = append(batch, r)
			}
			rows.Close()
			if len(batch) == 0 {
				break
			}
			for _, r := range batch {
				lit := VectorLiteral(decodeF32(r.emb))
				if lit == "" { // unreadable embedding: drop it so the loop terminates
					_, _ = d.Exec(ctx, `UPDATE `+t+` SET embedding=NULL WHERE id=$1`, r.id)
					continue
				}
				if _, err := d.Exec(ctx, `UPDATE `+t+` SET vec=$2::vector WHERE id=$1`, r.id, lit); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func decodeF32(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// VectorLiteral renders a pgvector text literal: "[0.1,0.2,…]".
func VectorLiteral(v []float32) string {
	if len(v) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(x), 'g', 8, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}

func (d *DB) detectVector(ctx context.Context) bool {
	var n int
	_ = d.QueryRow(ctx, `SELECT count(*) FROM pg_available_extensions WHERE name='vector'`).Scan(&n)
	return n > 0
}

func (d *DB) migrate(ctx context.Context) error {
	conn, err := d.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	// serialize concurrent starters
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(727272)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(727272)`)
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, n).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sqlb, err := migrationsFS.ReadFile("migrations/" + n)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlb)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("%s: %w", n, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, n); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// DatabaseName extracts the database name from a DSN (URL form).
func DatabaseName(dsn string) (string, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "", err
	}
	if cfg.Database == "" {
		return "prism", nil
	}
	return cfg.Database, nil
}

// EnsureDatabase creates the database referenced by dsn when it is missing,
// by connecting to the maintenance database "postgres". It reports whether it
// created the database.
func EnsureDatabase(ctx context.Context, dsn string) (created bool, err error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return false, fmt.Errorf("parse dsn: %w", err)
	}
	name := cfg.Database
	if name == "" {
		name = "prism"
	}
	admin := cfg.Copy()
	admin.Database = "postgres"
	admin.ConnectTimeout = 8 * time.Second
	conn, err := pgx.ConnectConfig(ctx, admin)
	if err != nil {
		return false, err
	}
	defer conn.Close(context.Background())
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()+` ENCODING 'UTF8' TEMPLATE template0 LC_COLLATE 'C' LC_CTYPE 'C'`); err != nil {
		// retry with the server defaults if the C locale is not permitted
		if _, err2 := conn.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); err2 != nil {
			return false, err
		}
	}
	return true, nil
}

// BuildDSN builds a postgres:// URL from parts.
func BuildDSN(host string, port int, user, password, dbname string) string {
	u := url.URL{Scheme: "postgres", Host: fmt.Sprintf("%s:%d", host, port), Path: "/" + dbname}
	if user != "" {
		if password != "" {
			u.User = url.UserPassword(user, password)
		} else {
			u.User = url.User(user)
		}
	}
	q := url.Values{}
	q.Set("sslmode", "prefer")
	u.RawQuery = q.Encode()
	return u.String()
}

// IsUnique reports a unique-violation error.
func IsUnique(err error) bool {
	pe, ok := err.(*pgconn.PgError)
	return ok && pe.Code == "23505"
}

// Redact hides the password in a DSN for display.
func Redact(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn
	}
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), "••••")
	}
	return strings.ReplaceAll(u.String(), "%E2%80%A2", "•")
}

// ProbeInfo describes a Postgres server for the setup wizard.
type ProbeInfo struct {
	Version  string
	Database string
	Exists   bool
	Vector   bool
}

// Probe connects to the server (via the maintenance DB) and reports on it without changing anything.
func Probe(ctx context.Context, dsn string) (*ProbeInfo, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	name := cfg.Database
	if name == "" {
		name = "prism"
	}
	admin := cfg.Copy()
	admin.Database = "postgres"
	admin.ConnectTimeout = 8 * time.Second
	conn, err := pgx.ConnectConfig(ctx, admin)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	info := &ProbeInfo{Database: name}
	if err := conn.QueryRow(ctx, `SHOW server_version`).Scan(&info.Version); err != nil {
		return nil, err
	}
	_ = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&info.Exists)
	var n int
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM pg_available_extensions WHERE name='vector'`).Scan(&n)
	info.Vector = n > 0
	return info, nil
}
