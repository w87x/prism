package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html/charset"

	"prism/internal/netguard"
	"prism/internal/settings"
)

// Renderer renders a page in a real browser (implemented by package browser).
type Renderer interface {
	Render(ctx context.Context, rawURL, waitSelector string, wait time.Duration) (html string, err error)
	Available() bool
}

type Page struct {
	URL         string
	FinalURL    string
	Status      int
	ContentType string
	Body        string // raw body (HTML or text)
	Rendered    bool   // came from the browser
	Via         string // http | flare | browser: how it was fetched
	Learned     bool   // the method was chosen because it worked for this site before
}

type Fetcher struct {
	Settings     *settings.Store
	Browser      Renderer
	AllowPrivate bool // tests / advanced users; also settings.Web.AllowPrivate

	prefMu sync.Mutex
	prefs  map[string]hostPref // host -> the fetch method that worked (see learn)

	downMu sync.Mutex
	down   map[string]time.Time // hostname -> when its DNS lookup last failed outright
}

// hostDownCooldown: once a host's DNS lookup fails outright ("no such host"), skip it for this long instead
// of re-resolving it on every subsequent web_fetch call — a host that doesn't exist right now won't exist a
// second later either, and repeatedly hammering it (or the resolver) on every retry helps nobody.
const hostDownCooldown = 10 * time.Minute

// hostDownSince reports whether host's DNS lookup recently failed outright, and when. A stale entry (past
// the cooldown) is treated as expired and cleared, so a host that recovers is tried again normally.
func (f *Fetcher) hostDownSince(host string) (time.Time, bool) {
	f.downMu.Lock()
	defer f.downMu.Unlock()
	t, ok := f.down[host]
	if ok && time.Since(t) > hostDownCooldown {
		delete(f.down, host)
		return time.Time{}, false
	}
	return t, ok
}

func (f *Fetcher) markHostDown(host string) {
	f.downMu.Lock()
	defer f.downMu.Unlock()
	if f.down == nil {
		f.down = map[string]time.Time{}
	}
	f.down[host] = time.Now()
}

// isHostNotFound reports a DNS resolution failure specifically (the host does not exist), as opposed to a
// timeout or connection refusal — those can be transient network/firewall blips and are not grounds to stop
// trying, but "no such host" is a much stronger signal worth a real cooldown.
func isHostNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// checkURL validates the scheme; private-address blocking happens in netguard (dial time and per request).
func checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid URL %q (http/https only)", raw)
	}
	return u, nil
}

func (f *Fetcher) allowPrivate(cfg settings.Web) bool { return f.AllowPrivate || cfg.AllowPrivate }

// Fetch retrieves a page. mode: "auto" (HTTP, escalating to FlareSolverr/browser when the
// page looks blocked or client-rendered), "http" or "browser".
func (f *Fetcher) Fetch(ctx context.Context, raw, mode, waitSel string, wait time.Duration) (*Page, error) {
	u, err := checkURL(raw)
	if err != nil {
		return nil, err
	}
	if since, down := f.hostDownSince(u.Hostname()); down {
		return nil, fmt.Errorf("%s was unreachable %s ago (DNS lookup failed) — not retrying yet to avoid hammering a dead host; try again later or with render=browser", u.Hostname(), time.Since(since).Round(time.Second))
	}
	cfg := settings.Load(ctx, f.Settings, settings.KeyWeb, settings.Web{})
	if !f.allowPrivate(cfg) {
		if err := netguard.CheckURL(ctx, u.String()); err != nil {
			return nil, err
		}
	}
	if mode == "browser" {
		return f.viaBrowser(ctx, u.String(), waitSel, wait)
	}
	if mode == "http" {
		return f.viaHTTP(ctx, u, cfg)
	}
	// auto: a method that worked for this site before goes first, so a protected site is not fetched the slow way
	// on every visit
	if via := f.learned(ctx, u.Hostname()); via != "" {
		var p *Page
		var err error
		switch via {
		case "flare":
			if cfg.FlareSolverrURL != "" {
				p, err = f.viaFlare(ctx, cfg, u.String())
			}
		case "browser":
			if f.Browser != nil && f.Browser.Available() {
				p, err = f.viaBrowser(ctx, u.String(), waitSel, wait)
			}
		}
		if p != nil && err == nil && p.Status/100 == 2 && !looksLikeChallenge(p.Body) {
			p.Learned = true
			return p, nil
		}
		f.learn(ctx, u.Hostname(), "") // it no longer works: forget it and take the normal route
	}
	page, err := f.fetchAuto(ctx, u, cfg, waitSel, wait)
	if err == nil && page != nil && page.Status/100 == 2 {
		f.learn(ctx, u.Hostname(), map[string]string{"flare": "flare", "browser": "browser"}[page.Via])
	}
	return page, err
}

