package builtin

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"prism/internal/netguard"
	"prism/internal/tools"
)

// FeedItem is one entry of an RSS/Atom feed.
type FeedItem struct {
	Title     string
	Link      string
	Published string
	Summary   string
	ID        string
}

// ReadFeed fetches and parses an RSS 2.0 or Atom feed. Private/loopback addresses are refused.
func ReadFeed(ctx context.Context, url string, limit int) (title string, items []FeedItem, err error) {
	return ReadFeedAllow(ctx, url, limit, false)
}

// ReadFeedAllow is ReadFeed with an explicit choice about private addresses (Settings → Web → allow private).
func ReadFeedAllow(ctx context.Context, url string, limit int, allowPrivate bool) (title string, items []FeedItem, err error) {
	if !allowPrivate {
		if err := netguard.CheckURL(ctx, url); err != nil {
			return "", nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (PRISM feed reader)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml, */*")
	resp, err := netguard.Client(allowPrivate, 30*time.Second).Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", nil, fmt.Errorf("feed returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", nil, err
	}
	var doc struct {
		XMLName xml.Name
		Channel struct {
			Title string `xml:"title"`
			Items []struct {
				Title string `xml:"title"`
				Link  string `xml:"link"`
				Date  string `xml:"pubDate"`
				Desc  string `xml:"description"`
				GUID  string `xml:"guid"`
			} `xml:"item"`
		} `xml:"channel"`
		Title   string `xml:"title"`
		Entries []struct {
			Title string `xml:"title"`
			Links []struct {
				Href string `xml:"href,attr"`
				Rel  string `xml:"rel,attr"`
			} `xml:"link"`
			Updated string `xml:"updated"`
			Publ    string `xml:"published"`
			Summary string `xml:"summary"`
			Content string `xml:"content"`
			ID      string `xml:"id"`
		} `xml:"entry"`
	}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	dec.Strict = false
	dec.CharsetReader = func(cs string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := dec.Decode(&doc); err != nil {
		return "", nil, fmt.Errorf("not a valid feed: %w", err)
	}
	if limit <= 0 {
		limit = 10
	}
	switch doc.XMLName.Local {
	case "rss", "RDF", "rdf":
		title = doc.Channel.Title
		for _, it := range doc.Channel.Items {
			id := it.GUID
			if id == "" {
				id = it.Link
			}
			items = append(items, FeedItem{Title: strip(it.Title), Link: strings.TrimSpace(it.Link), Published: it.Date, Summary: clip(strip(it.Desc), 240), ID: id})
		}
	case "feed":
		title = doc.Title
		for _, e := range doc.Entries {
			link := ""
			for _, l := range e.Links {
				if l.Rel == "" || l.Rel == "alternate" {
					link = l.Href
					break
				}
			}
			d := e.Publ
			if d == "" {
				d = e.Updated
			}
			s := e.Summary
			if s == "" {
				s = e.Content
			}
			items = append(items, FeedItem{Title: strip(e.Title), Link: link, Published: d, Summary: clip(strip(s), 240), ID: e.ID})
		}
	default:
		return "", nil, fmt.Errorf("unrecognised feed format <%s>", doc.XMLName.Local)
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return title, items, nil
}

func strip(s string) string {
	var sb strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			sb.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func registerRSS(reg *tools.Registry, d Deps) {
	reg.Register(&tools.Tool{
		Name: "rss_read", Category: "web", Risk: tools.RiskRead, Untrusted: true,
		Description: "Read an RSS/Atom feed: latest items with title, link, date and a short summary.",
		Params:      tools.Obj("url", tools.Str("url", "feed URL"), tools.Int("limit", "max items (default 10)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				URL   string
				Limit int
			}](raw)
			if err != nil {
				return "", err
			}
			title, items, err := ReadFeedAllow(ctx, a.URL, a.Limit, d.allowPrivate(ctx))
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Feed: %s (%d items)\n", title, len(items))
			for _, it := range items {
				fmt.Fprintf(&sb, "- %s [%s]\n  %s\n  %s\n", it.Title, it.Published, it.Link, it.Summary)
			}
			return sb.String(), nil
		},
	})
}
