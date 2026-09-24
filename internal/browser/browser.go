// Package browser drives Chrome through the DevTools protocol (chromedp): it
// renders JS-heavy pages for web_fetch and exposes interactive browser tools.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"prism/internal/netguard"
	"prism/internal/settings"
	"prism/internal/tools"
)

// SaveFunc stores a binary artifact and returns its id and path.
type SaveFunc func(ctx context.Context, name, mime string, data []byte, by string) (int64, string, error)

// tabHandle is one named, addressable browser tab.
type tabHandle struct {
	ctx    context.Context
	cancel context.CancelFunc
	opened time.Time
}

type Manager struct {
	Settings *settings.Store
	DataDir  string
	Save     SaveFunc
	// CanRead gates the local file path browser_upload may read (wired to the same policy file_read etc.
	// use). Nil: uploads are refused (no policy to check against).
	CanRead func(ctx context.Context, path string) error

	mu      sync.Mutex
	alloc   context.CancelFunc
	browser context.Context
	bcancel context.CancelFunc
	tabs    map[string]*tabHandle // tab id -> handle; see resolveTabID
	lastErr string
	started time.Time
}

var macChrome = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
	"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
}

func (m *Manager) chromeBin(cfg settings.Browser) string {
	if cfg.ChromeBin != "" {
		return cfg.ChromeBin
	}
	for _, p := range macChrome {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, n := range []string{"google-chrome", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// Available reports whether a browser can be started or attached.
func (m *Manager) Available() bool {
	cfg := settings.Load(context.Background(), m.Settings, settings.KeyBrowser, settings.Browser{Headless: true})
	return cfg.RemoteURL != "" || m.chromeBin(cfg) != ""
}

// State is used by the status bar: off (never started / no browser), standby (installed, idle), ok (connected), error.
func (m *Manager) State() (state, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.browser != nil && m.browser.Err() == nil:
		n := len(m.tabs)
		d := "connected"
		if n > 0 {
			d = fmt.Sprintf("connected · %d tab(s)", n)
		}
		return "ok", d
	case m.lastErr != "":
		return "error", m.lastErr
	case m.Available():
		return "standby", "not started"
	}
	return "off", "no Chrome found"
}

func (m *Manager) profileDir() string { return filepath.Join(m.DataDir, "browser-profile") }

// Reap stops a Chrome left behind on PRISM's profile by an earlier PRISM (see Reap). Safe to call at any time:
// it does nothing while this manager's own browser is running.
func (m *Manager) Reap() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browser != nil && m.browser.Err() == nil {
		return 0
	}
	n, _ := Reap(m.profileDir())
	if n > 0 {
		m.lastErr = "" // the failure that made the status light red is gone with the orphan
	}
	return n
}

func (m *Manager) start() error {
	if m.browser != nil && m.browser.Err() == nil {
		return nil
	}
	cfg := settings.Load(context.Background(), m.Settings, settings.KeyBrowser, settings.Browser{Headless: true})
	if cfg.RemoteURL == "" && m.chromeBin(cfg) != "" {
		_, _ = Reap(m.profileDir()) // an orphan on our profile would make Chrome refuse to start
	}
	err := m.launch(cfg)
	if err != nil && cfg.RemoteURL == "" && strings.Contains(err.Error(), "SingletonLock") {
		if n, _ := Reap(m.profileDir()); n > 0 { // raced with a fresh orphan: clear it and try once more
			err = m.launch(cfg)
		}
	}
	if err != nil {
		m.lastErr = oneLine(err)
		return fmt.Errorf("could not start the browser: %w", err)
	}
	return nil
}

func (m *Manager) launch(cfg settings.Browser) error {
	var alloc context.Context
	var acancel context.CancelFunc
	if cfg.RemoteURL != "" {
		alloc, acancel = chromedp.NewRemoteAllocator(context.Background(), cfg.RemoteURL)
	} else {
		bin := m.chromeBin(cfg)
		if bin == "" {
			return errors.New("no Chrome/Chromium installation found; set the path or a remote DevTools URL in Settings → Browser")
		}
		profile := m.profileDir()
		_ = os.MkdirAll(profile, 0o755)
		opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
		opts = append(opts,
			chromedp.ExecPath(bin),
			chromedp.UserDataDir(profile),
			chromedp.Flag(ownerFlag, strconv.Itoa(os.Getpid())), // lets a later PRISM recognise (and reap) this Chrome if we die
			chromedp.Flag("headless", cfg.Headless),
			chromedp.Flag("disable-gpu", cfg.Headless),
			chromedp.Flag("mute-audio", true),
			chromedp.Flag("hide-scrollbars", true),
			chromedp.WindowSize(1366, 900),
		)
		alloc, acancel = chromedp.NewExecAllocator(context.Background(), opts...)
	}
	bctx, bcancel := chromedp.NewContext(alloc)
	// The first Run starts the browser and its lifetime is tied to that context, so the
	// startup timeout is a watchdog rather than a derived context.
	watchdog := time.AfterFunc(45*time.Second, bcancel)
	err := chromedp.Run(bctx)
	watchdog.Stop()
	if err != nil {
		bcancel()
		acancel()
		return err
	}
	m.alloc, m.browser, m.bcancel, m.lastErr, m.started = acancel, bctx, bcancel, "", time.Now()
	return nil
}

// Stop closes the browser (or detaches from a remote one) and every open tab.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.tabs {
		h.cancel()
	}
	m.tabs = nil
	if m.bcancel != nil {
		m.bcancel()
	}
	if m.alloc != nil {
		m.alloc()
	}
	m.browser, m.alloc, m.bcancel = nil, nil, nil
}

