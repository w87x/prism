package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"prism/internal/browser"
	"prism/internal/docsearch"
	"prism/internal/foldermap"
	"prism/internal/ingest"
	"prism/internal/integrations/elevenlabs"
	"prism/internal/integrations/macos"
	"prism/internal/integrations/mail"
	"prism/internal/integrations/obsidian"
	"prism/internal/integrations/telegram"
	"prism/internal/kb"
	"prism/internal/mcp"
	"prism/internal/plugins"
	"prism/internal/scheduler"
	"prism/internal/settings"
	"prism/internal/skills"
	"prism/internal/tools/builtin"
	"prism/internal/tracker"
	"prism/internal/web"
)

// Extensions groups the optional subsystems.
type Extensions struct {
	Skills   *skills.Store
	Web      *web.Service
	Browser  *browser.Manager
	MCP      *mcp.Manager
	Sched    *scheduler.Service
	Vault    *obsidian.Vault
	Docs     *docsearch.Service
	Ingest   *ingest.Service
	Folders  *foldermap.Service
	Telegram *telegram.Bot
	KB       *kb.Service
	Voice    *elevenlabs.Service
	Cal      *macos.Helper
	Mail     *mail.Service
	Plugins  *plugins.Manager
	Trackers *tracker.Service
}

func (a *App) buildExtensions(ctx context.Context) error {
	x := &a.Ext

	// skills
	x.Skills = skills.NewStore(a.DB.Pool, a.Cfg.DataDir)
	skills.RegisterTools(a.Tools, x.Skills)
	skills.RegisterHubTools(a.Tools, x.Skills, skills.Adapter(a.LLM, func() []string {
		var out []string
		for _, t := range a.Tools.All() {
			if !t.Deferred {
				out = append(out, t.Name)
			}
		}
		return out
	}), func(ctx context.Context) string {
		return settings.Load(ctx, a.Settings, "skills", struct {
			GitHubToken string `json:"github_token"`
		}{}).GitHubToken
	})
	a.Engine.Skills = x.Skills

	// structured trackers
	x.Trackers = &tracker.Service{DB: a.DB.Pool, OnChange: func() { a.Emit("trackers.update", nil) }}
	tracker.RegisterTools(a.Tools, x.Trackers)

	// browser + web
	fsDeps := builtin.Deps{DB: a.DB.Pool, Settings: a.Settings, DataDir: a.Cfg.DataDir}
	x.Browser = &browser.Manager{Settings: a.Settings, DataDir: a.Cfg.DataDir, CanRead: fsDeps.CanReadPath}
	x.Browser.Save = func(ctx context.Context, name, mime string, data []byte, by string) (int64, string, error) {
		art, err := builtin.SaveArtifact(ctx, builtin.Deps{DB: a.DB.Pool, DataDir: a.Cfg.DataDir, Emit: a.Emit}, name, mime, data, by)
		if err != nil {
			return 0, "", err
		}
		return art.ID, art.Path, nil
	}
	x.Browser.RegisterTools(a.Tools)
	go func() { // a previous PRISM that died without closing its Chrome leaves it holding the profile
		if n := x.Browser.Reap(); n > 0 {
			a.Logf("info", "browser", "stopped %d orphaned Chrome process(es) from an earlier run", n)
		}
	}()
	fetcher := &web.Fetcher{Settings: a.Settings, Browser: x.Browser}
	x.Web = &web.Service{Settings: a.Settings, Fetcher: fetcher, Extract: &web.Extractor{LLM: a.LLM},
		Bookmarks: func(ctx context.Context, query string, limit int) []web.BookmarkHit {
			bs, err := builtin.FindBookmarks(ctx, a.DB.Pool, query, limit)
			if err != nil {
				return nil
			}
			out := make([]web.BookmarkHit, len(bs))
			for i, b := range bs {
				out[i] = web.BookmarkHit{Title: b.Title, URL: b.URL, Description: b.Description}
			}
			return out
		}}
	x.Web.RegisterTools(a.Tools)

	// MCP
	x.MCP = mcp.NewManager(a.DB.Pool, a.Tools)
	x.MCP.OnState = func() { a.Emit("mcp.update", nil) }
	x.MCP.AllowPrivate = func() bool {
		return settings.Load(context.Background(), a.Settings, settings.KeyWeb, settings.Web{}).AllowPrivate
	}

	// macOS + Obsidian + semantic search
	x.Cal = macos.NewHelper(a.Cfg.DataDir)
	macos.RegisterTools(a.Tools, a.Settings, x.Cal, a.Cfg.DataDir)
	a.Engine.Sinks = append(a.Engine.Sinks, &macos.Sink{Settings: a.Settings, Clients: a.Hub.NumClients})
	x.Vault = &obsidian.Vault{Settings: a.Settings}
	obsidian.RegisterTools(a.Tools, x.Vault)
	x.Docs = &docsearch.Service{DB: a.DB.Pool, VectorOn: a.DB.VectorOn, LLM: a.LLM, Emit: a.Emit,
		Extract: &docsearch.Extractor{Docs: docsearch.NewDocsHelper(a.Cfg.DataDir)}, InboxDir: filepath.Join(a.Cfg.DataDir, "inbox"), CanRead: fsDeps.CanReadPath}
	docsearch.RegisterTools(a.Tools, x.Docs)
	x.Ingest = &ingest.Service{DB: a.DB.Pool, Mem: a.Memory, LLM: a.LLM, Settings: a.Settings, Text: x.Docs.Extract.Text,
		InboxDir: x.Docs.InboxDir, CanRead: fsDeps.CanReadPath, Emit: a.Emit, Logf: a.Logf}
	x.Ingest.Recover(ctx)
	x.Folders = &foldermap.Service{DB: a.DB.Pool, LLM: a.LLM, Extract: x.Docs.Extract, CanRead: fsDeps.CanReadPath, Emit: a.Emit}
	x.Folders.Recover(ctx)
	foldermap.RegisterTools(a.Tools, x.Folders)

	// knowledge base
	x.KB = &kb.Service{DB: a.DB.Pool, Memory: a.Memory, LLM: a.LLM, Engine: a.Engine, Settings: a.Settings, Emit: a.Emit, Logf: a.Logf}
	x.KB.RegisterTools(a.Tools)

	// ElevenLabs: speech, sound effects, images
	x.Voice = &elevenlabs.Service{Settings: a.Settings, Save: func(ctx context.Context, name, mime string, data []byte, by string) (int64, error) {
		art, err := builtin.SaveArtifact(ctx, builtin.Deps{DB: a.DB.Pool, DataDir: a.Cfg.DataDir, Emit: a.Emit}, name, mime, data, by)
		if err != nil {
			return 0, err
		}
		return art.ID, nil
	}}
	elevenlabs.RegisterTools(a.Tools, x.Voice)

	// mail: several tagged accounts over PRISM's own IMAP/SMTP client or the himalaya CLI
	x.Mail = &mail.Service{Store: &mail.Store{DB: a.DB.Pool}}
	mail.RegisterTools(a.Tools, x.Mail)

	// runtime plugins: Python tools agents write and the user approves (sandboxed)
	x.Plugins = &plugins.Manager{DB: a.DB.Pool, Reg: a.Tools, DataDir: a.Cfg.DataDir, DenyReads: fsDeps.DenyRoots, Emit: a.Emit}
	plugins.RegisterTools(a.Tools, x.Plugins)
	if err := x.Plugins.Sync(ctx); err != nil {
		a.Logf("warn", "plugins", "cannot load plugins: %v", err)
	}

	// Telegram
	x.Telegram = &telegram.Bot{Settings: a.Settings, Engine: a.Engine, DB: a.DB.Pool, Logf: a.Logf, UploadDir: filepath.Join(a.Cfg.DataDir, "work", "uploads")}
	a.Engine.Sinks = append(a.Engine.Sinks, x.Telegram)

	// scheduler (autonomy)
	x.Sched = &scheduler.Service{DB: a.DB.Pool, Engine: a.Engine, Settings: a.Settings, Emit: a.Emit, Logf: a.Logf}
	x.Sched.Notify = a.Notify
	x.Sched.Env = scheduler.Env{
		LLM: a.LLM,
		FetchText: func(ctx context.Context, url string) (string, error) {
			p, err := fetcher.Fetch(ctx, url, "auto", "", 0)
			if err != nil {
				return "", err
			}
			body, _ := web.RenderText(p)
			if p.Status/100 != 2 {
				body = fmt.Sprintf("[status %d]\n%s", p.Status, body)
			}
			return body, nil
		},
		Search: func(ctx context.Context, q string) (string, error) {
			res, _, err := web.Search(ctx, settings.Load(ctx, a.Settings, settings.KeyWeb, settings.Web{}), q, 6)
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			for _, r := range res {
				fmt.Fprintf(&sb, "%s\n%s\n%s\n\n", r.Title, r.URL, r.Snippet)
			}
			return sb.String(), nil
		},
		Feed: func(ctx context.Context, url string) ([]scheduler.FeedItem, error) {
			_, items, err := builtin.ReadFeedAllow(ctx, url, 50, settings.Load(ctx, a.Settings, settings.KeyWeb, settings.Web{}).AllowPrivate)
			var out []scheduler.FeedItem
			for _, it := range items {
				out = append(out, scheduler.FeedItem{ID: it.ID, Title: it.Title, Link: it.Link})
			}
			return out, err
		},
		Download: func(ctx context.Context, id int64) (string, int64, int64, error) {
			d, err := a.Downloads.Get(ctx, id)
			return d.Status, d.Bytes, d.Total, err
		},
	}
	x.Sched.RegisterTools(a.Tools)
	return x.Sched.SeedDefaults(ctx)
}

