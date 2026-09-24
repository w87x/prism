package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/agent"
	"prism/internal/docsearch"
	"prism/internal/foldermap"
	"prism/internal/ingest"
	"prism/internal/integrations/elevenlabs"
	"prism/internal/integrations/macos"
	"prism/internal/integrations/mail"
	"prism/internal/integrations/obsidian"
	"prism/internal/kb"
	"prism/internal/mcp"
	"prism/internal/memory"
	"prism/internal/onboarding"
	"prism/internal/scheduler"
	"prism/internal/settings"
	"prism/internal/skills"
	"prism/internal/web"
)

// SkillsConfig (settings key "skills").
type SkillsConfig struct {
	GitHubToken string `json:"github_token"`
}

func (s *Server) registerExtensions() {
	s.registerSkills()
	s.registerMCP()
	s.registerWeb()
	s.registerAutonomy()
	s.registerIntegrations()
	s.registerOnboarding()
	s.registerKB()
	s.registerTrackers()
	s.registerSearch()
}

func (s *Server) registerSkills() {
	a := s.App
	sk := func() *skills.Store { return a.Ext.Skills }
	toolNames := func() []string {
		var out []string
		for _, t := range a.Tools.All() {
			if !t.Deferred {
				out = append(out, t.Name)
			}
		}
		return out
	}
	token := func(ctx context.Context) string {
		return settings.Load(ctx, a.Settings, "skills", SkillsConfig{}).GitHubToken
	}
	rpc(s, "skills.list", func(ctx context.Context, _ none) ([]skills.Skill, error) { return sk().List(ctx) })
	rpc(s, "skills.get", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (*skills.Skill, error) {
		return sk().GetID(ctx, r.ID)
	})
	rpc(s, "skills.save", func(ctx context.Context, k skills.Skill) (int64, error) { return sk().Save(ctx, k) })
	rpc(s, "skills.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, sk().Delete(ctx, r.ID)
	})
	// adapt rewrites a skill in place for PRISM, keeping the original for revert
	rpc(s, "skills.adapt", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (*skills.Skill, error) {
		k, err := sk().GetID(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		src := k.Body
		if k.Original != "" {
			src = k.Original
		}
		out, err := skills.Adapter(a.LLM, toolNames)(ctx, src)
		if err != nil {
			return nil, err
		}
		k.Original, k.Body, k.Adapted = src, out, true
		if _, fm := skills.ParseFrontmatter(out); fm["description"] != "" {
			k.Description = fm["description"]
		}
		if _, err := sk().Save(ctx, *k); err != nil {
			return nil, err
		}
		return sk().GetID(ctx, r.ID)
	})
	rpc(s, "skills.revert", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (*skills.Skill, error) {
		k, err := sk().GetID(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		if k.Original == "" {
			return k, errors.New("this skill has no original to restore")
		}
		k.Body, k.Original, k.Adapted = k.Original, "", false
		if _, err := sk().Save(ctx, *k); err != nil {
			return nil, err
		}
		return sk().GetID(ctx, r.ID)
	})
	rpc(s, "skills.hubs", func(ctx context.Context, _ none) ([]skills.Hub, error) { return sk().Hubs(ctx) })
	rpc(s, "skills.hub_save", func(ctx context.Context, h skills.Hub) (int64, error) { return sk().SaveHub(ctx, h) })
	rpc(s, "skills.hub_delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, sk().DeleteHub(ctx, r.ID)
	})
	rpc(s, "skills.hub_browse", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) ([]skills.Remote, error) {
		h, err := sk().GetHub(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		return sk().Browse(cctx, *h, token(ctx))
	})
	rpc(s, "skills.hub_search", func(ctx context.Context, r struct {
		Query string `json:"query"`
	}) ([]skills.Hit, error) {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return sk().SearchHubs(cctx, r.Query, token(ctx), false, 40)
	})
	rpc(s, "skills.hub_import", func(ctx context.Context, r struct {
		HubID int64  `json:"hub_id"`
		Path  string `json:"path"`
		Adapt bool   `json:"adapt"`
	}) (*skills.Skill, error) {
		h, err := sk().GetHub(ctx, r.HubID)
		if err != nil {
			return nil, err
		}
		var adapt func(context.Context, string) (string, error)
		if r.Adapt {
			adapt = skills.Adapter(a.LLM, toolNames)
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return sk().Import(cctx, *h, r.Path, token(ctx), adapt)
	})
}

func (s *Server) registerMCP() {
	a := s.App
	type row struct {
		mcp.Server
		Status mcp.Status `json:"status"`
	}
	rpc(s, "mcp.list", func(ctx context.Context, _ none) ([]row, error) {
		srvs, err := a.Ext.MCP.List(ctx)
		if err != nil {
			return nil, err
		}
		st := a.Ext.MCP.Statuses()
		out := make([]row, 0, len(srvs))
		for _, sv := range srvs {
			r := row{Server: sv, Status: st[sv.ID]}
			if r.Status.State == "" {
				r.Status.State = map[bool]string{true: "connecting", false: "disabled"}[sv.Enabled]
			}
			out = append(out, r)
		}
		return out, nil
	})
	rpc(s, "mcp.save", func(ctx context.Context, sv mcp.Server) (int64, error) {
		id, err := a.Ext.MCP.Save(ctx, sv)
		if err != nil {
			return 0, err
		}
		go func() { _ = a.Ext.MCP.Reload(context.Background(), id) }() // connect in the background (npx may download)
		return id, nil
	})
	rpc(s, "mcp.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Ext.MCP.Delete(ctx, r.ID)
	})
	// sign-in for servers that use OAuth: returns the address to open in the browser; the callback is /oauth/callback
	rpc(s, "mcp.oauth_start", func(ctx context.Context, r struct {
		ID     int64  `json:"id"`
		Origin string `json:"origin"` // where the user reaches PRISM (the callback lands there)
	}) (string, error) {
		return a.Ext.MCP.OAuthStart(ctx, r.ID, r.Origin)
	})
	rpc(s, "mcp.oauth_signout", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Ext.MCP.OAuthSignOut(ctx, r.ID)
	})
	rpc(s, "mcp.reload", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		go func() { _ = a.Ext.MCP.Reload(context.Background(), r.ID) }()
		return true, nil
	})
}