// allowPrivate reports whether the user let web tools reach local/LAN addresses.
func (m *Manager) allowPrivate() bool {
	return settings.Load(context.Background(), m.Settings, settings.KeyWeb, settings.Web{}).AllowPrivate
}

// guardLocation refuses to keep a page open that ended up on a private address after redirects
// (the pre-navigation check cannot see those): it blanks the tab so the content is never read.
func (m *Manager) guardLocation(ctx context.Context) error {
	if m.allowPrivate() {
		return nil
	}
	var loc string
	if err := chromedp.Run(ctx, chromedp.Location(&loc)); err != nil {
		return err
	}
	if err := netguard.CheckURL(ctx, loc); err != nil {
		_ = chromedp.Run(ctx, chromedp.Navigate("about:blank"))
		return fmt.Errorf("navigation ended on a blocked address: %w", err)
	}
	return nil
}

// Render loads a URL in a throw-away tab and returns the rendered DOM. Unlike the named interactive tabs
// below, this one is never registered in m.tabs — each web_fetch(render=browser) call gets its own,
// discarded when it returns.
func (m *Manager) Render(ctx context.Context, rawURL, waitSelector string, wait time.Duration) (string, error) {
	m.mu.Lock()
	if err := m.start(); err != nil {
		m.mu.Unlock()
		return "", err
	}
	bctx := m.browser
	m.mu.Unlock()
	tabCtx, cancel := chromedp.NewContext(bctx)
	defer cancel()
	if err := chromedp.Run(tabCtx); err != nil { // allocate the tab before deriving timeouts
		return "", fmt.Errorf("open tab: %w", err)
	}
	tctx, cancelT := context.WithTimeout(tabCtx, 50*time.Second)
	defer cancelT()
	go func() { // honour caller cancellation
		select {
		case <-ctx.Done():
			cancelT()
		case <-tctx.Done():
		}
	}()
	if waitSelector == "" {
		waitSelector = "body"
	}
	if wait <= 0 {
		wait = 900 * time.Millisecond
	}
	var out string
	err := chromedp.Run(tctx,
		chromedp.Navigate(rawURL),
		chromedp.WaitReady(waitSelector, chromedp.ByQuery),
		chromedp.Sleep(wait),
	)
	if err == nil {
		err = m.guardLocation(tctx)
	}
	if err == nil {
		err = chromedp.Run(tctx, chromedp.OuterHTML("html", &out, chromedp.ByQuery))
	}
	if err != nil {
		return "", fmt.Errorf("render %s: %w", rawURL, err)
	}
	return out, nil
}

