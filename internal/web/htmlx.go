package web

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Parse parses an HTML document.
func Parse(src string) (*html.Node, error) { return html.Parse(strings.NewReader(src)) }

var junkTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Svg: true, atom.Iframe: true, atom.Template: true,
	atom.Canvas: true, atom.Object: true, atom.Embed: true, atom.Link: true, atom.Meta: true, atom.Head: true,
}

var chromeTags = map[atom.Atom]bool{atom.Nav: true, atom.Footer: true, atom.Aside: true, atom.Form: true, atom.Header: false}

var blockTags = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true, atom.Main: true, atom.Ul: true, atom.Ol: true, atom.Li: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true, atom.Table: true, atom.Tr: true,
	atom.Blockquote: true, atom.Pre: true, atom.Br: true, atom.Hr: true, atom.Figure: true, atom.Header: true, atom.Dl: true, atom.Dt: true, atom.Dd: true,
}

func attr(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}

func find(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := find(c, a); r != nil {
			return r
		}
	}
	return nil
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && junkTags[n.DataAtom] {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(sb.String()), " ")
}

// Title returns the document title.
func Title(doc *html.Node) string {
	if t := find(doc, atom.Title); t != nil {
		return textOf(t)
	}
	if h := find(doc, atom.H1); h != nil {
		return textOf(h)
	}
	return ""
}

func resolve(base *url.URL, ref string) string {
	if base == nil || ref == "" {
		return ref
	}
	u, err := base.Parse(ref)
	if err != nil {
		return ref
	}
	return u.String()
}

// mainNode picks <main>/<article> when it holds most of the text, else <body>.
func mainNode(doc *html.Node) *html.Node {
	body := find(doc, atom.Body)
	if body == nil {
		return doc
	}
	total := len(textOf(body))
	for _, a := range []atom.Atom{atom.Main, atom.Article} {
		if n := find(body, a); n != nil && len(textOf(n))*100 > total*45 {
			return n
		}
	}
	return body
}

