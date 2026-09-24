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

var producedTables = []string{
	"agent_profiles", "soul_history", "evolution_proposals", "sessions", "session_messages", "tasks", "task_summaries",
	"chat_messages", "pending_asks", "memory_banks", "memory_facts", "memory_raw", "memory_links", "memory_pending_facts",
	"memory_ops", "memory_entities", "memory_entity_mentions", "memory_entity_links", "skills", "crons", "intents",
	"briefings", "bookmarks", "artifacts", "downloads", "doc_chunks", "logs", "notifications", "kb_folders", "kb_pages",
	"llm_calls", "tool_calls", "folder_maps", "folder_map_entries", "bg_processes", "trackers", "tracker_rows", "tracker_changes",
}

var integrationTables = []string{
	"settings", "providers", "provider_keys", "models", "model_lists", "mail_accounts", "mcp_servers", "skill_hubs",
	"tool_settings", "plugins", "doc_sources", "telegram_topics",
}

// Reset empties the tables of the given scope (all other data keeps its identity counters untouched) and
// removes artifact files that lived under dataDir. Callers must stop running work first and re-seed the
// built-in agents and schedules afterwards.
func Reset(ctx context.Context, db *pgxpool.Pool, scope, dataDir string) error {
	tables := append([]string{}, producedTables...)
	switch scope {
	case ScopeData:
	case ScopeAll:
		tables = append(tables, integrationTables...)
	default:
		return os.ErrInvalid
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
	if _, err := db.Exec(ctx, `TRUNCATE `+strings.Join(tables, ", ")+` RESTART IDENTITY CASCADE`); err != nil {
		return err
	}
	if scope == ScopeData {
		// document sources stay, their chunks were dropped: mark them as not yet indexed
		_, _ = db.Exec(ctx, `UPDATE doc_sources SET indexed_at=NULL`)
	}
	return nil
}