var unsafeIDChars = regexp.MustCompile(`[^a-zA-Z0-9_.:-]+`)

// sanitizeID turns a tab id into something safe to use as a directory name.
func sanitizeID(id string) string {
	s := unsafeIDChars.ReplaceAllString(id, "_")
	if s == "" {
		s = "tab"
	}
	return s
}

// defaultTabID names the tab a tool call gets when it doesn't say which one: one tab per task, so
// concurrent tasks never fight over the same page, and one tab per chat session for plain conversation
// (which often has no task row at all). An agent that wants deliberate multi-tab control (comparing two
// pages, keeping a reference page open) passes its own "tab" id explicitly instead.
func defaultTabID(env *tools.Env) string {
	if env.TaskID != 0 {
		return fmt.Sprintf("task:%d", env.TaskID)
	}
	return fmt.Sprintf("session:%d", env.SessionID)
}

func resolveTabID(env *tools.Env, given string) string {
	if given != "" {
		return given
	}
	return defaultTabID(env)
}

func (m *Manager) downloadDir(id string) string {
	return filepath.Join(m.DataDir, "work", "downloads", sanitizeID(id))
}

// tabCtx returns the named interactive tab, creating it lazily. Every tab gets its own download directory
// (see downloadDir/browser_download_wait) so two tabs' downloads never collide.
func (m *Manager) tabCtx(id string) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.start(); err != nil {
		return nil, err
	}
	if h, ok := m.tabs[id]; ok && h.ctx.Err() == nil {
		return h.ctx, nil
	}
	tab, tcancel := chromedp.NewContext(m.browser)
	// allocate the target now: a tab's lifetime is bound to the first context it runs under
	wd := time.AfterFunc(30*time.Second, tcancel)
	err := chromedp.Run(tab)
	wd.Stop()
	if err != nil {
		tcancel()
		return nil, fmt.Errorf("could not open a browser tab: %w", err)
	}
	dldir := m.downloadDir(id)
	_ = os.MkdirAll(dldir, 0o755)
	_ = chromedp.Run(tab, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpbrowser.SetDownloadBehavior(cdpbrowser.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(dldir).Do(ctx)
	}))
	if m.tabs == nil {
		m.tabs = map[string]*tabHandle{}
	}
	m.tabs[id] = &tabHandle{ctx: tab, cancel: tcancel, opened: time.Now()}
	return tab, nil
}

// closeTab closes one named tab; reports whether it was actually open.
func (m *Manager) closeTab(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.tabs[id]
	if !ok {
		return false
	}
	h.cancel()
	delete(m.tabs, id)
	return true
}

type TabInfo struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// listTabs describes every currently open tab (across every task/session, not just the caller's), so an
// agent can discover and explicitly target one another task left open.
func (m *Manager) listTabs() []TabInfo {
	m.mu.Lock()
	ids := make([]string, 0, len(m.tabs))
	ctxs := make(map[string]context.Context, len(m.tabs))
	for id, h := range m.tabs {
		if h.ctx.Err() == nil {
			ids = append(ids, id)
			ctxs[id] = h.ctx
		}
	}
	m.mu.Unlock()
	sort.Strings(ids)
	out := make([]TabInfo, 0, len(ids))
	for _, id := range ids {
		var loc, title string
		tctx, cancel := context.WithTimeout(ctxs[id], 5*time.Second)
		_ = chromedp.Run(tctx, chromedp.Location(&loc), chromedp.Title(&title))
		cancel()
		out = append(out, TabInfo{ID: id, URL: loc, Title: title})
	}
	return out
}

