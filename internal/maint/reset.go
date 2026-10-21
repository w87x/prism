package maint

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Reset scopes: "data" wipes everything PRISM produced (agents, tasks, chats, memory, briefings, artifacts,
// knowledge pages, trackers, logs, usage history…) but keeps how PRISM is wired up (API keys, providers,
// models, mail accounts, MCP servers, skill hubs, tool settings, plugins, settings, document sources and
// Telegram topics). "all" wipes those too, leaving an empty database like a first run.
const (
	ScopeData = "data"
	ScopeAll  = "all"
)

// What a reset leaves alone. Everything else in the database is wiped, so a table added in a later version is cleared too: the old
// hand-kept list of "produced" tables had fallen 34 tables behind, and task_meta (acceptance criteria, ratings, handovers) survived a reset
// while task ids started again from 1 — new tasks inherited old tasks' criteria and were judged against work they never did.
var keptTables = map[string]bool{
	"schema_migrations": true, // which migrations ran
	"memory_predicates": true, // seeded by the first migration: the cardinality rules
	"chats":             true, // the Main chat row is seeded by a migration; the others are removed below
	"core_blocks":       true, // its default blocks are seeded by a migration; they are emptied below
}

// stateSettings are settings that remember WHERE the work had got to — an id, a time — not how PRISM is configured. They go with the data:
// the ids start again from 1, so "harvest bookmarks after message 23138" stopped harvesting until a new database caught up, and a list of
// task ids already offered to Daedalus would have hidden the new tasks that reuse them.
var stateSettings = []string{"harvest.bookmarks", "skill_recurring", "skill_tidy", "jobs_review_at", "memory.digest", "memory.verify", "seed_tools"}

var integrationTables = []string{
	"settings", "providers", "provider_keys", "models", "model_lists", "mail_accounts", "mcp_servers", "skill_hubs",
	"tool_settings", "plugins", "doc_sources", "telegram_topics",
}

// Reset empties the tables of the given scope (all other data keeps its identity counters untouched) and
// removes artifact files that lived under dataDir. Callers must stop running work first and re-seed the
// built-in agents and schedules afterwards.
func Reset(ctx context.Context, db *pgxpool.Pool, scope, dataDir string) error {
	if scope != ScopeData && scope != ScopeAll {
		return os.ErrInvalid
	}
	kept := map[string]bool{}
	for t := range keptTables {
		kept[t] = true
	}
	if scope == ScopeData {
		for _, t := range integrationTables {
			kept[t] = true
		}
	}
	// the tables that really exist, minus what is kept (a table of a newer or older schema needs no special care)
	var tables []string
	if rows, err := db.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1`); err == nil {
		for rows.Next() {
			var t string
			if rows.Scan(&t) == nil && !kept[t] {
				tables = append(tables, t)
			}
		}
		rows.Close()
	}
	// artifact files first (the rows are about to go)
	if rows, err := db.Query(ctx, `SELECT path FROM artifacts`); err == nil {
		root := filepath.Clean(dataDir) + string(os.PathSeparator)
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil && p != "" && strings.HasPrefix(filepath.Clean(p), root) {
				_ = os.Remove(p)
			}
		}
		rows.Close()
	}
	// skills' bundled files live on disk next to their rows: a new skill of the same name must not find the old one's files
	if dataDir != "" {
		_ = os.RemoveAll(filepath.Join(filepath.Clean(dataDir), "skills"))
	}
	if len(tables) > 0 {
		if _, err := db.Exec(ctx, `TRUNCATE `+strings.Join(tables, ", ")+` RESTART IDENTITY CASCADE`); err != nil {
			return err
		}
	}
	if scope == ScopeData {
		_, _ = db.Exec(ctx, `DELETE FROM settings WHERE key = ANY($1)`, stateSettings)
	}
	// the seeded rows stay, emptied of what was learned
	_, _ = db.Exec(ctx, `DELETE FROM chats WHERE topic<>''`)
	// the Main chat is seeded by a migration, and a cascade from the tables above can take it too
	_, _ = db.Exec(ctx, `INSERT INTO chats(topic, title, auto_title) SELECT '', 'Main', false WHERE NOT EXISTS (SELECT 1 FROM chats WHERE topic='')`)
	_, _ = db.Exec(ctx, `UPDATE core_blocks SET text='', updated_by='', updated_at=now()`)
	_, _ = db.Exec(ctx, `DELETE FROM core_blocks WHERE label NOT IN ('about_user','preferences','now')`)
	if scope == ScopeData {
		// document sources stay, their chunks were dropped: mark them as not yet indexed
		_, _ = db.Exec(ctx, `UPDATE doc_sources SET indexed_at=NULL`)
	}
	return nil
}