func (s *Server) registerWeb() {
	a := s.App
	rpc(s, "web.providers", func(ctx context.Context, _ none) (map[string]any, error) {
		cfg := settings.Load(ctx, a.Settings, settings.KeyWeb, settings.Web{})
		if len(cfg.SearchOrder) == 0 {
			cfg.SearchOrder = web.DefaultOrder()
		}
		return map[string]any{"providers": web.ProviderList(cfg), "config": cfg}, nil
	})
	rpc(s, "web.search", func(ctx context.Context, r struct {
		Query string `json:"query"`
	}) (map[string]any, error) {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		res, used, err := web.Search(cctx, settings.Load(ctx, a.Settings, settings.KeyWeb, settings.Web{}), r.Query, 6)
		if err != nil {
			return nil, err
		}
		return map[string]any{"provider": used, "results": res}, nil
	})
	rpc(s, "browser.status", func(ctx context.Context, _ none) (map[string]any, error) {
		st, d := a.Ext.Browser.State()
		return map[string]any{"state": st, "detail": d, "available": a.Ext.Browser.Available()}, nil
	})
	rpc(s, "browser.stop", func(ctx context.Context, _ none) (bool, error) { a.Ext.Browser.Stop(); return true, nil })
	rpc(s, "browser.test", func(ctx context.Context, r struct {
		URL string `json:"url"`
	}) (map[string]any, error) {
		if r.URL == "" {
			r.URL = "https://example.com"
		}
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		start := time.Now()
		html, err := a.Ext.Browser.Render(cctx, r.URL, "", 0)
		if err != nil {
			return nil, err
		}
		doc, _ := web.Parse(html)
		return map[string]any{"title": web.Title(doc), "bytes": len(html), "ms": time.Since(start).Milliseconds()}, nil
	})
}

