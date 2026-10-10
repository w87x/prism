package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"prism/internal/settings"
)

// Search through the big engines' own result pages, rendered like a person would see them — Bing and Google no longer sell
// a plain search API (Microsoft shut the Bing one down in August 2025). These are "browser-search plugins": free, keyless,
// and as good as the engine, but they are scrapes of a page whose markup the engine may change, and an engine may answer
// with a captcha or consent page when asked too much; the error then says so. They are meant as fallbacks behind the API
// providers (see the search order in Settings).

// BrowserHTML renders a page in PRISM's own browser and returns its HTML. Wired from Service.RegisterTools; nil when there
// is no browser, in which case Google (which needs one) is unavailable and Bing falls back to a plain request only.
var BrowserHTML func(ctx context.Context, rawURL, waitSelector string) (string, error)

func browserAvailable() bool { return BrowserHTML != nil }

// the result-page addresses are variables so tests can point them at a local server
var (
	bingSearchURL = "https://www.bing.com/search?"
	bingImagesURL = "https://www.bing.com/images/search?"
)

func httpHTML(ctx context.Context, c settings.Web, rawURL string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	req.Header.Set("User-Agent", userAgent(c))
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b := readAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(b), nil
}

// serpPage fetches a results page through the browser when there is one. Plain HTTP is only the fallback for engines whose
// finished HTML is still usable without one (Bing's image tiles); Bing's plain-HTTP web results are degraded — a multi-word
// query comes back answering only its first word — so its web search requires the browser.
func serpPage(ctx context.Context, c settings.Web, rawURL, waitSel string, plainFallback bool, blocked func(string) bool) (string, error) {
	if browserAvailable() {
		body, err := BrowserHTML(ctx, rawURL, waitSel)
		if err == nil && !blocked(body) {
			return body, nil
		}
		if !plainFallback {
			if err == nil {
				err = errors.New("the engine answered with a block page")
			}
			return "", err
		}
	} else if !plainFallback {
		return "", errors.New("no browser is available to render the results page")
	}
	body, err := httpHTML(ctx, c, rawURL)
	if err != nil {
		return "", err
	}
	if blocked(body) {
		return "", errors.New("the engine answered with a block page")
	}
	return body, nil
}

func hasClassTok(n *html.Node, tok string) bool {
	for _, f := range strings.Fields(attr(n, "class")) {
		if f == tok {
			return true
		}
	}
	return false
}

func walk(n *html.Node, f func(*html.Node) bool) {
	if n == nil || !f(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, f)
	}
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// ── Bing ─────────────────────────────────────────────────────────────────────

// bingBlocked: a page without results that mentions a challenge — Bing asks for a captcha when it is asked too much
func bingBlocked(body string) bool {
	return !strings.Contains(body, "b_algo") && (strings.Contains(body, "b_captcha") || strings.Contains(body, "captcha") || strings.Contains(body, "Solve the challenge") || len(body) < 4000)
}

func searchBing(ctx context.Context, c settings.Web, q string, limit int) ([]Result, error) {
	u := bingSearchURL + url.Values{"q": {q}, "setlang": {"en"}, "cc": {"us"}}.Encode()
	body, err := serpPage(ctx, c, u, "li.b_algo", false, bingBlocked)
	if err != nil {
		return nil, err
	}
	return parseBing(body, limit)
}

// bingTarget turns Bing's tracking link (bing.com/ck/a?…&u=a1<base64url of the address>) into the real address.
func bingTarget(href string) string {
	u, err := url.Parse(href)
	if err != nil || !strings.HasSuffix(u.Hostname(), "bing.com") || !strings.HasPrefix(u.Path, "/ck/") {
		return href
	}
	v := u.Query().Get("u")
	if strings.HasPrefix(v, "a1") {
		if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(v[2:], "=")); err == nil && strings.HasPrefix(string(b), "http") {
			return string(b)
		}
	}
	return href
}

func parseBing(body string, limit int) ([]Result, error) {
	doc, err := Parse(body)
	if err != nil {
		return nil, err
	}
	var out []Result
	walk(doc, func(n *html.Node) bool {
		if len(out) >= limit {
			return false
		}
		if n.Type != html.ElementNode || n.Data != "li" || !hasClassTok(n, "b_algo") {
			return true
		}
		var title, href, snippet string
		walk(n, func(m *html.Node) bool {
			if m.Type != html.ElementNode {
				return true
			}
			switch {
			case m.Data == "h2" && title == "":
				walk(m, func(x *html.Node) bool {
					if x.Type == html.ElementNode && x.Data == "a" && href == "" {
						href, title = attr(x, "href"), collapse(textOf(x))
						return false
					}
					return true
				})
				return false
			case m.Data == "p" && snippet == "":
				snippet = collapse(textOf(m))
			}
			return true
		})
		if href != "" && title != "" {
			out = append(out, Result{Title: title, URL: bingTarget(href), Snippet: clip(snippet, 400)})
		}
		return false
	})
	return out, nil
}

// searchImagesBing reads Bing's image results: each tile carries its data as JSON in the "m" attribute (murl is the image
// file, purl the page, t the title).
func searchImagesBing(ctx context.Context, c settings.Web, q string, limit int) ([]ImageResult, error) {
	u := bingImagesURL + url.Values{"q": {q}, "setlang": {"en"}, "first": {"1"}}.Encode()
	body, err := serpPage(ctx, c, u, "a.iusc", true, func(b string) bool { return !strings.Contains(b, "iusc") })
	if err != nil {
		return nil, err
	}
	return parseBingImages(body, limit)
}