// Markdown renders readable markdown-ish text from HTML.
func Markdown(doc *html.Node, base *url.URL, links bool) string {
	var sb strings.Builder
	var walk func(n *html.Node, inPre bool, listDepth int)
	nl := func() {
		s := sb.String()
		if !strings.HasSuffix(s, "\n") && s != "" {
			sb.WriteByte('\n')
		}
	}
	blank := func() {
		nl()
		if s := sb.String(); s != "" && !strings.HasSuffix(s, "\n\n") {
			sb.WriteByte('\n')
		}
	}
	walk = func(n *html.Node, inPre bool, depth int) {
		switch n.Type {
		case html.TextNode:
			if inPre {
				sb.WriteString(n.Data)
				return
			}
			t := strings.Join(strings.Fields(n.Data), " ")
			if t == "" {
				if strings.ContainsAny(n.Data, " \n\t") && sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") && !strings.HasSuffix(sb.String(), " ") {
					sb.WriteByte(' ')
				}
				return
			}
			if strings.HasPrefix(n.Data, " ") && sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") && !strings.HasSuffix(sb.String(), " ") {
				sb.WriteByte(' ')
			}
			sb.WriteString(t)
			if strings.HasSuffix(n.Data, " ") {
				sb.WriteByte(' ')
			}
			return
		case html.ElementNode:
		default:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c, inPre, depth)
			}
			return
		}
		if junkTags[n.DataAtom] || chromeTags[n.DataAtom] || attr(n, "hidden") != "" || attr(n, "aria-hidden") == "true" {
			return
		}
		switch n.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			blank()
			sb.WriteString(strings.Repeat("#", int(n.Data[1]-'0')) + " ")
			sb.WriteString(textOf(n))
			blank()
			return
		case atom.A:
			t := textOf(n)
			href := resolve(base, attr(n, "href"))
			if links && t != "" && href != "" && !strings.HasPrefix(href, "javascript:") && !strings.HasPrefix(href, "#") {
				sb.WriteString("[" + t + "](" + href + ")")
			} else {
				sb.WriteString(t)
			}
			return
		case atom.Img:
			if alt := strings.TrimSpace(attr(n, "alt")); alt != "" {
				sb.WriteString("[img: " + alt + "]")
			}
			return
		case atom.Pre:
			blank()
			sb.WriteString("```\n")
			var raw strings.Builder
			var collect func(*html.Node)
			collect = func(x *html.Node) {
				if x.Type == html.TextNode {
					raw.WriteString(x.Data)
				}
				for c := x.FirstChild; c != nil; c = c.NextSibling {
					collect(c)
				}
			}
			collect(n)
			sb.WriteString(strings.Trim(raw.String(), "\n"))
			sb.WriteString("\n```")
			blank()
			return
		case atom.Code:
			sb.WriteString("`" + textOf(n) + "`")
			return
		case atom.Strong, atom.B:
			sb.WriteString("**" + textOf(n) + "**")
			return
		case atom.Li:
			nl()
			sb.WriteString(strings.Repeat("  ", max(depth-1, 0)) + "- ")
		case atom.Ul, atom.Ol:
			depth++
		case atom.Tr:
			nl()
			var cells []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
					cells = append(cells, textOf(c))
				} else if c.Type == html.ElementNode && (c.DataAtom == atom.Tbody || c.DataAtom == atom.Thead) {
					walk(c, inPre, depth)
				}
			}
			if len(cells) > 0 {
				sb.WriteString("| " + strings.Join(cells, " | ") + " |")
				nl()
			}
			return
		case atom.Blockquote:
			blank()
			sb.WriteString("> ")
		case atom.Br:
			sb.WriteByte('\n')
			return
		case atom.Hr:
			blank()
			sb.WriteString("---")
			blank()
			return
		}
		isBlock := blockTags[n.DataAtom]
		if isBlock && n.DataAtom != atom.Li {
			nl()
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inPre, depth)
		}
		if isBlock {
			if n.DataAtom == atom.P || n.DataAtom == atom.Table {
				blank()
			} else {
				nl()
			}
		}
	}
	walk(mainNode(doc), false, 0)
	out := sb.String()
	out = regexp.MustCompile(`[ \t]+\n`).ReplaceAllString(out, "\n")
	out = regexp.MustCompile(`\n{3,}`).ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// ── DOM → JSON (the domcap idea, done in-process) ───────────────────────────