func (s *Server) registerAutonomy() {
	a := s.App
	sc := func() *scheduler.Service { return a.Ext.Sched }
	rpc(s, "crons.list", func(ctx context.Context, _ none) ([]scheduler.Cron, error) { return sc().Crons(ctx) })
	rpc(s, "crons.save", func(ctx context.Context, c scheduler.Cron) (int64, error) { return sc().SaveCron(ctx, c) })
	rpc(s, "crons.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, sc().DeleteCron(ctx, r.ID)
	})
	rpc(s, "crons.run", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, sc().RunCronNow(ctx, r.ID)
	})
	rpc(s, "intents.list", func(ctx context.Context, r struct {
		Status string `json:"status"`
	}) ([]scheduler.Intent, error) {
		return sc().Intents(ctx, r.Status)
	})
	rpc(s, "intents.create", func(ctx context.Context, r struct {
		Owner       string              `json:"owner"`
		Description string              `json:"description"`
		Type        string              `json:"type"`
		CadenceS    int                 `json:"cadence_s"`
		Repeat      bool                `json:"repeat"`
		Predicate   scheduler.Predicate `json:"predicate"`
	}) (int64, error) {
		if r.Owner == "" {
			r.Owner = "Atlas"
		}
		return sc().CreateIntent(ctx, scheduler.Intent{Owner: r.Owner, Description: r.Description, Type: r.Type, CadenceS: r.CadenceS, Repeat: r.Repeat, Notify: true}, r.Predicate)
	})
	rpc(s, "intents.update", func(ctx context.Context, r struct {
		ID       int64  `json:"id"`
		Status   string `json:"status"`
		CadenceS *int   `json:"cadence_s"`
	}) (bool, error) {
		return true, sc().UpdateIntent(ctx, r.ID, r.Status, r.CadenceS)
	})
	// monitors: give a watch more (or, negative, less) time; wakes an expired one
	rpc(s, "intents.extend", func(ctx context.Context, r struct {
		ID      int64 `json:"id"`
		Minutes int   `json:"minutes"`
	}) (bool, error) {
		return true, sc().ExtendIntent(ctx, r.ID, r.Minutes)
	})
	rpc(s, "intents.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, sc().DeleteIntent(ctx, r.ID)
	})
	rpc(s, "briefings.list", func(ctx context.Context, r struct {
		Status string `json:"status"`
	}) ([]scheduler.Briefing, error) {
		return sc().Briefings(ctx, r.Status)
	})
	rpc(s, "bookmarks.harvest", func(ctx context.Context, _ none) (int, error) {
		n, err := a.HarvestBookmarks(ctx)
		if err == nil && n > 0 {
			a.Logf("info", "memory", "bookmarked %d page(s) the agents visited (manual)", n)
		}
		return n, err
	})
	rpc(s, "briefings.reply", func(ctx context.Context, r struct {
		ID   int64  `json:"id"`
		Text string `json:"text"`
	}) (bool, error) {
		_, err := sc().ReplyBriefing(ctx, r.ID, r.Text)
		return err == nil, err
	})
	rpc(s, "briefings.status", func(ctx context.Context, r struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}) (bool, error) {
		return true, sc().SetBriefingStatus(ctx, r.ID, r.Status)
	})
	rpc(s, "briefings.save_obsidian", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (string, error) {
		b, err := sc().Briefing(ctx, r.ID)
		if err != nil {
			return "", err
		}
		content := fmt.Sprintf("---\nagent: %s\ndate: %s\nimportance: %d\n---\n\n# %s\n\n%s\n", b.Agent, b.CreatedAt.Format("2006-01-02"), b.Importance, b.Title, b.Body)
		rel, err := a.Ext.Vault.WriteNote(ctx, "Briefings/"+b.CreatedAt.Format("2006-01-02")+" "+obsidianSafeTitle(b.Title), content, false)
		if err != nil {
			return "", err
		}
		if b.Status == "new" {
			_ = sc().SetBriefingStatus(ctx, r.ID, "delivered")
		}
		return rel, nil
	})
	rpc(s, "dream.now", func(ctx context.Context, _ none) (bool, error) {
		cs, err := sc().Crons(ctx)
		if err != nil {
			return false, err
		}
		for _, c := range cs {
			if c.System && c.Agent == "Oneiros" {
				return true, sc().RunCronNow(ctx, c.ID)
			}
		}
		return false, errors.New("dream schedule not found")
	})
	rpc(s, "autonomy.audit", func(ctx context.Context, r struct {
		Limit int `json:"limit"`
	}) ([]scheduler.AuditEntry, error) {
		return sc().Audit(ctx, r.Limit)
	})
}