const snapshotJS = `(() => {
  const vis = e => { const r = e.getBoundingClientRect(); const s = getComputedStyle(e); return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'; };
  document.querySelectorAll('[data-prism-ref]').forEach(e => e.removeAttribute('data-prism-ref'));
  const els = [...document.querySelectorAll('a[href],button,input,select,textarea,summary,[role=button],[role=link],[role=tab],[onclick]')].filter(vis).slice(0, 120);
  const items = els.map((e, i) => {
    e.setAttribute('data-prism-ref', i + 1);
    const t = (e.innerText || e.value || e.getAttribute('aria-label') || e.placeholder || e.title || '').trim().replace(/\s+/g, ' ').slice(0, 80);
    return { ref: i + 1, tag: e.tagName.toLowerCase(), type: e.type || '', text: t, href: e.href || '', name: e.name || '', ph: e.placeholder || '' };
  });
  const text = (document.body ? document.body.innerText : '').replace(/\n{3,}/g, '\n\n').slice(0, %d);
  return JSON.stringify({ url: location.href, title: document.title, text, items });
})()`

type snapshot struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Items []struct {
		Ref  int    `json:"ref"`
		Tag  string `json:"tag"`
		Type string `json:"type"`
		Text string `json:"text"`
		Href string `json:"href"`
		Name string `json:"name"`
		PH   string `json:"ph"`
	} `json:"items"`
}

