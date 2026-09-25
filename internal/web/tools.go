package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"prism/internal/settings"
	"prism/internal/tools"
)

// Service bundles the web tools' dependencies.
type Service struct {
	Settings *settings.Store
	Fetcher  *Fetcher
	Extract  *Extractor
	// Bookmarks looks up saved bookmarks relevant to a query (nil: bookmarks unavailable). Wired from
	// internal/app to builtin.FindBookmarks — web_search checks it first so an already-known link can
	// answer the need without spending a real search call. Kept as a function, not an import of
	// internal/tools/builtin, to keep this package's dependencies pointed the other way.
	Bookmarks func(ctx context.Context, query string, limit int) []BookmarkHit
}

// BookmarkHit is the sliver of a saved bookmark web_search needs to surface it — deliberately not the full
// builtin.Bookmark shape, so this package stays decoupled from that one.
type BookmarkHit struct {
	Title, URL, Description string
}

func (s *Service) cfg(ctx context.Context) settings.Web {
	return settings.Load(ctx, s.Settings, settings.KeyWeb, settings.Web{})
}

// RegisterTools installs web_search, web_fetch and web_extract.
func (s *Service) RegisterTools(reg *tools.Registry) {
	reg.Register(
		&tools.Tool{
			Name: "web_search", Category: "web", Risk: tools.RiskRead, Untrusted: true,
			Description: "Search the web. Checks the user's saved bookmarks first (a known link can answer this without spending a real search) then returns titles, URLs and snippets (providers tried in the configured order: Yandex, AnySearch, Tavily, DuckDuckGo). Follow up with web_fetch or web_extract on promising URLs.",
			Params:      tools.Obj("query", tools.Str("query", "search query"), tools.Int("limit", "max results (default 8)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Query string
					Limit int
				}](raw)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				if s.Bookmarks != nil {
					if bm := s.Bookmarks(ctx, a.Query, 3); len(bm) > 0 {
						sb.WriteString("From your bookmarks (already known — check these before using a fresh result):\n")
						for i, b := range bm {
							fmt.Fprintf(&sb, "%d. %s — %s\n   %s\n", i+1, b.Title, b.URL, b.Description)
						}
						sb.WriteString("\n")
					}
				}
				res, used, err := Search(ctx, s.cfg(ctx), a.Query, a.Limit)
				if err != nil {
					if sb.Len() > 0 { // the bookmark match is still worth returning even if the live search failed
						return sb.String(), nil
					}
					return "", err
				}
				fmt.Fprintf(&sb, "Results (via %s):\n", used)
				for i, r := range res {
					fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, r.Snippet)
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "web_fetch", Category: "web", Risk: tools.RiskRead, Untrusted: true,
			Description: "Fetch a web page and return it as markdown (default), text, links, raw html, or a JSON DOM tree. Uses plain HTTP and escalates to a real browser / FlareSolverr for JS-heavy or protected pages (render=auto). Long pages are paged with offset. For structured data prefer web_structured (free, exact) then web_extract; for images, video and stream URLs use web_media.",
			Params: tools.Obj("url",
				tools.Str("url", "http(s) URL"),
				tools.Enum("format", "output format", "markdown", "text", "links", "html", "dom"),
				tools.Enum("render", "auto (default) | http | browser", "auto", "http", "browser"),
				tools.Str("wait_selector", "CSS selector to wait for when rendering in the browser"),
				tools.Int("wait_ms", "extra wait after load in the browser (ms)"),
				tools.Int("max_chars", "max characters to return (default 12000)"),
				tools.Int("offset", "character offset for paging")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					URL          string `json:"url"`
					Format       string `json:"format"`
					Render       string `json:"render"`
					WaitSelector string `json:"wait_selector"`
					WaitMS       int    `json:"wait_ms"`
					MaxChars     int    `json:"max_chars"`
					Offset       int    `json:"offset"`
				}](raw)
				if err != nil {
					return "", err
				}
				page, err := s.Fetcher.Fetch(ctx, a.URL, a.Render, a.WaitSelector, time.Duration(a.WaitMS)*time.Millisecond)
				if err != nil {
					return "", err
				}
				body, title := renderPage(page, a.Format)
				if a.MaxChars <= 0 {
					a.MaxChars = 12000
				}
				r := []rune(body)
				total := len(r)
				if a.Offset > 0 {
					if a.Offset >= total {
						return fmt.Sprintf("(offset beyond end; total %d chars)", total), nil
					}
					r = r[a.Offset:]
				}
				more := ""
				if len(r) > a.MaxChars {
					r = r[:a.MaxChars]
					more = fmt.Sprintf("\n…[%d more chars; continue with offset=%d]", total-a.Offset-a.MaxChars, a.Offset+a.MaxChars)
				}
				head := fmt.Sprintf("URL: %s\nStatus: %d%s\n", page.FinalURL, page.Status, map[bool]string{true: " (browser-rendered)", false: ""}[page.Rendered])
				if page.Learned {
					head += "Note: fetched via " + page.Via + " because that worked for this site before.\n"
				}
				if title != "" {
					head += "Title: " + title + "\n"
				}
				if page.Status/100 != 2 {
					head += "Note: non-2xx status; content may be an error page.\n"
				}
				return head + "\n" + string(r) + more, nil
			},
		},
		&tools.Tool{
			Name: "web_extract", Category: "web", Risk: tools.RiskRead, Untrusted: true,
			Description: "Extract structured JSON from a web page: describe what you want (e.g. 'all articles with title, link, description') and optionally the object shape. It reads the page's own structured data first (JSON-LD, embedded JSON, tables — cheap and exact) and only reads the visible text when that is not enough. Values are coerced to the schema's types (numbers from '1 299,90 €', absolute URLs). It can follow next-page links (max_pages) and include image / table listings. Costs model calls — use web_fetch when you only need to read a page, web_structured to see the raw structured data.",
			Params: tools.Obj("url,instruction",
				tools.Str("url", "http(s) URL"),
				tools.Str("instruction", "what to extract, in plain language"),
				tools.Str("schema", `optional shape with types, e.g. {"title":"string","link":"url","price":"number","in_stock":"boolean"}`),
				tools.Enum("source", "auto (default): structured data first, then the page text | structured | page", "auto", "structured", "page"),
				tools.StrList("include", "extra material for the model: images, tables"),
				tools.Int("max_pages", "follow 'next page' links up to this many pages (default 1, max 5)"),
				tools.Enum("render", "auto (default) | http | browser", "auto", "http", "browser"),
				tools.Str("wait_selector", "CSS selector to wait for (browser rendering)"),
				tools.Int("max_items", "cap the number of items")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					URL          string   `json:"url"`
					Instruction  string   `json:"instruction"`
					Schema       string   `json:"schema"`
					Source       string   `json:"source"`
					Include      []string `json:"include"`
					MaxPages     int      `json:"max_pages"`
					Render       string   `json:"render"`
					WaitSelector string   `json:"wait_selector"`
					MaxItems     int      `json:"max_items"`
				}](raw)
				if err != nil {
					return "", err
				}
				page, err := s.Fetcher.Fetch(ctx, a.URL, a.Render, a.WaitSelector, 0)
				if err != nil {
					return "", err
				}
				if page.Status/100 != 2 {
					return "", fmt.Errorf("page returned HTTP %d", page.Status)
				}
				res, err := s.Extract.Run(ctx, page, ExtractOpts{Instruction: a.Instruction, Schema: a.Schema, MaxItems: a.MaxItems, Source: a.Source, Include: a.Include, MaxPages: a.MaxPages,
					Fetch: func(c context.Context, u string) (*Page, error) {
						return s.Fetcher.Fetch(c, u, a.Render, a.WaitSelector, 0)
					}})
				if err != nil {
					return "", err
				}
				b, _ := json.MarshalIndent(res, "", " ")
				return string(b), nil
			},
		},
		&tools.Tool{
			Name: "web_structured", Category: "web", Risk: tools.RiskRead, Untrusted: true,
			Description: "Read what a page already says about itself in machine-readable form, without any model call: OpenGraph / meta tags, JSON-LD (products, articles, events, videos, recipes…), microdata, JSON embedded in the page (Next.js __NEXT_DATA__, Nuxt, window.__STATE__, script tags), tables, feed links and the main article text with author and date. Use it before web_extract: if the data you need is here, you have it exactly. sections limits the output.",
			Params: tools.Obj("url",
				tools.Str("url", "http(s) URL"),
				tools.StrList("sections", "any of: meta, json_ld, microdata, embedded, tables, feeds, article (default: all)"),
				tools.Enum("render", "auto (default) | http | browser", "auto", "http", "browser"),
				tools.Bool("capture", "also record the JSON API responses the page loads itself (needs the browser; finds data that is not in the HTML)"),
				tools.Int("max_chars", "output budget (default 16000)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					URL      string   `json:"url"`
					Sections []string `json:"sections"`
					Render   string   `json:"render"`
					Capture  bool     `json:"capture"`
					MaxChars int      `json:"max_chars"`
				}](raw)
				if err != nil {
					return "", err
				}
				page, capt, err := s.fetchMaybeCapture(ctx, a.URL, a.Render, a.Capture)
				if err != nil {
					return "", err
				}
				if page.Status/100 != 2 {
					return "", fmt.Errorf("page returned HTTP %d", page.Status)
				}
				st, err := ExtractStructured(page)
				if err != nil {
					return "", err
				}
				return renderStructured(st, capt, a.Sections, a.MaxChars, page), nil
			},
		},
		&tools.Tool{
			Name: "web_media", Category: "web", Risk: tools.RiskRead, Untrusted: true,
			Description: "List the images, videos, audio, streams (HLS .m3u8 / DASH .mpd), embedded players and downloadable files of a page, with absolute URLs, sizes, alt text and where each was found. capture=true also records what the page's player requests over the network — often the only place a stream URL appears. Use download_start (or the shell with yt-dlp for video sites) to fetch what you pick.",
			Params: tools.Obj("url",
				tools.Str("url", "http(s) URL"),
				tools.StrList("kinds", "images, video, audio, streams, embeds, files (default: all)"),
				tools.Int("min_width", "skip images known to be narrower than this (icons, thumbnails)"),
				tools.Enum("render", "auto (default) | http | browser", "auto", "http", "browser"),
				tools.Bool("capture", "record the page's network traffic in the browser to find stream / video URLs"),
				tools.Int("limit", "max items (default 80)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					URL      string   `json:"url"`
					Kinds    []string `json:"kinds"`
					MinWidth int      `json:"min_width"`
					Render   string   `json:"render"`
					Capture  bool     `json:"capture"`
					Limit    int      `json:"limit"`
				}](raw)
				if err != nil {
					return "", err
				}
				page, capt, err := s.fetchMaybeCapture(ctx, a.URL, a.Render, a.Capture)
				if err != nil {
					return "", err
				}
				if page.Status/100 != 2 {
					return "", fmt.Errorf("page returned HTTP %d", page.Status)
				}
				items, err := ExtractMedia(page, a.MinWidth)
				if err != nil {
					return "", err
				}
				if capt != nil {
					for _, u := range capt.Media {
						k := kindOfURL(u)
						if k == "" {
							k = "stream"
						}
						items = append(items, MediaItem{Kind: k, URL: u, Found: "network"})
					}
				}
				items = FilterMedia(items, a.Kinds)
				if a.Limit <= 0 || a.Limit > 300 {
					a.Limit = 80
				}
				more := 0
				if len(items) > a.Limit {
					more, items = len(items)-a.Limit, items[:a.Limit]
				}
				out := map[string]any{"page": page.FinalURL, "count": len(items), "items": items}
				if more > 0 {
					out["more"] = fmt.Sprintf("%d more not shown: narrow with kinds or min_width", more)
				}
				b, _ := json.MarshalIndent(out, "", " ")
				return string(b), nil
			},
		},
	)
}

