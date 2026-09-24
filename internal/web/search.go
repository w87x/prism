package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"prism/internal/settings"
)

type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type provider struct {
	id        string
	label     string
	available func(c settings.Web) bool
	search    func(ctx context.Context, c settings.Web, q string, limit int) ([]Result, error)
}

var client = &http.Client{Timeout: 30 * time.Second}

func readAll(r io.Reader) []byte { b, _ := io.ReadAll(io.LimitReader(r, 4<<20)); return b }

func providers() []provider {
	return []provider{
		{"yandex", "Yandex Search", func(c settings.Web) bool { return c.YandexKey != "" && c.YandexFolder != "" }, searchYandex},
		{"anysearch", "AnySearch", func(c settings.Web) bool { return c.AnySearchKey != "" }, searchAnySearch},
		{"tavily", "Tavily", func(c settings.Web) bool { return c.TavilyKey != "" }, searchTavily},
		{"ddg", "DuckDuckGo", func(c settings.Web) bool { return true }, searchDDG},
	}
}

// ProviderInfo describes a provider for the settings UI.
type ProviderInfo struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
}

func ProviderList(c settings.Web) []ProviderInfo {
	var out []ProviderInfo
	for _, p := range providers() {
		out = append(out, ProviderInfo{p.id, p.label, p.available(c)})
	}
	return out
}

func DefaultOrder() []string { return []string{"yandex", "anysearch", "tavily", "ddg"} }

// Search runs the query through the configured providers in order until one answers.
func Search(ctx context.Context, c settings.Web, query string, limit int) (results []Result, used string, err error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	order := c.SearchOrder
	if len(order) == 0 {
		order = DefaultOrder()
	}
	byID := map[string]provider{}
	for _, p := range providers() {
		byID[p.id] = p
	}
	var errs []string
	for _, id := range order {
		p, ok := byID[id]
		if !ok || !p.available(c) {
			continue
		}
		res, err := p.search(ctx, c, query, limit)
		if err != nil {
			errs = append(errs, id+": "+err.Error())
			continue
		}
		if len(res) == 0 {
			errs = append(errs, id+": no results")
			continue
		}
		return res, id, nil
	}
	if len(errs) == 0 {
		return nil, "", errors.New("no search provider is available")
	}
	return nil, "", errors.New(strings.Join(errs, "; "))
}

// ── DuckDuckGo (HTML endpoint, no key) ─────────────────────────────────────

