package server

import (
	"context"
	"errors"
	"os"

	"prism/internal/tools/builtin"
)

func (s *Server) registerMisc() {
	a := s.App

	rpc(s, "downloads.list", func(ctx context.Context, _ none) ([]builtin.Download, error) { return a.Downloads.List(ctx, 100) })
	rpc(s, "downloads.start", func(ctx context.Context, r struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
	}) (builtin.Download, error) {
		return a.Downloads.Start(ctx, r.URL, r.Filename, "user")
	})
	rpc(s, "downloads.cancel", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Downloads.Cancel(ctx, r.ID)
	})

	rpc(s, "processes.list", func(ctx context.Context, _ none) ([]builtin.Process, error) { return a.Processes.List(ctx, 100) })
	rpc(s, "processes.cancel", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Processes.Cancel(ctx, r.ID)
	})
	rpc(s, "processes.log", func(ctx context.Context, r struct {
		ID     int64 `json:"id"`
		Offset int   `json:"offset"`
	}) (string, error) {
		text, _, err := a.Processes.Log(ctx, r.ID, r.Offset, 40000)
		return text, err
	})

	rpc(s, "bookmarks.list", func(ctx context.Context, _ none) ([]builtin.Bookmark, error) {
		return builtin.ListBookmarks(ctx, a.DB.Pool)
	})
	rpc(s, "bookmarks.save", func(ctx context.Context, b builtin.Bookmark) (int64, error) {
		if b.URL == "" {
			return 0, errors.New("url required")
		}
		if b.ID == 0 { // a fresh, user-added bookmark: rank it above whatever the agents have accumulated
			top, err := builtin.TopmostBookmarkRank(ctx, a.DB.Pool)
			if err != nil {
				return 0, err
			}
			b.Rank = top
		}
		return builtin.SaveBookmark(ctx, a.DB.Pool, b)
	})
	rpc(s, "bookmarks.enrich", func(ctx context.Context, r struct {
		URL string `json:"url"`
	}) (builtin.BookmarkSuggestion, error) {
		return builtin.EnrichBookmark(ctx, builtin.Deps{Settings: a.Settings, LLM: a.LLM}, r.URL)
	})
	rpc(s, "bookmarks.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		_, err := a.DB.Exec(ctx, `DELETE FROM bookmarks WHERE id=$1`, r.ID)
		return true, err
	})

	rpc(s, "artifacts.list", func(ctx context.Context, _ none) ([]builtin.Artifact, error) {
		return builtin.ListArtifacts(ctx, a.DB.Pool)
	})
	rpc(s, "artifacts.keep", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, builtin.KeepArtifact(ctx, a.DB.Pool, r.ID)
	})
	// bulk versions for the Artifacts page: keep = drop the expiry, delete = remove the file too
	rpc(s, "artifacts.keep_many", func(ctx context.Context, r struct {
		IDs []int64 `json:"ids"`
	}) (int, error) {
		t, err := a.DB.Exec(ctx, `UPDATE artifacts SET expires_at=NULL WHERE id=ANY($1) AND expires_at IS NOT NULL AND expires_at>now()`, r.IDs)
		return int(t.RowsAffected()), err
	})
	rpc(s, "artifacts.delete_many", func(ctx context.Context, r struct {
		IDs []int64 `json:"ids"`
	}) (int, error) {
		rows, err := a.DB.Query(ctx, `DELETE FROM artifacts WHERE id=ANY($1) RETURNING path`, r.IDs)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				_ = os.Remove(p)
				n++
			}
		}
		return n, rows.Err()
	})
	rpc(s, "artifacts.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		var p string
		if err := a.DB.QueryRow(ctx, `DELETE FROM artifacts WHERE id=$1 RETURNING path`, r.ID).Scan(&p); err != nil {
			return false, err
		}
		_ = os.Remove(p)
		return true, nil
	})

	rpc(s, "notifications.list", func(ctx context.Context, r struct {
		Limit int `json:"limit"`
	}) (map[string]any, error) {
		items, unread, err := a.Notifs.List(ctx, r.Limit)
		return map[string]any{"items": items, "unread": unread}, err
	})
	rpc(s, "notifications.read", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Notifs.MarkRead(ctx, r.ID)
	})
	rpc(s, "notifications.clear", func(ctx context.Context, _ none) (bool, error) { return true, a.Notifs.Clear(ctx) })
	rpc(s, "maintenance.cleanup", func(ctx context.Context, _ none) (any, error) { return a.Cleanup(ctx) })

	type logRow struct {
		ID      int64  `json:"id"`
		TS      string `json:"ts"`
		Level   string `json:"level"`
		Source  string `json:"source"`
		Message string `json:"message"`
	}
	rpc(s, "logs.list", func(ctx context.Context, r struct {
		Limit int    `json:"limit"`
		Level string `json:"level"`
	}) ([]logRow, error) {
		if r.Limit <= 0 || r.Limit > 1000 {
			r.Limit = 200
		}
		rows, err := a.DB.Query(ctx, `SELECT id, to_char(ts,'YYYY-MM-DD"T"HH24:MI:SSOF'), level, source, message FROM logs
			WHERE ($1='' OR level=$1) ORDER BY id DESC LIMIT $2`, r.Level, r.Limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []logRow
		for rows.Next() {
			var x logRow
			if err := rows.Scan(&x.ID, &x.TS, &x.Level, &x.Source, &x.Message); err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, rows.Err()
	})

	// code workspaces: isolated worktrees where agents do coding work, reviewed here
	rpc(s, "workspaces.list", func(ctx context.Context, _ none) ([]builtin.Workspace, error) {
		return builtin.Workspaces(ctx, a.DB.Pool)
	})
	rpc(s, "workspaces.diff", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (string, error) {
		return builtin.WorkspaceDiff(ctx, a.DB.Pool, r.ID, false)
	})
	rpc(s, "workspaces.verify", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (string, error) {
		st, rep, err := builtin.VerifyWorkspace(ctx, a.DB.Pool, r.ID)
		return st + "\n" + rep, err
	})
	rpc(s, "workspaces.commands", func(ctx context.Context, r struct {
		Repo string `json:"repo"`
	}) (builtin.CodeCommands, error) {
		return builtin.Commands(ctx, a.DB.Pool, r.Repo)
	})
	rpc(s, "workspaces.set_commands", func(ctx context.Context, r builtin.CodeCommands) (bool, error) {
		return true, builtin.SetCommands(ctx, a.DB.Pool, r)
	})
	rpc(s, "workspaces.apply", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (string, error) {
		return builtin.WorkspaceApply(ctx, a.DB.Pool, r.ID)
	})
	rpc(s, "workspaces.keep", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (string, error) {
		return builtin.WorkspaceKeep(ctx, a.DB.Pool, r.ID)
	})
	rpc(s, "workspaces.discard", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, builtin.WorkspaceDiscard(ctx, a.DB.Pool, r.ID)
	})
}