func (s *Server) registerIntegrations() {
	a := s.App
	rpc(s, "telegram.status", func(ctx context.Context, _ none) (map[string]any, error) {
		st, d := a.Ext.Telegram.State()
		cfg := settings.Load(ctx, a.Settings, settings.KeyTelegram, settings.Telegram{MirrorNotices: true})
		rows, _ := a.DB.Query(ctx, `SELECT chat_id, thread_id, name, purpose, created_by FROM telegram_topics ORDER BY id DESC LIMIT 50`)
		type topic struct {
			ChatID    int64  `json:"chat_id"`
			ThreadID  int64  `json:"thread_id"`
			Name      string `json:"name"`
			Purpose   string `json:"purpose"`
			CreatedBy string `json:"created_by"`
		}
		var topics []topic
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var t topic
				if rows.Scan(&t.ChatID, &t.ThreadID, &t.Name, &t.Purpose, &t.CreatedBy) == nil {
					topics = append(topics, t)
				}
			}
		}
		return map[string]any{"state": st, "detail": d, "paired": cfg.OwnerID != 0, "owner_id": cfg.OwnerID, "group_id": cfg.GroupID, "pair_code": cfg.PairCode, "topics": topics}, nil
	})
	rpc(s, "telegram.pair", func(ctx context.Context, _ none) (string, error) { return a.Ext.Telegram.NewPairCode(ctx) })
	rpc(s, "telegram.unpair", func(ctx context.Context, _ none) (bool, error) {
		c := settings.Load(ctx, a.Settings, settings.KeyTelegram, settings.Telegram{MirrorNotices: true})
		c.OwnerID, c.PairCode = 0, ""
		return true, a.Settings.Set(ctx, settings.KeyTelegram, c)
	})
	rpc(s, "telegram.test", func(ctx context.Context, r struct {
		Token string `json:"token"`
	}) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return a.Ext.Telegram.Test(cctx, r.Token)
	})

	// ElevenLabs: test a key (saved or typed), read the plan/credits, list voices
	rpc(s, "elevenlabs.status", func(ctx context.Context, r struct {
		Key string `json:"key"`
	}) (map[string]any, error) {
		key := strings.TrimSpace(r.Key)
		if key == "" && !a.Ext.Voice.Configured(ctx) {
			return map[string]any{"configured": false}, nil
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		sub, err := a.Ext.Voice.Subscription(cctx, key)
		if err != nil {
			return map[string]any{"configured": true, "ok": false, "error": err.Error()}, nil
		}
		return map[string]any{"configured": true, "ok": true, "tier": sub.Tier, "used": sub.Used, "limit": sub.Limit, "reset_unix": sub.ResetUnix}, nil
	})
	rpc(s, "elevenlabs.voices", func(ctx context.Context, r struct {
		Key string `json:"key"`
	}) ([]elevenlabs.Voice, error) {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		vs, err := a.Ext.Voice.Voices(cctx, strings.TrimSpace(r.Key))
		if vs == nil {
			vs = []elevenlabs.Voice{}
		}
		return vs, err
	})

	rpc(s, "obsidian.detect", func(ctx context.Context, _ none) ([]string, error) { return obsidian.Detect(), nil })
	rpc(s, "obsidian.status", func(ctx context.Context, _ none) (map[string]any, error) {
		ok, d := a.Ext.Vault.Configured(ctx)
		return map[string]any{"configured": ok, "detail": d}, nil
	})
	// obsidian.set binds the vault and registers it as a semantic-search source
	rpc(s, "obsidian.set", func(ctx context.Context, r struct {
		Path string `json:"path"`
	}) (map[string]any, error) {
		path := strings.TrimSpace(r.Path)
		if err := a.Settings.Set(ctx, settings.KeyObsidian, settings.Obsidian{VaultPath: path}); err != nil {
			return nil, err
		}
		if path == "" {
			return map[string]any{"configured": false}, nil
		}
		ok, d := a.Ext.Vault.Configured(ctx)
		if !ok {
			return nil, errors.New(d)
		}
		if _, err := a.Ext.Docs.SaveSource(ctx, docsearch.Source{Name: "obsidian", Path: path, Globs: []string{"*.md"}}); err != nil {
			return nil, err
		}
		return map[string]any{"configured": true, "path": d}, nil
	})
	// ── mail accounts ──
	rpc(s, "mail.accounts", func(ctx context.Context, _ none) ([]mail.Account, error) { return a.Ext.Mail.Store.List(ctx) })
	rpc(s, "mail.save", func(ctx context.Context, x mail.Account) (int64, error) { return a.Ext.Mail.Store.Save(ctx, x) })
	rpc(s, "mail.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Ext.Mail.Store.Delete(ctx, r.ID)
	})
	rpc(s, "mail.himalaya", func(ctx context.Context, _ none) (map[string]any, error) {
		if !mail.HimalayaAvailable() {
			return map[string]any{"installed": false, "accounts": []string{}}, nil
		}
		names, err := mail.HimalayaAccounts(ctx)
		if names == nil {
			names = []string{}
		}
		res := map[string]any{"installed": true, "accounts": names}
		if err != nil {
			res["error"] = err.Error()
		}
		return res, nil
	})
	// test connects with the form's values (a saved account's stored password is used when the field is blank)
	rpc(s, "mail.test", func(ctx context.Context, x mail.Account) (map[string]any, error) {
		if x.ID != 0 {
			if old, err := a.Ext.Mail.Store.Get(ctx, x.Tag); err == nil && old.ID == x.ID {
				if x.Password == "" {
					x.Password = old.Password
				}
				if x.SMTPPassword == "" {
					x.SMTPPassword = old.SMTPPassword
				}
			}
		}
		be, err := mail.NewBackend(x)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		fs, err := be.Folders(cctx)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		unread := 0
		for _, f := range fs {
			if f.Role == "inbox" {
				unread = f.Unread
			}
		}
		return map[string]any{"ok": true, "folders": fs, "unread": unread}, nil
	})

	// Calendar & Reminders: the first call compiles the helper and makes macOS ask for permission
	rpc(s, "calendar.check", func(ctx context.Context, _ none) (map[string]any, error) {
		if !macos.Available() {
			return map[string]any{"ok": false, "error": "only available on macOS"}, nil
		}
		var r struct {
			Calendars []struct {
				Title    string `json:"title"`
				Kind     string `json:"kind"`
				Writable bool   `json:"writable"`
				Source   string `json:"source"`
			} `json:"calendars"`
		}
		if err := a.Ext.Cal.Run(ctx, "cals", nil, &r); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		return map[string]any{"ok": true, "calendars": r.Calendars}, nil
	})
	rpc(s, "shortcuts.list", func(ctx context.Context, _ none) ([]string, error) {
		if !macos.Available() {
			return []string{}, nil
		}
		names, err := macos.ShortcutNames(ctx)
		if names == nil {
			names = []string{}
		}
		return names, err
	})
	rpc(s, "macos.test", func(ctx context.Context, _ none) (bool, error) {
		if !macos.Available() {
			return false, errors.New("not running on macOS")
		}
		return true, macos.Notify(ctx, "PRISM", "Notifications are working.", "Glass")
	})

	// documents dropped onto the Library page: stored in the Inbox and indexed right away
	rpc(s, "docs.upload", func(ctx context.Context, r struct {
		Name string `json:"name"`
		Data []byte `json:"data"`
	}) (map[string]any, error) {
		name, chunks, err := a.Ext.Docs.Ingest(ctx, r.Name, r.Data)
		if err != nil {
			return nil, err
		}
		return map[string]any{"name": name, "chunks": chunks}, nil
	})
	// teaching documents to memory (chat exports, notes, articles…)
	rpc(s, "ingest.files", func(ctx context.Context, _ none) ([]ingest.File, error) { return a.Ext.Ingest.Files(ctx) })
	rpc(s, "ingest.upload", func(ctx context.Context, r struct {
		Name string `json:"name"`
		Data []byte `json:"data"`
	}) (string, error) {
		return a.Ext.Ingest.Save(r.Name, r.Data)
	})
	rpc(s, "ingest.inspect", func(ctx context.Context, r struct {
		Ref string `json:"ref"`
	}) (map[string]any, error) {
		info, err := a.Ext.Ingest.Inspect(ctx, r.Ref)
		if err != nil {
			return nil, err
		}
		return map[string]any{"info": info, "me": a.Ext.Ingest.GuessMe(ctx, info)}, nil
	})
	rpc(s, "ingest.delete", func(ctx context.Context, r struct {
		Name   string `json:"name"`
		Forget bool   `json:"forget"`
	}) (bool, error) {
		return true, a.Ext.Ingest.Delete(ctx, r.Name, r.Forget)
	})
	rpc(s, "ingest.forget_job", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Ext.Ingest.ForgetJob(ctx, r.ID)
	})
	rpc(s, "ingest.start", func(ctx context.Context, r ingest.StartReq) (*ingest.Job, error) { return a.Ext.Ingest.Start(ctx, r) })
	rpc(s, "ingest.jobs", func(ctx context.Context, _ none) ([]ingest.Job, error) { return a.Ext.Ingest.Jobs(ctx) })
	rpc(s, "ingest.cancel", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Ext.Ingest.Cancel(ctx, r.ID)
	})
	rpc(s, "ingest.resume", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Ext.Ingest.Resume(ctx, r.ID)
	})
	// folder maps: per-file summaries of attached folders
	rpc(s, "foldermap.list", func(ctx context.Context, _ none) ([]foldermap.Map, error) { return a.Ext.Folders.List(ctx) })
	rpc(s, "foldermap.entries", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) ([]foldermap.Entry, error) {
		return a.Ext.Folders.Entries(ctx, r.ID)
	})
	rpc(s, "foldermap.build", func(ctx context.Context, r struct {
		Path string `json:"path"`
	}) (foldermap.Map, error) {
		return a.Ext.Folders.Build(ctx, r.Path)
	})
	rpc(s, "foldermap.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Ext.Folders.Delete(ctx, r.ID)
	})
	rpc(s, "docs.sources", func(ctx context.Context, _ none) ([]docsearch.Source, error) { return a.Ext.Docs.Sources(ctx) })
	rpc(s, "docs.save", func(ctx context.Context, x docsearch.Source) (int64, error) { return a.Ext.Docs.SaveSource(ctx, x) })
	rpc(s, "docs.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Ext.Docs.DeleteSource(ctx, r.ID)
	})
	rpc(s, "docs.index", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		go func() { // embedding a big vault takes a while: report progress via events
			bg := context.Background()
			f, c, err := a.Ext.Docs.Index(bg, r.ID, func(done, total int) {
				a.Emit("docs.progress", map[string]any{"id": r.ID, "done": done, "total": total})
			})
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			a.Emit("docs.indexed", map[string]any{"id": r.ID, "files": f, "chunks": c, "error": msg})
		}()
		return true, nil
	})
	rpc(s, "docs.search", func(ctx context.Context, r struct {
		Query  string `json:"query"`
		Source string `json:"source"`
	}) ([]docsearch.Hit, error) {
		return a.Ext.Docs.Search(ctx, r.Query, r.Source, 8)
	})
}