// fetchMaybeCapture fetches a page; with capture it goes through the browser and also returns the network capture.
func (s *Service) fetchMaybeCapture(ctx context.Context, rawURL, render string, capture bool) (*Page, *Capture, error) {
	if capture {
		return s.Fetcher.CaptureFetch(ctx, rawURL, "", 0)
	}
	p, err := s.Fetcher.Fetch(ctx, rawURL, render, "", 0)
	return p, nil, err
}

// renderStructured formats structured data as JSON within a size budget, trimming the biggest sections first.
func renderStructured(st *Structured, capt *Capture, sections []string, budget int, page *Page) string {
	if budget <= 0 || budget > 60000 {
		budget = 16000
	}
	want := map[string]bool{}
	for _, x := range sections {
		want[strings.ToLower(strings.TrimSpace(x))] = true
	}
	on := func(k string) bool { return len(want) == 0 || want[k] }
	out := map[string]any{"url": page.FinalURL}
	if page.Via != "" {
		out["fetched_via"] = page.Via
	}
	if st.Title != "" {
		out["title"] = st.Title
	}
	if st.Description != "" {
		out["description"] = st.Description
	}
	if st.Canonical != "" {
		out["canonical"] = st.Canonical
	}
	if on("meta") && len(st.Meta) > 0 {
		out["meta"] = st.Meta
	}
	if on("json_ld") && len(st.JSONLD) > 0 {
		out["json_ld"] = st.JSONLD
	}
	if on("microdata") && len(st.Microdata) > 0 {
		out["microdata"] = st.Microdata
	}
	if on("embedded") && len(st.Embedded) > 0 {
		out["embedded"] = st.Embedded
	}
	if on("tables") && len(st.Tables) > 0 {
		out["tables"] = st.Tables
	}
	if on("feeds") && len(st.Feeds) > 0 {
		out["feeds"] = st.Feeds
	}
	if on("article") && st.Article != nil {
		out["article"] = st.Article
	}
	if capt != nil {
		out["network"] = map[string]any{"requests": capt.Requests, "json": capt.JSON, "media": capt.Media}
	}
	b, _ := json.MarshalIndent(out, "", " ")
	if len(b) <= budget {
		return string(b)
	}
	// too big: shrink the heavy sections one by one (article text, embedded, network, tables) until it fits
	for _, k := range []string{"article", "network", "embedded", "tables", "json_ld", "microdata"} {
		if v, ok := out[k]; ok {
			vb, _ := json.Marshal(v)
			if len(vb) > 1500 {
				out[k] = fmt.Sprintf("(omitted: %d bytes — ask for it alone with sections=[%q] and a larger max_chars)", len(vb), k)
				b, _ = json.MarshalIndent(out, "", " ")
				if len(b) <= budget {
					return string(b)
				}
			}
		}
	}
	return string(b[:budget]) + "\n…[truncated]"
}

func renderPage(p *Page, format string) (body, title string) {
	isHTML := strings.Contains(p.ContentType, "html") || strings.Contains(p.Body[:min(len(p.Body), 400)], "<html") || strings.Contains(p.Body[:min(len(p.Body), 400)], "<!DOCTYPE")
	if !isHTML {
		return p.Body, ""
	}
	doc, err := Parse(p.Body)
	if err != nil {
		return p.Body, ""
	}
	base, _ := url.Parse(p.FinalURL)
	title = Title(doc)
	switch format {
	case "html":
		return Compact(doc, base), title
	case "dom":
		return DOMJSON(BuildDOM(doc, DOMOptions{Base: base, KeepAttrs: []string{"href", "src", "alt", "title", "id", "class", "datetime", "name", "content"}})), title
	case "links":
		var sb strings.Builder
		for _, l := range Links(doc, base) {
			fmt.Fprintf(&sb, "%s — %s\n", l[0], l[1])
		}
		return sb.String(), title
	case "text":
		return Markdown(doc, base, false), title
	}
	return Markdown(doc, base, true), title
}

// RenderText renders a fetched page as plain readable text (used by watchers).
func RenderText(p *Page) (string, string) { return renderPage(p, "text") }