func (a *App) startExtensions(ctx context.Context) {
	x := &a.Ext
	x.MCP.Start(ctx)
	go x.Telegram.Run(ctx)
	x.Sched.Start(ctx)
	x.KB.Start(ctx)
	go func() { // the Inbox exists from the start; watched sources are re-indexed when their files change
		if _, err := x.Docs.EnsureInbox(ctx); err != nil {
			a.Logf("warn", "docs", "cannot create the inbox: %v", err)
		}
		x.Docs.WatchLoop(ctx, 90*time.Second)
	}()
}

func (a *App) stopExtensions() {
	if a.Ext.MCP != nil {
		a.Ext.MCP.Stop()
	}
	if a.Ext.Browser != nil {
		a.Ext.Browser.Stop()
	}
}

func (a *App) extensionLEDs(ctx context.Context) []LED {
	x := &a.Ext
	var out []LED
	bs, bd := x.Browser.State()
	out = append(out, LED{ID: "browser", Label: "BROWSER", State: bs, Detail: bd})
	ts, td := x.Telegram.State()
	out = append(out, LED{ID: "telegram", Label: "TG", State: ts, Detail: td})
	if c, tot := x.MCP.Counts(); tot > 0 {
		st := "ok"
		if c < tot {
			st = "warn"
		}
		out = append(out, LED{ID: "mcp", Label: "MCP", State: st, Detail: fmt.Sprintf("%d/%d servers", c, tot)})
	}
	if ok, d := x.Vault.Configured(ctx); ok {
		out = append(out, LED{ID: "obsidian", Label: "OBSIDIAN", State: "standby", Detail: d})
	}
	au := settings.Load(ctx, a.Settings, settings.KeyAutonomy, settings.Autonomy{Enabled: true, DreamEnabled: true})
	st := "ok"
	if !au.Enabled {
		st = "off"
	}
	out = append(out, LED{ID: "auto", Label: "AUTO", State: st, Detail: "cron · intents · dreams"})
	_ = time.Now
	return out
}