func searchDDG(ctx context.Context, c settings.Web, q string, limit int) ([]Result, error) {
	form := url.Values{"q": {q}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent(c))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, err
	}
	var out []Result
	var walk func(*html.Node)
	var cur *Result
	hasClass := func(n *html.Node, cls string) bool {
		for _, f := range strings.Fields(attr(n, "class")) {
			if f == cls {
				return true
			}
		}
		return false
	}
	walk = func(n *html.Node) {
		if len(out) >= limit {
			return
		}
		if n.Type == html.ElementNode {
			switch {
			case n.Data == "a" && hasClass(n, "result__a"):
				href := attr(n, "href")
				if u, err := url.Parse(href); err == nil {
					if t := u.Query().Get("uddg"); t != "" {
						href = t
					} else if strings.HasPrefix(href, "//") {
						href = "https:" + href
					}
				}
				out = append(out, Result{Title: textOf(n), URL: href})
				cur = &out[len(out)-1]
			case cur != nil && hasClass(n, "result__snippet"):
				cur.Snippet = textOf(n)
				cur = nil
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	return out, nil
}

// ── Tavily ─────────────────────────────────────────────────────────────────

func searchTavily(ctx context.Context, c settings.Web, q string, limit int) ([]Result, error) {
	body, _ := json.Marshal(map[string]any{"query": q, "max_results": limit, "search_depth": "basic"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.TavilyKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b := readAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, clip(string(b), 200))
	}
	var r struct {
		Results []struct{ Title, URL, Content string } `json:"results"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	var out []Result
	for _, x := range r.Results {
		out = append(out, Result{x.Title, x.URL, clip(x.Content, 400)})
	}
	return out, nil
}

// ── AnySearch ──────────────────────────────────────────────────────────────

func searchAnySearch(ctx context.Context, c settings.Web, q string, limit int) ([]Result, error) {
	base := strings.TrimRight(c.AnySearchURL, "/")
	if base == "" {
		base = "https://api.anysearch.com"
	}
	body, _ := json.Marshal(map[string]any{"query": q, "max_results": min(limit, 10)})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.AnySearchKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b := readAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, clip(string(b), 200))
	}
	var r struct {
		Data struct {
			Results []anyItem `json:"results"`
		} `json:"data"`
		Results []anyItem `json:"results"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	items := r.Data.Results
	if len(items) == 0 {
		items = r.Results
	}
	var out []Result
	for _, x := range items {
		out = append(out, Result{x.Title, x.URL, clip(firstNonEmpty(x.Snippet, x.Content), 400)})
	}
	return out, nil
}

type anyItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Content string `json:"content"`
}

// ExtractAnySearch fetches page content via AnySearch's MCP "extract" tool.
func ExtractAnySearch(ctx context.Context, c settings.Web, target string) (string, error) {
	base := strings.TrimRight(c.AnySearchURL, "/")
	if base == "" {
		base = "https://api.anysearch.com"
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "extract", "arguments": map[string]any{"url": target}}})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.AnySearchKey)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Result struct {
			Content []struct{ Type, Text string } `json:"content"`
		} `json:"result"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(readAll(resp.Body), &r); err != nil {
		return "", err
	}
	if r.Error != nil {
		return "", errors.New(r.Error.Message)
	}
	var parts []string
	for _, b := range r.Result.Content {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// ── Yandex Search API v2 (async operation) ─────────────────────────────────

func searchYandex(ctx context.Context, c settings.Web, q string, limit int) ([]Result, error) {
	payload := map[string]any{
		"query":          map[string]any{"searchType": "SEARCH_TYPE_RU", "queryText": q, "page": "0"},
		"groupSpec":      map[string]any{"groupMode": "GROUP_MODE_FLAT", "groupsOnPage": fmt.Sprint(max(1, min(limit, 100))), "docsInGroup": "1"},
		"maxPassages":    "4",
		"folderId":       c.YandexFolder,
		"responseFormat": "FORMAT_XML",
	}
	if c.YandexRegion != "" {
		payload["region"] = c.YandexRegion
	}
	body, _ := json.Marshal(payload)
	do := func(req *http.Request) ([]byte, int, error) {
		req.Header.Set("Authorization", "Api-Key "+c.YandexKey)
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		return readAll(resp.Body), resp.StatusCode, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://searchapi.api.cloud.yandex.net/v2/web/searchAsync", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	b, code, err := do(req)
	if err != nil {
		return nil, err
	}
	if code/100 != 2 {
		return nil, fmt.Errorf("HTTP %d: %s", code, clip(string(b), 200))
	}
	var op struct {
		ID       string                    `json:"id"`
		Done     bool                      `json:"done"`
		Error    *struct{ Message string } `json:"error"`
		Response struct {
			RawData string `json:"rawData"`
		} `json:"response"`
	}
	if err := json.Unmarshal(b, &op); err != nil || op.ID == "" {
		return nil, errors.New("could not parse Yandex operation response")
	}
	deadline := time.Now().Add(20 * time.Second)
	for !op.Done {
		if time.Now().After(deadline) {
			return nil, errors.New("Yandex operation timed out")
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		greq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://operation.api.cloud.yandex.net/operations/"+op.ID, nil)
		b, code, err = do(greq)
		if err != nil {
			return nil, err
		}
		if code/100 != 2 {
			return nil, fmt.Errorf("operation status HTTP %d", code)
		}
		op.Response.RawData = ""
		if err := json.Unmarshal(b, &op); err != nil {
			return nil, err
		}
	}
	if op.Error != nil {
		return nil, errors.New(op.Error.Message)
	}
	raw, err := base64.StdEncoding.DecodeString(op.Response.RawData)
	if err != nil {
		return nil, fmt.Errorf("bad rawData: %w", err)
	}
	return parseYandexXML(raw, limit)
}

func parseYandexXML(raw []byte, limit int) ([]Result, error) {
	var doc struct {
		Response struct {
			Error struct {
				Code string `xml:"code,attr"`
				Text string `xml:",chardata"`
			} `xml:"error"`
			Docs []struct {
				URL      string     `xml:"url"`
				Title    innerXML   `xml:"title"`
				Headline innerXML   `xml:"headline"`
				Passages []innerXML `xml:"passages>passage"`
			} `xml:"results>grouping>group>doc"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if t := strings.TrimSpace(doc.Response.Error.Text); t != "" {
		return nil, fmt.Errorf("yandex error %s: %s", doc.Response.Error.Code, t)
	}
	var out []Result
	for _, d := range doc.Response.Docs {
		if len(out) >= limit {
			break
		}
		desc := strings.TrimSpace(string(d.Headline))
		if desc == "" && len(d.Passages) > 0 {
			desc = strings.TrimSpace(string(d.Passages[0]))
		}
		out = append(out, Result{strings.TrimSpace(string(d.Title)), strings.TrimSpace(d.URL), desc})
	}
	return out, nil
}

// innerXML captures an element's text including nested markup (<hlword>) flattened.
type innerXML string

func (s *innerXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var sb strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	*s = innerXML(sb.String())
	return nil
}

func userAgent(c settings.Web) string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func firstNonEmpty(a ...string) string {
	for _, x := range a {
		if x != "" {
			return x
		}
	}
	return ""
}