func parseBingImages(body string, limit int) ([]ImageResult, error) {
	doc, err := Parse(body)
	if err != nil {
		return nil, err
	}
	var out []ImageResult
	walk(doc, func(n *html.Node) bool {
		if len(out) >= limit {
			return false
		}
		if n.Type == html.ElementNode && n.Data == "a" && hasClassTok(n, "iusc") {
			var m struct {
				Murl string `json:"murl"`
				Purl string `json:"purl"`
				T    string `json:"t"`
			}
			if json.Unmarshal([]byte(attr(n, "m")), &m) == nil && strings.HasPrefix(m.Murl, "http") {
				out = append(out, ImageResult{Title: clip(collapse(html.UnescapeString(m.T)), 160), Image: m.Murl, Page: m.Purl})
			}
			return false
		}
		return true
	})
	return out, nil
}

// ── Google ───────────────────────────────────────────────────────────────────

var crumbRe = regexp.MustCompile(`^https?://[^\s›]+(\s*›.*)?$`)

func googleBlocked(body string) bool {
	return !strings.Contains(body, "<h3") && (strings.Contains(body, "unusual traffic") || strings.Contains(body, "/sorry/") || strings.Contains(body, "recaptcha"))
}

func searchGoogle(ctx context.Context, c settings.Web, q string, limit int) ([]Result, error) {
	if !browserAvailable() {
		return nil, errors.New("Google needs a browser to render its results, and none is available")
	}
	u := "https://www.google.com/search?" + url.Values{"q": {q}, "hl": {"en"}, "num": {fmt.Sprint(min(max(limit, 5), 20))}, "pws": {"0"}}.Encode()
	// Google serves a JavaScript challenge to plain requests: always the browser (wait for the first title)
	body, err := BrowserHTML(ctx, u, "a h3")
	if err != nil {
		return nil, err
	}
	res, err := parseGoogle(body, limit)
	if err == nil && len(res) == 0 && googleBlocked(body) {
		return nil, errors.New("Google asked for a captcha — open PRISM's browser once, search something on google.com and solve it")
	}
	return res, err
}

// parseGoogle reads the rendered results page. Google no longer puts the real address in the link (it is an opaque
// /goto?url= token), so the address is rebuilt from the breadcrumb it prints under the title ("https://site › path › leaf");
// when Google abbreviated that ("› … ›") only the site's address is certain, and the result says so.
func parseGoogle(body string, limit int) ([]Result, error) {
	doc, err := Parse(body)
	if err != nil {
		return nil, err
	}
	var out []Result
	seen := map[string]bool{}
	walk(doc, func(n *html.Node) bool {
		if len(out) >= limit {
			return false
		}
		if n.Type != html.ElementNode || n.Data != "a" {
			return true
		}
		var h3 *html.Node
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "h3" {
				h3 = c
			}
		}
		if h3 == nil {
			return true
		}
		title := collapse(textOf(h3))
		var crumb string
		walk(n, func(m *html.Node) bool {
			if m.Type == html.TextNode && crumb == "" {
				if t := collapse(m.Data); crumbRe.MatchString(t) {
					crumb = t
				}
			}
			return true
		})
		addr, partial := googleAddress(crumb, attr(n, "href"))
		if title == "" || addr == "" || seen[addr+title] {
			return false
		}
		seen[addr+title] = true
		// the snippet: the text of the result block after the title and breadcrumb
		block := n
		for i := 0; i < 6 && block.Parent != nil; i++ {
			block = block.Parent
			if len(collapse(textOf(block))) > len(title)+len(crumb)+60 {
				break
			}
		}
		sn := collapse(textOf(block))
		if i := strings.Index(sn, crumb); crumb != "" && i >= 0 {
			sn = strings.TrimSpace(sn[i+len(crumb):])
		} else {
			sn = strings.TrimSpace(strings.TrimPrefix(sn, title))
		}
		sn = strings.TrimSuffix(sn, "Read more")
		if partial {
			sn = "(Google abbreviated the address; this is the site, open it or search further) " + sn
		}
		out = append(out, Result{Title: title, URL: addr, Snippet: clip(strings.TrimSpace(sn), 400)})
		return false
	})
	return out, nil
}

func googleAddress(crumb, href string) (addr string, partial bool) {
	if u, err := url.Parse(href); err == nil && u.Scheme != "" && !strings.HasSuffix(u.Hostname(), "google.com") && strings.HasPrefix(href, "http") {
		return href, false
	}
	if crumb == "" {
		return "", false
	}
	parts := strings.Split(crumb, "›")
	base := strings.TrimSpace(parts[0])
	if !strings.HasPrefix(base, "http") {
		return "", false
	}
	if len(parts) == 1 {
		return base, false
	}
	var segs []string
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if p == "" || strings.Contains(p, "…") || strings.Contains(p, "...") || strings.Contains(p, " ") { // abbreviated or a page title, not a path part
			return base + "/", true
		}
		segs = append(segs, p)
	}
	return base + "/" + strings.Join(segs, "/"), false
}
