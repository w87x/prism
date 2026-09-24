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
			Description: "Fetch a web page and return it as markdown (default), text, links, raw html, or a JSON DOM tree. Uses plain HTTP and escalates to a real browser / FlareSolverr for JS-heavy or protected pages (render=auto). Long pages are paged with offset. For structured data prefer web_extract.",
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
			Description: "Extract structured JSON from a web page with an LLM: describe what you want (e.g. 'all articles with title, link, description') and optionally the object shape. Handles long pages by chunking. Costs model calls — use web_fetch when you only need to read.",
			Params: tools.Obj("url,instruction",
				tools.Str("url", "http(s) URL"),
				tools.Str("instruction", "what to extract, in plain language"),
				tools.Str("schema", `optional shape, e.g. {"title":"string","link":"url","price":"number"}`),
				tools.Enum("render", "auto (default) | http | browser", "auto", "http", "browser"),
				tools.Str("wait_selector", "CSS selector to wait for (browser rendering)"),
				tools.Int("max_items", "cap the number of items")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					URL          string `json:"url"`
					Instruction  string `json:"instruction"`
					Schema       string `json:"schema"`
					Render       string `json:"render"`
					WaitSelector string `json:"wait_selector"`
					MaxItems     int    `json:"max_items"`
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
				res, err := s.Extract.Extract(ctx, page, a.Instruction, a.Schema, a.MaxItems)
				if err != nil {
					return "", err
				}
				b, _ := json.MarshalIndent(res, "", " ")
				return string(b), nil
			},
		},
	)
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