func (s *Server) registerOnboarding() {
	a := s.App
	rpc(s, "onboarding.state", func(ctx context.Context, _ none) (map[string]any, error) {
		ob := settings.Load(ctx, a.Settings, settings.KeyOnboarding, settings.Onboarding{})
		g := settings.Load(ctx, a.Settings, settings.KeyGeneral, settings.General{})
		roles := settings.Load(ctx, a.Settings, settings.KeyModelRoles, settings.ModelRoles{})
		ps, _ := a.Profiles.List(ctx)
		users := 0
		for _, p := range ps {
			if !p.System {
				users++
			}
		}
		return map[string]any{"done": ob.Done, "hints": ob.Hints, "general": g, "has_chat_model": a.LLM.HasChat(ctx), "roles": roles, "user_agents": users}, nil
	})
	rpc(s, "onboarding.templates", func(ctx context.Context, _ none) ([]onboarding.Draft, error) {
		ps, _ := a.Profiles.List(ctx)
		var names []string
		for _, p := range ps {
			names = append(names, p.Name)
		}
		ds := onboarding.Templates()
		for i := range ds {
			for _, n := range names {
				if strings.EqualFold(n, ds[i].Name) {
					ds[i].Exists = true
				}
			}
		}
		return ds, nil
	})
	// onboarding.propose runs generation as a background job and streams drafts via
	// "onboarding.progress" events, because local models can take minutes.
	rpc(s, "onboarding.propose", func(ctx context.Context, r struct {
		Hints string `json:"hints"`
		Count int    `json:"count"`
		Model string `json:"model"`
	}) (map[string]any, error) {
		ps, _ := a.Profiles.List(ctx)
		var names []string
		for _, p := range ps {
			names = append(names, p.Name)
		}
		job := time.Now().UnixNano()
		go func() {
			bg := context.Background()
			drafts, used, err := onboarding.Propose(bg, a.LLM, a.Tools, names, r.Hints, r.Count, r.Model, func(p onboarding.Progress) {
				a.Emit("onboarding.progress", map[string]any{"job": job, "stage": p.Stage, "note": p.Note, "draft": p.Draft, "total": p.Total})
			})
			ev := map[string]any{"job": job, "stage": "finished", "drafts": drafts, "generated": used}
			if err != nil {
				ev["note"] = err.Error()
			}
			a.Emit("onboarding.progress", ev)
		}()
		return map[string]any{"job": job}, nil
	})
	rpc(s, "onboarding.apply", func(ctx context.Context, r struct {
		Drafts  []onboarding.Draft `json:"drafts"`
		Replace bool               `json:"replace"`
	}) (int, error) {
		return onboarding.Apply(ctx, a.Profiles, r.Drafts, r.Replace)
	})
	rpc(s, "onboarding.finish", func(ctx context.Context, r struct {
		Hints string `json:"hints"`
	}) (bool, error) {
		if err := a.Settings.Set(ctx, settings.KeyOnboarding, settings.Onboarding{Done: true, Hints: r.Hints}); err != nil {
			return false, err
		}
		// the user's own words become facts through the regular raw → facts pipeline
		if strings.TrimSpace(r.Hints) != "" {
			_ = a.Memory.AddRaw(ctx, memoryRaw("user", "Atlas", "About me (onboarding): "+r.Hints))
			go func() { _, _ = a.Memory.Process(context.Background(), 20, true) }()
		}
		g := settings.Load(ctx, a.Settings, settings.KeyGeneral, settings.General{})
		hello := "Hi"
		if g.UserName != "" {
			hello += ", " + g.UserName
		}
		a.Engine.Greet(ctx, hello+"! I'm Atlas — your single point of contact. Tell me what you need: I'll answer right away or bring in the right specialists and report back. What shall we start with?")
		a.Emit("status", a.Status(ctx))
		return true, nil
	})
	rpc(s, "onboarding.reset", func(ctx context.Context, _ none) (bool, error) {
		return true, a.Settings.Set(ctx, settings.KeyOnboarding, settings.Onboarding{Done: false})
	})
	// start over: scope "data" keeps API keys/integrations, "all" wipes them too (see maint.Reset)
	rpc(s, "onboarding.recreate", func(ctx context.Context, r struct {
		Scope string `json:"scope"`
	}) (bool, error) {
		if r.Scope != "data" && r.Scope != "all" {
			return false, errors.New(`scope must be "data" or "all"`)
		}
		return true, a.ResetData(ctx, r.Scope)
	})
	_ = agent.RoleEntry
}