// fetchAuto is the default route: plain HTTP, escalating to FlareSolverr or the browser when the page is blocked
// or needs JavaScript.
func (f *Fetcher) fetchAuto(ctx context.Context, u *url.URL, cfg settings.Web, waitSel string, wait time.Duration) (*Page, error) {
	page, herr := f.viaHTTP(ctx, u, cfg)
	blocked := herr == nil && (page.Status == 403 || page.Status == 503 || page.Status == 429) && looksLikeChallenge(page.Body)
	if blocked && cfg.FlareSolverrURL != "" {
		if p, err := f.viaFlare(ctx, cfg, u.String()); err == nil {
			return p, nil
		}
	}
	needsJS := herr == nil && page.Status/100 == 2 && strings.Contains(page.ContentType, "html") && clientRendered(page.Body)
	if (herr != nil || blocked || needsJS) && f.Browser != nil && f.Browser.Available() {
		if p, err := f.viaBrowser(ctx, u.String(), waitSel, wait); err == nil {
			return p, nil
		} else if herr == nil && !blocked && !needsJS {
			return page, nil
		}
	}
	return page, herr
}

func looksLikeChallenge(body string) bool {
	l := strings.ToLower(body)
	return strings.Contains(l, "cloudflare") || strings.Contains(l, "just a moment") || strings.Contains(l, "captcha") || strings.Contains(l, "cf-chl")
}

// clientRendered guesses that the visible text is tiny compared to the markup (SPA shell).
func clientRendered(body string) bool {
	if len(body) < 300 {
		return false
	}
	doc, err := Parse(body)
	if err != nil {
		return false
	}
	txt := len(textOf(mainNode(doc)))
	return txt < 200 && (strings.Contains(body, "id=\"root\"") || strings.Contains(body, "id=\"app\"") || strings.Contains(body, "__NEXT_DATA__") || strings.Contains(strings.ToLower(body), "enable javascript"))
}

