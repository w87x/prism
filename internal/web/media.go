package web

import (
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// MediaItem is one picture, clip, stream or downloadable file found on a page.
type MediaItem struct {
	Kind    string `json:"kind"` // image | video | audio | embed | stream | file
	URL     string `json:"url"`
	Alt     string `json:"alt,omitempty"`
	Title   string `json:"title,omitempty"`
	Type    string `json:"type,omitempty"` // MIME type when the page says
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Caption string `json:"caption,omitempty"`
	Poster  string `json:"poster,omitempty"`
	Found   string `json:"found"` // where on the page it was found: img, srcset, og:image, video, source, iframe, json-ld, link, script, network
}

var (
	imageExts  = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".avif": true, ".svg": true, ".bmp": true, ".heic": true}
	videoExts  = map[string]bool{".mp4": true, ".webm": true, ".mov": true, ".mkv": true, ".m4v": true, ".avi": true, ".ogv": true}
	audioExts  = map[string]bool{".mp3": true, ".m4a": true, ".ogg": true, ".oga": true, ".wav": true, ".flac": true, ".aac": true, ".opus": true}
	streamExts = map[string]bool{".m3u8": true, ".mpd": true}
	fileExts   = map[string]bool{".pdf": true, ".zip": true, ".epub": true, ".docx": true, ".xlsx": true, ".pptx": true, ".csv": true, ".torrent": true, ".7z": true, ".rar": true, ".tar": true, ".gz": true, ".dmg": true, ".apk": true}
	embedHosts = []string{"youtube.com", "youtube-nocookie.com", "youtu.be", "vimeo.com", "dailymotion.com", "twitch.tv", "rutube.ru", "vk.com", "ok.ru", "streamable.com", "wistia.com", "loom.com", "bilibili.com", "tiktok.com"}
	scriptURL  = regexp.MustCompile(`https?:\\?/\\?/[^"'\s<>()]+?\.(?:m3u8|mpd|mp4|webm|mp3)(?:\?[^"'\s\\<>()]*)?`)
	srcsetRe   = regexp.MustCompile(`\s*([^\s,]+)(?:\s+(\d+(?:\.\d+)?)([wx]))?\s*(?:,|$)`)
)

func kindOfURL(u string) string {
	p := u
	if q := strings.IndexAny(p, "?#"); q >= 0 {
		p = p[:q]
	}
	ext := strings.ToLower(path.Ext(p))
	switch {
	case imageExts[ext]:
		return "image"
	case videoExts[ext]:
		return "video"
	case audioExts[ext]:
		return "audio"
	case streamExts[ext]:
		return "stream"
	case fileExts[ext]:
		return "file"
	}
	return ""
}

func isEmbedHost(u string) bool {
	pu, err := url.Parse(u)
	if err != nil {
		return false
	}
	h := strings.TrimPrefix(strings.ToLower(pu.Hostname()), "www.")
	for _, e := range embedHosts {
		if h == e || strings.HasSuffix(h, "."+e) {
			return true
		}
	}
	return false
}

// bestFromSrcset chooses the largest candidate of a srcset attribute.
func bestFromSrcset(v string, base *url.URL) (string, int) {
	best, bestScore, w := "", -1.0, 0
	for _, m := range srcsetRe.FindAllStringSubmatch(v, -1) {
		if m[1] == "" {
			continue
		}
		score := 1.0
		if m[2] != "" {
			f, _ := strconv.ParseFloat(m[2], 64)
			score = f
			if m[3] == "w" {
				w = int(f)
			}
		}
		if score > bestScore {
			best, bestScore = resolve(base, m[1]), score
		}
	}
	if bestScore < 0 || best == "" {
		return "", 0
	}
	return best, w
}

func intAttr(n *html.Node, k string) int {
	v := strings.TrimSuffix(strings.TrimSpace(attr(n, k)), "px")
	i, _ := strconv.Atoi(v)
	return i
}

func nearestCaption(n *html.Node) string {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.DataAtom == atom.Figure {
			if c := find(p, atom.Figcaption); c != nil {
				return textOf(c)
			}
			return ""
		}
	}
	return ""
}

