package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"prism/internal/settings"
)

// ImageResult is one picture found by an image search: where the image file is, what it is called, and the page it sits on.
type ImageResult struct {
	Title string `json:"title"`
	Image string `json:"image"` // the image file itself
	Page  string `json:"page"`  // the page it was found on
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
}

// the endpoints are variables so tests can point them at a local server
var (
	ddgImagePageURL = "https://duckduckgo.com/"
	ddgImageJSONURL = "https://duckduckgo.com/i.js"
	tavilySearchURL = "https://api.tavily.com/search"
)

func joinErr(prev error, next string) error {
	if prev == nil {
		return errors.New(next)
	}
	return fmt.Errorf("%v; %s", prev, next)
}

var vqdRe = regexp.MustCompile(`vqd=["']?([\d-]+)["']?`)

// SearchImages finds pictures for a query: through Tavily when the user has a key (it returns image addresses with
// descriptions), otherwise through DuckDuckGo's image search, which needs no key. It returns the provider it used.
func SearchImages(ctx context.Context, c settings.Web, q string, limit int) ([]ImageResult, string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, "", errors.New("say what to look for")
	}
	if limit <= 0 || limit > 12 {
		limit = 6
	}
	var firstErr error
	if c.TavilyKey != "" {
		if r, err := searchImagesTavily(ctx, c, q, limit); err == nil && len(r) > 0 {
			return r, "Tavily", nil
		} else if err != nil {
			firstErr = err
		}
	}
	// Bing's image page next (it serves the tiles without a token and with the full image address), then DuckDuckGo's JSON
	if r, err := searchImagesBing(ctx, c, q, limit); err == nil && len(r) > 0 {
		return r, "Bing", nil
	} else if err != nil {
		firstErr = joinErr(firstErr, "Bing: "+err.Error())
	}
	r, err := searchImagesDDG(ctx, c, q, limit)
	if err != nil {
		return nil, "", joinErr(firstErr, "DuckDuckGo: "+err.Error())
	}
	return r, "DuckDuckGo", nil
}

func searchImagesTavily(ctx context.Context, c settings.Web, q string, limit int) ([]ImageResult, error) {
	body, _ := json.Marshal(map[string]any{"query": q, "max_results": 3, "search_depth": "basic", "include_images": true, "include_image_descriptions": true})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tavilySearchURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.TavilyKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b := readAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Tavily HTTP %d", resp.StatusCode)
	}
	var r struct {
		Images []json.RawMessage `json:"images"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	var out []ImageResult
	for _, raw := range r.Images {
		var s string
		var o struct{ URL, Description string }
		switch {
		case json.Unmarshal(raw, &s) == nil && s != "":
			out = append(out, ImageResult{Image: s})
		case json.Unmarshal(raw, &o) == nil && o.URL != "":
			out = append(out, ImageResult{Image: o.URL, Title: clip(o.Description, 160)})
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// searchImagesDDG asks DuckDuckGo's image search: the search page hands out a token (vqd) that the JSON endpoint requires.
func searchImagesDDG(ctx context.Context, c settings.Web, q string, limit int) ([]ImageResult, error) {
	ua := userAgent(c)
	page, _ := http.NewRequestWithContext(ctx, http.MethodGet, ddgImagePageURL+"?"+url.Values{"q": {q}, "iax": {"images"}, "ia": {"images"}}.Encode(), nil)
	page.Header.Set("User-Agent", ua)
	resp, err := client.Do(page)
	if err != nil {
		return nil, err
	}
	html := readAll(resp.Body)
	resp.Body.Close()
	m := vqdRe.FindSubmatch(html)
	if m == nil {
		return nil, errors.New("image search is not available right now (no search token)")
	}
	api, _ := http.NewRequestWithContext(ctx, http.MethodGet, ddgImageJSONURL+"?"+url.Values{"l": {"us-en"}, "o": {"json"}, "q": {q}, "vqd": {string(m[1])}, "f": {",,,,,"}, "p": {"1"}}.Encode(), nil)
	api.Header.Set("User-Agent", ua)
	api.Header.Set("Referer", ddgImagePageURL)
	api.Header.Set("Accept", "application/json")
	resp, err = client.Do(api)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b := readAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		Results []struct {
			Image  string `json:"image"`
			Title  string `json:"title"`
			URL    string `json:"url"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	var out []ImageResult
	for _, x := range r.Results {
		if x.Image == "" {
			continue
		}
		out = append(out, ImageResult{Title: clip(x.Title, 160), Image: x.Image, Page: x.URL, W: x.Width, H: x.Height})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
