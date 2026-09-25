package web

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Structured is what a page already says about itself in machine-readable form. Reading it costs no model call and
// is usually more accurate than asking a model to read the visible text: product and article data in JSON-LD, the
// OpenGraph card, embedded application state (Next.js, Nuxt, inline JSON), and the page's tables.
type Structured struct {
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Canonical   string            `json:"canonical,omitempty"`
	Language    string            `json:"language,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`      // OpenGraph / Twitter card / article:* / a few standard names
	JSONLD      []any             `json:"json_ld,omitempty"`   // every JSON-LD entity (an @graph is flattened)
	Microdata   []map[string]any  `json:"microdata,omitempty"` // itemscope / itemprop items
	Embedded    []Embedded        `json:"embedded,omitempty"`  // JSON found in script tags
	Tables      []Table           `json:"tables,omitempty"`
	Feeds       []FeedLink        `json:"feeds,omitempty"`
	Article     *Article          `json:"article,omitempty"`
}

type Embedded struct {
	Name string `json:"name"` // script id, or the window.__X__ variable
	Data any    `json:"data"`
	Size int    `json:"size"`
}

type Table struct {
	Caption string     `json:"caption,omitempty"`
	Headers []string   `json:"headers,omitempty"`
	Rows    [][]string `json:"rows"`
}

type FeedLink struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	Type  string `json:"type"`
}

type Article struct {
	Title     string `json:"title,omitempty"`
	Author    string `json:"author,omitempty"`
	Published string `json:"published,omitempty"`
	Text      string `json:"text"`
}

const (
	maxEmbedded = 400 << 10 // bytes of one embedded JSON blob worth keeping
	maxTables   = 30
	maxRows     = 300
)

func walkNodes(n *html.Node, f func(*html.Node) bool) {
	if n.Type == html.ElementNode && !f(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkNodes(c, f)
	}
}

var (
	windowVarRe = regexp.MustCompile(`window\.(__[A-Za-z0-9_$]+__)\s*=\s*`)
	ldTypeRe    = regexp.MustCompile(`(?i)ld\+json`)
)