func memoryRaw(from, to, text string) memory.RawMsg {
	return memory.RawMsg{From: from, To: to, Channel: "onboarding", Text: text, Agent: to}
}

func (s *Server) registerKB() {
	a := s.App
	k := func() *kb.Service { return a.Ext.KB }
	rpc(s, "kb.tree", func(ctx context.Context, _ none) (map[string]any, error) { return k().Tree(ctx) })
	rpc(s, "kb.page_get", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (kb.Page, error) {
		return k().GetPage(ctx, r.ID)
	})
	rpc(s, "kb.page_save", func(ctx context.Context, p kb.Page) (int64, error) { return k().SavePage(ctx, p) })
	rpc(s, "kb.page_delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, k().DeletePage(ctx, r.ID)
	})
	// generation can take minutes on local models: run it in the background, progress arrives as kb.update events
	rpc(s, "kb.page_generate", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		go func() {
			if err := k().Generate(context.Background(), r.ID); err != nil {
				a.Emit("toast", map[string]any{"level": "err", "text": "Knowledge page failed: " + err.Error()})
			}
		}()
		return true, nil
	})
	rpc(s, "kb.folder_save", func(ctx context.Context, f kb.Folder) (int64, error) { return k().SaveFolder(ctx, f) })
	rpc(s, "kb.folder_delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, k().DeleteFolder(ctx, r.ID)
	})
	// folder_expand: a smart folder proposes its pages, then they are generated one by one
	rpc(s, "kb.folder_expand", func(ctx context.Context, r struct {
		ID       int64 `json:"id"`
		Generate bool  `json:"generate"`
	}) (bool, error) {
		go func() {
			bg := context.Background()
			pages, err := k().Expand(bg, r.ID)
			if err != nil {
				a.Emit("toast", map[string]any{"level": "err", "text": "Smart folder: " + err.Error()})
				return
			}
			if r.Generate {
				for _, p := range pages {
					_ = k().Generate(bg, p.ID)
				}
			}
		}()
		return true, nil
	})
}