// snapshot describes tab (already resolved by the caller): visible text plus numbered interactive elements.
func (m *Manager) snapshot(tab context.Context, textLimit int) (string, error) {
	tctx, cancel := context.WithTimeout(tab, 30*time.Second)
	defer cancel()
	var raw string
	if err := chromedp.Run(tctx, chromedp.Evaluate(fmt.Sprintf(snapshotJS, textLimit), &raw)); err != nil {
		return "", err
	}
	var s snapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return "", err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "URL: %s\nTitle: %s\n\n--- page text ---\n%s\n\n--- interactive elements (use ref) ---\n", s.URL, s.Title, s.Text)
	for _, it := range s.Items {
		desc := it.Tag
		if it.Type != "" && it.Type != it.Tag {
			desc += "(" + it.Type + ")"
		}
		fmt.Fprintf(&sb, "[%d] %s %q", it.Ref, desc, it.Text)
		if it.Name != "" {
			sb.WriteString(" name=" + it.Name)
		}
		if it.PH != "" && it.PH != it.Text {
			sb.WriteString(" placeholder=" + it.PH)
		}
		if it.Href != "" && it.Tag == "a" {
			sb.WriteString(" → " + it.Href)
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

func sel(ref int, selector string) (string, error) {
	switch {
	case ref > 0:
		return fmt.Sprintf(`[data-prism-ref="%d"]`, ref), nil
	case selector != "":
		return selector, nil
	}
	return "", errors.New("give either ref (from browser_snapshot) or selector")
}

// waitAfter is the action to run after a page-changing interaction: waiting for a specific selector is far
// more reliable than a blind sleep, but a sleep is kept as the default so existing behaviour is unchanged
// when the caller doesn't know what to wait for.
func waitAfter(waitSelector string, waitMS int, fallback time.Duration) chromedp.Action {
	if waitSelector != "" {
		return chromedp.WaitVisible(waitSelector, chromedp.ByQuery)
	}
	d := fallback
	if waitMS > 0 {
		d = time.Duration(waitMS) * time.Millisecond
	}
	return chromedp.Sleep(d)
}

func tabParam() tools.Prop {
	return tools.Str("tab", "which tab (default: a tab of your own, kept for this task/conversation). Use browser_tab_list to see others.")
}
func waitParams() []tools.Prop {
	return []tools.Prop{
		tools.Str("wait_selector", "CSS selector to wait for afterwards instead of a fixed pause (more reliable)"),
		tools.Int("wait_ms", "how long to wait when wait_selector is not given (default ~900ms)"),
	}
}

// RegisterTools installs the interactive browser tools.
func (m *Manager) RegisterTools(reg *tools.Registry) {
	reg.Register(
		&tools.Tool{
			Name: "browser_open", Category: "browser", Risk: tools.RiskRead, Untrusted: true,
			Description: "Open a URL in an interactive browser tab (persistent profile: logins survive) and return a snapshot: page text plus numbered interactive elements you can click/type into by ref. " +
				"By default this is your own tab for this task — reuse it by calling browser_open/browser_snapshot/etc. again without 'tab'; pass 'tab' explicitly to work with more than one page at once.",
			Params: tools.Obj("url", append([]tools.Prop{tools.Str("url", "http(s) URL"), tabParam()}, waitParams()...)...),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					URL          string
					Tab          string
					WaitSelector string `json:"wait_selector"`
					WaitMS       int    `json:"wait_ms"`
				}](raw)
				if err != nil {
					return "", err
				}
				if !strings.HasPrefix(a.URL, "http://") && !strings.HasPrefix(a.URL, "https://") {
					return "", errors.New("http(s) URLs only")
				}
				if !m.allowPrivate() {
					if err := netguard.CheckURL(ctx, a.URL); err != nil {
						return "", err
					}
				}
				tab, err := m.tabCtx(resolveTabID(env, a.Tab))
				if err != nil {
					return "", err
				}
				tctx, cancel := context.WithTimeout(tab, 45*time.Second)
				defer cancel()
				if err := chromedp.Run(tctx, chromedp.Navigate(a.URL), chromedp.WaitReady("body", chromedp.ByQuery), waitAfter(a.WaitSelector, a.WaitMS, 700*time.Millisecond)); err != nil {
					return "", err
				}
				if err := m.guardLocation(tctx); err != nil {
					return "", err
				}
				return m.snapshot(tab, 5000)
			},
		},
		&tools.Tool{
			Name: "browser_snapshot", Category: "browser", Risk: tools.RiskRead, Untrusted: true,
			Description: "Snapshot a browser tab: visible text and numbered interactive elements.",
			Params:      tools.Obj("", tabParam(), tools.Int("max_chars", "max page text chars (default 5000)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Tab      string
					MaxChars int `json:"max_chars"`
				}](raw)
				if err != nil {
					return "", err
				}
				if a.MaxChars <= 0 {
					a.MaxChars = 5000
				}
				tab, err := m.tabCtx(resolveTabID(env, a.Tab))
				if err != nil {
					return "", err
				}
				return m.snapshot(tab, a.MaxChars)
			},
		},
		&tools.Tool{
			Name: "browser_click", Category: "browser", Risk: tools.RiskExec, Untrusted: true,
			Description: "Click an element by ref (from the latest snapshot) or CSS selector; returns a fresh snapshot.",
			Params: tools.Obj("", append([]tools.Prop{tools.Int("ref", "element number from the snapshot"), tools.Str("selector", "CSS selector (alternative)"),
				tabParam()}, waitParams()...)...),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Ref          int
					Selector     string
					Tab          string
					WaitSelector string `json:"wait_selector"`
					WaitMS       int    `json:"wait_ms"`
				}](raw)
				if err != nil {
					return "", err
				}
				s, err := sel(a.Ref, a.Selector)
				if err != nil {
					return "", err
				}
				tab, err := m.tabCtx(resolveTabID(env, a.Tab))
				if err != nil {
					return "", err
				}
				tctx, cancel := context.WithTimeout(tab, 30*time.Second)
				defer cancel()
				if err := chromedp.Run(tctx, chromedp.Click(s, chromedp.ByQuery, chromedp.NodeVisible), waitAfter(a.WaitSelector, a.WaitMS, 900*time.Millisecond)); err != nil {
					return "", err
				}
				return m.snapshot(tab, 3500)
			},
		},
		&tools.Tool{
			Name: "browser_type", Category: "browser", Risk: tools.RiskExec, Untrusted: true,
			Description: "Type text into an input by ref or selector (replacing its content); optionally press Enter. Returns a fresh snapshot.",
			Params: tools.Obj("text", append([]tools.Prop{tools.Int("ref", "element number from the snapshot"), tools.Str("selector", "CSS selector (alternative)"),
				tools.Str("text", "text to type"), tools.Bool("submit", "press Enter afterwards"), tabParam()}, waitParams()...)...),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Ref          int
					Selector     string
					Text         string
					Submit       bool
					Tab          string
					WaitSelector string `json:"wait_selector"`
					WaitMS       int    `json:"wait_ms"`
				}](raw)
				if err != nil {
					return "", err
				}
				s, err := sel(a.Ref, a.Selector)
				if err != nil {
					return "", err
				}
				tab, err := m.tabCtx(resolveTabID(env, a.Tab))
				if err != nil {
					return "", err
				}
				tctx, cancel := context.WithTimeout(tab, 30*time.Second)
				defer cancel()
				acts := []chromedp.Action{chromedp.Focus(s, chromedp.ByQuery), chromedp.SetValue(s, "", chromedp.ByQuery), chromedp.SendKeys(s, a.Text, chromedp.ByQuery)}
				if a.Submit {
					acts = append(acts, chromedp.SendKeys(s, kb.Enter, chromedp.ByQuery), waitAfter(a.WaitSelector, a.WaitMS, 1200*time.Millisecond))
				}
				if err := chromedp.Run(tctx, acts...); err != nil {
					return "", err
				}
				return m.snapshot(tab, 3500)
			},
		},
		&tools.Tool{
			Name: "browser_upload", Category: "browser", Risk: tools.RiskWrite, Untrusted: true,
			Description: "Upload a local file into a <input type=file> element by ref or selector. The path is subject to the same read policy as file_read (e.g. your workspace, or an exported artifact).",
			Params: tools.Obj("path", tools.Int("ref", "element number from the snapshot"), tools.Str("selector", "CSS selector (alternative)"),
				tools.Str("path", "local file path to upload"), tabParam()),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Ref      int
					Selector string
					Path     string
					Tab      string
				}](raw)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(a.Path) == "" {
					return "", errors.New("path is required")
				}
				if m.CanRead == nil {
					return "", errors.New("file uploads are not available")
				}
				if err := m.CanRead(ctx, a.Path); err != nil {
					return "", err
				}
				abs, err := filepath.Abs(a.Path)
				if err != nil {
					return "", err
				}
				if _, err := os.Stat(abs); err != nil {
					return "", fmt.Errorf("cannot read %s: %w", a.Path, err)
				}
				s, err := sel(a.Ref, a.Selector)
				if err != nil {
					return "", err
				}
				tab, err := m.tabCtx(resolveTabID(env, a.Tab))
				if err != nil {
					return "", err
				}
				tctx, cancel := context.WithTimeout(tab, 30*time.Second)
				defer cancel()
				if err := chromedp.Run(tctx, chromedp.SetUploadFiles(s, []string{abs}, chromedp.ByQuery)); err != nil {
					return "", err
				}
				return m.snapshot(tab, 3500)
			},
		},
		&tools.Tool{
			Name: "browser_download_wait", Category: "browser", Risk: tools.RiskRead, Auto: true,
			Description: "Wait for a file download started in this tab (e.g. by a preceding browser_click on a download link) to finish, then save it as an artifact and return its id/path. Downloads in a browser tab are private to that tab.",
			Params:      tools.Obj("", tabParam(), tools.Int("timeout_s", "max seconds to wait (default 30)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Tab      string
					TimeoutS int `json:"timeout_s"`
				}](raw)
				if err != nil {
					return "", err
				}
				tabID := resolveTabID(env, a.Tab)
				if _, err := m.tabCtx(tabID); err != nil { // ensure the tab (and its download dir) exists
					return "", err
				}
				timeout := 30 * time.Second
				if a.TimeoutS > 0 {
					timeout = time.Duration(a.TimeoutS) * time.Second
				}
				dir := m.downloadDir(tabID)
				deadline := time.Now().Add(timeout)
				var found string
				for {
					if f := completedDownload(dir); f != "" {
						found = f
						break
					}
					if time.Now().After(deadline) {
						break
					}
					select {
					case <-ctx.Done():
						return "", ctx.Err()
					case <-time.After(400 * time.Millisecond):
					}
				}
				if found == "" {
					return "", errors.New("no completed download detected within the timeout")
				}
				data, err := os.ReadFile(found)
				if err != nil {
					return "", err
				}
				if m.Save == nil {
					return fmt.Sprintf("download finished (%s, %d bytes) but artifact storage is unavailable", filepath.Base(found), len(data)), nil
				}
				id, p, err := m.Save(ctx, filepath.Base(found), "application/octet-stream", data, env.Agent)
				if err != nil {
					return "", err
				}
				_ = os.Remove(found)
				return fmt.Sprintf("Download saved as artifact #%d (%s).", id, p), nil
			},
		},
		&tools.Tool{
			Name: "browser_screenshot", Category: "browser", Risk: tools.RiskWrite, Auto: true,
			Description: "Take a PNG screenshot of a browser tab and save it as an artifact (returns its id and path).",
			Params:      tools.Obj("", tabParam(), tools.Bool("full_page", "capture the whole page")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Tab      string
					FullPage bool `json:"full_page"`
				}](raw)
				if err != nil {
					return "", err
				}
				tab, err := m.tabCtx(resolveTabID(env, a.Tab))
				if err != nil {
					return "", err
				}
				tctx, cancel := context.WithTimeout(tab, 40*time.Second)
				defer cancel()
				var buf []byte
				act := chromedp.CaptureScreenshot(&buf)
				if a.FullPage {
					act = chromedp.FullScreenshot(&buf, 90)
				}
				if err := chromedp.Run(tctx, act); err != nil {
					return "", err
				}
				if m.Save == nil {
					return fmt.Sprintf("screenshot captured (%d bytes) but artifact storage is unavailable", len(buf)), nil
				}
				id, p, err := m.Save(ctx, "screenshot.png", "image/png", buf, env.Agent)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Screenshot saved as artifact #%d (%s).", id, p), nil
			},
		},
		&tools.Tool{
			Name: "browser_eval", Category: "browser", Risk: tools.RiskExec, Untrusted: true,
			Description: "Evaluate a JavaScript expression in a tab and return the JSON result (for scraping data the snapshot does not expose).",
			Params:      tools.Obj("js", tools.Str("js", "JavaScript expression"), tabParam()),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					JS  string
					Tab string
				}](raw)
				if err != nil {
					return "", err
				}
				tab, err := m.tabCtx(resolveTabID(env, a.Tab))
				if err != nil {
					return "", err
				}
				tctx, cancel := context.WithTimeout(tab, 30*time.Second)
				defer cancel()
				var res any
				if err := chromedp.Run(tctx, chromedp.Evaluate(a.JS, &res)); err != nil {
					return "", err
				}
				b, _ := json.Marshal(res)
				return string(b), nil
			},
		},
		&tools.Tool{
			Name: "browser_tab_list", Category: "browser", Risk: tools.RiskRead, Auto: true,
			Description: "List every open browser tab (id, URL, title) — including ones other tasks left open — so you can target one explicitly with 'tab' instead of guessing.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				tabs := m.listTabs()
				if len(tabs) == 0 {
					return "No open tabs.", nil
				}
				var sb strings.Builder
				for _, t := range tabs {
					fmt.Fprintf(&sb, "%s — %s (%s)\n", t.ID, t.Title, t.URL)
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "browser_tab_close", Category: "browser", Risk: tools.RiskWrite, Auto: true,
			Description: "Close one browser tab (default: your own for this task) and free its resources. The browser itself and other tabs stay open.",
			Params:      tools.Obj("", tabParam()),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Tab string }](raw)
				if err != nil {
					return "", err
				}
				id := resolveTabID(env, a.Tab)
				if !m.closeTab(id) {
					return fmt.Sprintf("Tab %q was not open.", id), nil
				}
				return fmt.Sprintf("Tab %q closed.", id), nil
			},
		},
		&tools.Tool{
			Name: "browser_shutdown", Category: "browser", Risk: tools.RiskWrite, Auto: true,
			Description: "Close the entire browser and every tab, freeing all resources (the profile with cookies is kept). Use browser_tab_close for just your own tab.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				m.Stop()
				return "Browser closed.", nil
			},
		},
	)
}

// completedDownload returns the path of a fully-downloaded file in dir, or "" if none — Chrome names an
// in-progress download "<name>.crdownload" and renames it atomically to its final name only once complete,
// so the mere presence of a non-.crdownload file is sufficient (no separate size-stability check needed).
func completedDownload(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".crdownload") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		return filepath.Join(dir, e.Name())
	}
	return ""
}