// balancedJSON decodes the first JSON value in s.
func balancedJSON(s string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

func flattenLD(v any, out *[]any) {
	switch x := v.(type) {
	case []any:
		for _, it := range x {
			flattenLD(it, out)
		}
	case map[string]any:
		if g, ok := x["@graph"]; ok {
			flattenLD(g, out)
			rest := map[string]any{}
			for k, val := range x {
				if k != "@graph" {
					rest[k] = val
				}
			}
			if len(rest) > 1 { // more than an @context
				*out = append(*out, rest)
			}
			return
		}
		*out = append(*out, x)
	}
}

// ExtractStructured reads the machine-readable content of an HTML page.
func ExtractStructured(page *Page) (*Structured, error) {
	doc, err := Parse(page.Body)
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(page.FinalURL)
	s := &Structured{Title: Title(doc), Meta: map[string]string{}}
	if h := find(doc, atom.Html); h != nil {
		s.Language = attr(h, "lang")
	}
	seenEmbedded := map[string]bool{}
	walkNodes(doc, func(n *html.Node) bool {
		switch n.DataAtom {
		case atom.Meta:
			key := strings.ToLower(firstNonEmptyStr(attr(n, "property"), attr(n, "name"), attr(n, "itemprop")))
			val := strings.TrimSpace(attr(n, "content"))
			if key == "" || val == "" {
				break
			}
			switch {
			case strings.HasPrefix(key, "og:"), strings.HasPrefix(key, "twitter:"), strings.HasPrefix(key, "article:"), strings.HasPrefix(key, "product:"), strings.HasPrefix(key, "music:"), strings.HasPrefix(key, "video:"),
				key == "author", key == "keywords", key == "robots", key == "generator", key == "theme-color", key == "date", key == "pubdate":
				if _, dup := s.Meta[key]; !dup {
					s.Meta[key] = val
				}
			case key == "description":
				s.Description = val
			}
		case atom.Link:
			rel, typ := strings.ToLower(attr(n, "rel")), strings.ToLower(attr(n, "type"))
			href := resolve(base, attr(n, "href"))
			switch {
			case rel == "canonical":
				s.Canonical = href
			case strings.Contains(rel, "alternate") && (strings.Contains(typ, "rss") || strings.Contains(typ, "atom") || strings.Contains(typ, "feed+json") || typ == "application/json"):
				s.Feeds = append(s.Feeds, FeedLink{URL: href, Title: attr(n, "title"), Type: typ})
			}
		case atom.Script:
			body := ""
			if n.FirstChild != nil {
				body = n.FirstChild.Data
			}
			typ := attr(n, "type")
			switch {
			case ldTypeRe.MatchString(typ):
				if v, ok := balancedJSON(strings.TrimSpace(body)); ok {
					flattenLD(v, &s.JSONLD)
				}
			case strings.Contains(typ, "json") && len(body) > 2 && len(body) <= maxEmbedded:
				name := firstNonEmptyStr(attr(n, "id"), attr(n, "data-name"), "json")
				if v, ok := balancedJSON(strings.TrimSpace(body)); ok && !seenEmbedded[name+strconv.Itoa(len(body))] {
					seenEmbedded[name+strconv.Itoa(len(body))] = true
					s.Embedded = append(s.Embedded, Embedded{Name: name, Data: v, Size: len(body)})
				}
			case typ == "" || strings.Contains(typ, "javascript"):
				if len(body) > 20 && len(body) < 4<<20 {
					for _, m := range windowVarRe.FindAllStringSubmatchIndex(body, 8) {
						if v, ok := balancedJSON(body[m[1]:]); ok {
							if raw, _ := json.Marshal(v); len(raw) > 2 && len(raw) <= maxEmbedded {
								s.Embedded = append(s.Embedded, Embedded{Name: body[m[2]:m[3]], Data: v, Size: len(raw)})
							}
						}
					}
				}
			}
		case atom.Table:
			if len(s.Tables) < maxTables {
				if t, ok := readTable(n); ok {
					s.Tables = append(s.Tables, t)
				}
			}
			return true
		}
		return true
	})
	s.Microdata = readMicrodata(doc, base)
	s.Article = readArticle(doc, base, s)
	if len(s.Meta) == 0 {
		s.Meta = nil
	}
	return s, nil
}

func firstNonEmptyStr(v ...string) string {
	for _, x := range v {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func readTable(t *html.Node) (Table, bool) {
	var tb Table
	if c := find(t, atom.Caption); c != nil {
		tb.Caption = textOf(c)
	}
	var rows [][]string
	var headerRow []string
	walkNodes(t, func(n *html.Node) bool {
		if n.DataAtom == atom.Table && n != t { // a nested table is read on its own
			return false
		}
		if n.DataAtom != atom.Tr {
			return true
		}
		var cells []string
		allTh := true
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
				cells = append(cells, textOf(c))
				if c.DataAtom != atom.Th {
					allTh = false
				}
			}
		}
		if len(cells) == 0 {
			return false
		}
		if allTh && headerRow == nil && len(rows) == 0 {
			headerRow = cells
		} else if len(rows) < maxRows {
			rows = append(rows, cells)
		}
		return false
	})
	if len(rows) == 0 || (len(rows) == 1 && len(rows[0]) < 2 && headerRow == nil) {
		return tb, false // a layout table
	}
	tb.Headers, tb.Rows = headerRow, rows
	return tb, true
}

func readMicrodata(doc *html.Node, base *url.URL) []map[string]any {
	var out []map[string]any
	var read func(n *html.Node) map[string]any
	read = func(scope *html.Node) map[string]any {
		item := map[string]any{}
		if t := attr(scope, "itemtype"); t != "" {
			item["@type"] = t[strings.LastIndex(t, "/")+1:]
		}
		var visit func(n *html.Node)
		visit = func(n *html.Node) {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type != html.ElementNode {
					continue
				}
				if prop := attr(c, "itemprop"); prop != "" {
					var val any
					switch {
					case c.Attr != nil && hasAttr(c, "itemscope"):
						val = read(c)
					case c.DataAtom == atom.Meta:
						val = attr(c, "content")
					case c.DataAtom == atom.A || c.DataAtom == atom.Link:
						val = resolve(base, attr(c, "href"))
					case c.DataAtom == atom.Img || c.DataAtom == atom.Source || c.DataAtom == atom.Video || c.DataAtom == atom.Audio:
						val = resolve(base, attr(c, "src"))
					case c.DataAtom == atom.Time && attr(c, "datetime") != "":
						val = attr(c, "datetime")
					default:
						if v := attr(c, "content"); v != "" {
							val = v
						} else {
							val = textOf(c)
						}
					}
					if prev, ok := item[prop]; ok {
						if arr, isArr := prev.([]any); isArr {
							item[prop] = append(arr, val)
						} else {
							item[prop] = []any{prev, val}
						}
					} else {
						item[prop] = val
					}
					if hasAttr(c, "itemscope") {
						continue
					}
				}
				if !hasAttr(c, "itemscope") {
					visit(c)
				}
			}
		}
		visit(scope)
		return item
	}
	walkNodes(doc, func(n *html.Node) bool {
		if hasAttr(n, "itemscope") && attr(n, "itemprop") == "" && len(out) < 60 {
			if it := read(n); len(it) > 0 {
				out = append(out, it)
			}
			return false
		}
		return true
	})
	return out
}

func hasAttr(n *html.Node, k string) bool {
	for _, a := range n.Attr {
		if a.Key == k {
			return true
		}
	}
	return false
}

// readArticle pulls the main readable text with its byline and date, when the page looks like an article.
func readArticle(doc *html.Node, base *url.URL, s *Structured) *Article {
	main := mainNode(doc)
	text := Markdown(main, base, false)
	if len(text) < 400 {
		return nil
	}
	a := &Article{Title: firstNonEmptyStr(s.Meta["og:title"], s.Title), Text: text}
	a.Author = firstNonEmptyStr(s.Meta["article:author"], s.Meta["author"])
	a.Published = firstNonEmptyStr(s.Meta["article:published_time"], s.Meta["date"], s.Meta["pubdate"])
	for _, e := range s.JSONLD { // JSON-LD is the most reliable byline
		if m, ok := e.(map[string]any); ok {
			if a.Published == "" {
				if d, ok := m["datePublished"].(string); ok {
					a.Published = d
				}
			}
			if a.Author == "" {
				switch au := m["author"].(type) {
				case string:
					a.Author = au
				case map[string]any:
					a.Author, _ = au["name"].(string)
				case []any:
					var names []string
					for _, x := range au {
						if mm, ok := x.(map[string]any); ok {
							if nm, ok := mm["name"].(string); ok {
								names = append(names, nm)
							}
						}
					}
					a.Author = strings.Join(names, ", ")
				}
			}
		}
	}
	return a
}