// ExtractMedia lists the media a page offers. minWidth drops images known to be smaller (icons, tracking pixels);
// images of unknown size are kept.
func ExtractMedia(page *Page, minWidth int) ([]MediaItem, error) {
	doc, err := Parse(page.Body)
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(page.FinalURL)
	var out []MediaItem
	seen := map[string]bool{}
	add := func(m MediaItem) {
		if m.URL == "" || strings.HasPrefix(m.URL, "data:") || strings.HasPrefix(m.URL, "blob:") || strings.HasPrefix(m.URL, "javascript:") {
			return
		}
		key := m.Kind + "|" + m.URL
		if seen[key] || len(out) >= 400 {
			return
		}
		if m.Kind == "image" && m.Width > 0 && m.Width < minWidth {
			return
		}
		if m.Kind == "image" && ((m.Width > 0 && m.Width <= 2) || (m.Height > 0 && m.Height <= 2)) {
			return // tracking pixel
		}
		seen[key] = true
		out = append(out, m)
	}
	meta := map[string]string{}
	walkNodes(doc, func(n *html.Node) bool {
		switch n.DataAtom {
		case atom.Meta:
			key := strings.ToLower(firstNonEmptyStr(attr(n, "property"), attr(n, "name")))
			val := resolve(base, strings.TrimSpace(attr(n, "content")))
			switch key {
			case "og:image", "og:image:url", "og:image:secure_url", "twitter:image", "twitter:image:src":
				add(MediaItem{Kind: "image", URL: val, Found: key})
			case "og:video", "og:video:url", "og:video:secure_url":
				k := kindOfURL(val)
				if k == "" {
					k = "video"
				}
				add(MediaItem{Kind: k, URL: val, Found: key, Type: meta["og:video:type"]})
			case "og:audio", "og:audio:url", "og:audio:secure_url":
				add(MediaItem{Kind: "audio", URL: val, Found: key})
			case "og:video:type":
				meta[key] = val
			}
		case atom.Link:
			if strings.Contains(strings.ToLower(attr(n, "rel")), "image_src") {
				add(MediaItem{Kind: "image", URL: resolve(base, attr(n, "href")), Found: "link"})
			}
		case atom.Img:
			src := firstNonEmptyStr(attr(n, "src"), attr(n, "data-src"), attr(n, "data-lazy-src"), attr(n, "data-original"), attr(n, "data-lazy"))
			w, h := intAttr(n, "width"), intAttr(n, "height")
			found := "img"
			if ss := firstNonEmptyStr(attr(n, "srcset"), attr(n, "data-srcset")); ss != "" {
				if u, sw := bestFromSrcset(ss, base); u != "" {
					src, found = u, "srcset"
					if sw > 0 {
						w = sw
					}
				}
			}
			add(MediaItem{Kind: "image", URL: resolve(base, src), Alt: strings.TrimSpace(attr(n, "alt")), Title: attr(n, "title"), Width: w, Height: h, Caption: nearestCaption(n), Found: found})
		case atom.Source:
			parent := ""
			if n.Parent != nil {
				parent = n.Parent.Data
			}
			typ := attr(n, "type")
			if ss := attr(n, "srcset"); ss != "" && parent == "picture" {
				if u, w := bestFromSrcset(ss, base); u != "" {
					add(MediaItem{Kind: "image", URL: u, Width: w, Type: typ, Found: "srcset"})
				}
			}
			if src := attr(n, "src"); src != "" && (parent == "video" || parent == "audio") {
				k := parent
				if kk := kindOfURL(src); kk == "stream" {
					k = "stream"
				}
				add(MediaItem{Kind: k, URL: resolve(base, src), Type: typ, Found: "source"})
			}
		case atom.Video, atom.Audio:
			k := n.Data
			if src := attr(n, "src"); src != "" {
				if kk := kindOfURL(src); kk == "stream" {
					k = "stream"
				}
				add(MediaItem{Kind: k, URL: resolve(base, src), Poster: resolve(base, attr(n, "poster")), Title: attr(n, "title"), Found: k})
			}
			if p := attr(n, "poster"); p != "" {
				add(MediaItem{Kind: "image", URL: resolve(base, p), Found: "poster"})
			}
		case atom.Iframe:
			src := resolve(base, firstNonEmptyStr(attr(n, "src"), attr(n, "data-src")))
			if isEmbedHost(src) {
				add(MediaItem{Kind: "embed", URL: src, Title: attr(n, "title"), Width: intAttr(n, "width"), Height: intAttr(n, "height"), Found: "iframe"})
			}
		case atom.A:
			href := resolve(base, attr(n, "href"))
			if k := kindOfURL(href); k != "" {
				add(MediaItem{Kind: k, URL: href, Title: firstNonEmptyStr(textOf(n), attr(n, "title")), Found: "link"})
			} else if isEmbedHost(href) && (strings.Contains(href, "watch") || strings.Contains(href, "youtu.be") || strings.Contains(href, "/video")) {
				add(MediaItem{Kind: "embed", URL: href, Title: textOf(n), Found: "link"})
			}
		case atom.Script:
			body := ""
			if n.FirstChild != nil {
				body = n.FirstChild.Data
			}
			if len(body) > 20 && len(body) < 6<<20 {
				for _, u := range scriptURL.FindAllString(body, 60) {
					u = strings.ReplaceAll(u, `\/`, "/")
					if k := kindOfURL(u); k != "" {
						add(MediaItem{Kind: k, URL: u, Found: "script"})
					}
				}
			}
		}
		return true
	})
	// JSON-LD media objects
	if st, err := ExtractStructured(page); err == nil {
		for _, e := range st.JSONLD {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["@type"].(string)
			str := func(k string) string { v, _ := m[k].(string); return resolve(base, v) }
			switch typ {
			case "VideoObject":
				if u := str("contentUrl"); u != "" {
					k := kindOfURL(u)
					if k == "" {
						k = "video"
					}
					add(MediaItem{Kind: k, URL: u, Title: str("name"), Poster: str("thumbnailUrl"), Found: "json-ld"})
				}
				if u := str("embedUrl"); u != "" {
					add(MediaItem{Kind: "embed", URL: u, Title: str("name"), Found: "json-ld"})
				}
			case "AudioObject":
				add(MediaItem{Kind: "audio", URL: str("contentUrl"), Title: str("name"), Found: "json-ld"})
			case "ImageObject":
				add(MediaItem{Kind: "image", URL: firstNonEmptyStr(str("contentUrl"), str("url")), Title: str("name"), Found: "json-ld"})
			}
			switch img := m["image"].(type) {
			case string:
				add(MediaItem{Kind: "image", URL: resolve(base, img), Found: "json-ld"})
			case []any:
				for _, x := range img {
					if s, ok := x.(string); ok {
						add(MediaItem{Kind: "image", URL: resolve(base, s), Found: "json-ld"})
					} else if mm, ok := x.(map[string]any); ok {
						if s, ok := mm["url"].(string); ok {
							add(MediaItem{Kind: "image", URL: resolve(base, s), Found: "json-ld"})
						}
					}
				}
			case map[string]any:
				if s, ok := img["url"].(string); ok {
					add(MediaItem{Kind: "image", URL: resolve(base, s), Found: "json-ld"})
				}
			}
		}
	}
	return out, nil
}

// FilterMedia keeps the requested kinds ("images", "video", "audio", "streams", "embeds", "files"; empty = all).
func FilterMedia(items []MediaItem, kinds []string) []MediaItem {
	if len(kinds) == 0 {
		return items
	}
	want := map[string]bool{}
	for _, k := range kinds {
		k = strings.ToLower(strings.TrimSpace(k))
		k = strings.TrimSuffix(k, "s")
		if k == "all" {
			return items
		}
		want[k] = true
	}
	var out []MediaItem
	for _, m := range items {
		if want[m.Kind] {
			out = append(out, m)
		}
	}
	return out
}