// DOMNode is the JSON shape: {"tag","attrs","text","children"}.
type DOMNode struct {
	Tag      string            `json:"tag,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	Text     string            `json:"text,omitempty"`
	Children []*DOMNode        `json:"children,omitempty"`
}

// DOMOptions control which parts are kept.
type DOMOptions struct {
	Strip     []string // extra tags to remove
	KeepAttrs []string // attribute allow-list; nil → all
	Base      *url.URL
}

func BuildDOM(n *html.Node, o DOMOptions) *DOMNode {
	strip := map[string]bool{}
	for _, t := range o.Strip {
		strip[strings.ToLower(strings.TrimSpace(t))] = true
	}
	keep := map[string]bool{}
	for _, a := range o.KeepAttrs {
		keep[a] = true
	}
	var walk func(*html.Node) *DOMNode
	walk = func(n *html.Node) *DOMNode {
		switch n.Type {
		case html.TextNode:
			t := strings.Join(strings.Fields(n.Data), " ")
			if t == "" {
				return nil
			}
			return &DOMNode{Text: t}
		case html.ElementNode:
			if junkTags[n.DataAtom] && n.DataAtom != atom.Head || strip[n.Data] {
				return nil
			}
			d := &DOMNode{Tag: n.Data}
			for _, a := range n.Attr {
				if len(keep) > 0 && !keep[a.Key] {
					continue
				}
				if d.Attrs == nil {
					d.Attrs = map[string]string{}
				}
				v := a.Val
				if (a.Key == "href" || a.Key == "src") && o.Base != nil {
					v = resolve(o.Base, v)
				}
				d.Attrs[a.Key] = v
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if k := walk(c); k != nil {
					d.Children = append(d.Children, k)
				}
			}
			return d
		}
		var frag *DOMNode
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if k := walk(c); k != nil {
				if frag == nil {
					frag = k
				} else if k.Tag == "html" {
					frag = k
				}
			}
		}
		return frag
	}
	root := n
	for root != nil && root.Type == html.DocumentNode {
		root = root.FirstChild
		for root != nil && root.Type != html.ElementNode {
			root = root.NextSibling
		}
	}
	if root == nil {
		return &DOMNode{}
	}
	return walk(root)
}

func DOMJSON(n *DOMNode) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// ── compact HTML for LLM extraction ─────────────────────────────────────────

var keepAttrsCompact = map[string]bool{"href": true, "src": true, "alt": true, "title": true, "datetime": true, "itemprop": true, "aria-label": true, "id": true, "class": true, "data-price": true, "content": true}

// Compact renders a token-lean HTML view: junk removed, attributes filtered,
// class trimmed, single-child wrappers flattened, whitespace collapsed.
func Compact(doc *html.Node, base *url.URL) string {
	var sb strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			if t := strings.Join(strings.Fields(n.Data), " "); t != "" {
				sb.WriteString(t)
				sb.WriteByte(' ')
			}
			return
		case html.ElementNode:
			if junkTags[n.DataAtom] || attr(n, "hidden") != "" || attr(n, "aria-hidden") == "true" {
				return
			}
			if n.DataAtom == atom.Div || n.DataAtom == atom.Span || n.DataAtom == atom.Section {
				// flatten wrappers with no meaningful attributes
				meaningful := false
				for _, a := range n.Attr {
					if a.Key == "itemprop" || a.Key == "datetime" || a.Key == "id" || a.Key == "class" {
						meaningful = true
					}
				}
				if !meaningful {
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						walk(c)
					}
					return
				}
			}
			sb.WriteString("<" + n.Data)
			for _, a := range n.Attr {
				if !keepAttrsCompact[a.Key] || a.Val == "" {
					continue
				}
				v := a.Val
				switch a.Key {
				case "href", "src":
					v = resolve(base, v)
					if strings.HasPrefix(v, "data:") || len(v) > 300 {
						continue
					}
				case "class":
					f := strings.Fields(v)
					if len(f) > 3 {
						f = f[:3]
					}
					v = strings.Join(f, " ")
				}
				sb.WriteString(" " + a.Key + `="` + strings.ReplaceAll(v, `"`, "'") + `"`)
			}
			sb.WriteByte('>')
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			sb.WriteString("</" + n.Data + ">")
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(mainNodeForExtract(doc))
	return strings.TrimSpace(sb.String())
}

func mainNodeForExtract(doc *html.Node) *html.Node {
	if b := find(doc, atom.Body); b != nil {
		return b
	}
	return doc
}

// Chunk splits compact HTML into pieces of at most max chars, breaking at tag boundaries.
func Chunk(s string, max int) []string {
	if len(s) <= max {
		return []string{s}
	}
	var out []string
	for len(s) > max {
		cut := strings.LastIndex(s[:max], "><")
		if cut < max/2 {
			cut = strings.LastIndex(s[:max], "> ")
		}
		if cut < max/2 {
			cut = max - 1
		}
		out = append(out, s[:cut+1])
		s = strings.TrimLeft(s[cut+1:], " ")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// Links extracts (text, url) pairs.
func Links(doc *html.Node, base *url.URL) [][2]string {
	var out [][2]string
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.A {
			href := resolve(base, attr(n, "href"))
			if href != "" && !strings.HasPrefix(href, "javascript:") && !strings.HasPrefix(href, "#") && !seen[href] {
				seen[href] = true
				out = append(out, [2]string{textOf(n), href})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}