func (f *Fetcher) viaHTTP(ctx context.Context, u *url.URL, cfg settings.Web) (*Page, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", userAgent(cfg))
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,ru;q=0.6")
	resp, err := netguard.Client(f.allowPrivate(cfg), 40*time.Second).Do(req)
	if err != nil {
		if isHostNotFound(err) {
			f.markHostDown(u.Hostname())
		}
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	if err != nil {
		return nil, err
	}
	return &Page{URL: u.String(), FinalURL: resp.Request.URL.String(), Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: decodeBody(b, resp.Header.Get("Content-Type")), Via: "http"}, nil
}

func (f *Fetcher) viaBrowser(ctx context.Context, raw, waitSel string, wait time.Duration) (*Page, error) {
	if f.Browser == nil || !f.Browser.Available() {
		return nil, errors.New("browser is not available (install Chrome or configure a remote DevTools URL)")
	}
	h, err := f.Browser.Render(ctx, raw, waitSel, wait)
	if err != nil {
		return nil, err
	}
	return &Page{URL: raw, FinalURL: raw, Status: 200, ContentType: "text/html", Body: h, Rendered: true, Via: "browser"}, nil
}

// viaFlare solves Cloudflare-style challenges through a FlareSolverr instance.
func (f *Fetcher) viaFlare(ctx context.Context, cfg settings.Web, target string) (*Page, error) {
	body, _ := json.Marshal(map[string]any{"cmd": "request.get", "url": target, "maxTimeout": 60000})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.FlareSolverrURL, "/")+"/v1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 75 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			URL      string `json:"url"`
			Status   int    `json:"status"`
			Response string `json:"response"`
		} `json:"solution"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.Status != "ok" {
		return nil, fmt.Errorf("flaresolverr: %s", r.Message)
	}
	return &Page{URL: target, FinalURL: r.Solution.URL, Status: r.Solution.Status, ContentType: "text/html", Body: r.Solution.Response, Rendered: true, Via: "flare"}, nil
}

// decodeBody converts the body to UTF-8 using the declared charset (header, BOM or <meta>), so
// windows-1251/koi8-r/shift-jis pages come out readable.
func decodeBody(b []byte, ct string) string {
	r, err := charset.NewReader(bytes.NewReader(b), ct)
	if err != nil {
		return string(b)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return string(b)
	}
	return string(out)
}

// ── what worked for a site ──────────────────────────────────────────────────

const learnedTTL = 14 * 24 * time.Hour

type hostPref struct {
	Via string    `json:"via"`
	At  time.Time `json:"at"`
}

func (f *Fetcher) loadPrefs(ctx context.Context) {
	f.prefMu.Lock()
	defer f.prefMu.Unlock()
	if f.prefs == nil {
		f.prefs = map[string]hostPref{}
		if f.Settings != nil {
			f.prefs = settings.Load(ctx, f.Settings, "web.hosts", map[string]hostPref{})
			if f.prefs == nil {
				f.prefs = map[string]hostPref{}
			}
		}
	}
}

func (f *Fetcher) learned(ctx context.Context, host string) string {
	f.loadPrefs(ctx)
	f.prefMu.Lock()
	defer f.prefMu.Unlock()
	p, ok := f.prefs[host]
	if !ok || time.Since(p.At) > learnedTTL {
		return ""
	}
	return p.Via
}

// learn records which method produced a good page for host ("" clears the entry); it writes only when something
// changed, and keeps the table small.
func (f *Fetcher) learn(ctx context.Context, host, via string) {
	f.loadPrefs(ctx)
	f.prefMu.Lock()
	cur, had := f.prefs[host]
	switch {
	case via == "" && !had:
		f.prefMu.Unlock()
		return
	case via == "":
		delete(f.prefs, host)
	case had && cur.Via == via && time.Since(cur.At) < learnedTTL/2:
		f.prefMu.Unlock()
		return
	default:
		f.prefs[host] = hostPref{Via: via, At: time.Now()}
	}
	if len(f.prefs) > 300 {
		for h, p := range f.prefs {
			if time.Since(p.At) > learnedTTL {
				delete(f.prefs, h)
			}
		}
	}
	snapshot := make(map[string]hostPref, len(f.prefs))
	for k, v := range f.prefs {
		snapshot[k] = v
	}
	f.prefMu.Unlock()
	if f.Settings != nil {
		_ = f.Settings.Set(ctx, "web.hosts", snapshot)
	}
}

// ── network capture ─────────────────────────────────────────────────────────

// CapturedJSON is a JSON response the page fetched by itself while loading (its data API).
type CapturedJSON struct {
	URL       string `json:"url"`
	Status    int    `json:"status"`
	Body      string `json:"body"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Capture is what a page's own network traffic revealed.
type Capture struct {
	JSON     []CapturedJSON `json:"json"`
	Media    []string       `json:"media"` // stream / video / audio URLs the player requested
	Requests int            `json:"requests"`
}

// Capturer is a renderer that can also record the page's network traffic.
type Capturer interface {
	Capture(ctx context.Context, rawURL, waitSelector string, wait time.Duration) (html string, c Capture, err error)
}

// CaptureFetch renders a page in the browser and records its JSON API responses and media requests. Many sites
// load their data from such calls, and stream URLs never appear in the HTML at all.
func (f *Fetcher) CaptureFetch(ctx context.Context, raw, waitSel string, wait time.Duration) (*Page, *Capture, error) {
	u, err := checkURL(raw)
	if err != nil {
		return nil, nil, err
	}
	cfg := settings.Load(ctx, f.Settings, settings.KeyWeb, settings.Web{})
	if !f.allowPrivate(cfg) {
		if err := netguard.CheckURL(ctx, u.String()); err != nil {
			return nil, nil, err
		}
	}
	cp, ok := f.Browser.(Capturer)
	if f.Browser == nil || !ok || !f.Browser.Available() {
		return nil, nil, errors.New("network capture needs the browser (install Chrome or configure a remote DevTools URL)")
	}
	h, c, err := cp.Capture(ctx, u.String(), waitSel, wait)
	if err != nil {
		return nil, nil, err
	}
	return &Page{URL: u.String(), FinalURL: u.String(), Status: 200, ContentType: "text/html", Body: h, Rendered: true, Via: "browser"}, &c, nil
}
