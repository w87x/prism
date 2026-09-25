package web

import (
	"strings"
	"testing"
)

var sampleProduct = `<!DOCTYPE html><html lang="en"><head>
<title>Acme Widget — Shop</title>
<meta name="description" content="The best widget.">
<meta property="og:title" content="Acme Widget"><meta property="og:image" content="/img/widget-og.jpg">
<meta property="og:video" content="https://cdn.example.com/promo.mp4"><meta property="og:video:type" content="video/mp4">
<meta property="article:published_time" content="2026-03-01T10:00:00Z"><meta name="author" content="Jane Doe">
<link rel="canonical" href="https://shop.example.com/widget">
<link rel="alternate" type="application/rss+xml" title="Updates" href="/feed.xml">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[
 {"@type":"Product","name":"Acme Widget","sku":"W-1","image":["/img/a.jpg","/img/b.jpg"],"offers":{"@type":"Offer","price":"19.90","priceCurrency":"EUR"}},
 {"@type":"VideoObject","name":"Promo","contentUrl":"https://cdn.example.com/promo.mp4","embedUrl":"https://www.youtube.com/embed/abc","thumbnailUrl":"/img/promo.jpg"}]}</script>
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"stock":12,"variants":[{"id":1,"color":"red"}]}}}</script>
<script>window.__INITIAL_STATE__ = {"cart":{"items":3}}; console.log("x");</script>
<script>var player = {src: "https:\/\/stream.example.com\/live\/master.m3u8?token=1"};</script>
</head><body>
<main><h1>Acme Widget</h1>
<p>` + strings.Repeat("A long description paragraph about the widget that goes on and on to make the page look like real content. ", 6) + `</p>
<div itemscope itemtype="https://schema.org/Product"><span itemprop="name">Micro Widget</span><span itemprop="price" content="5.50">5,50 €</span><a itemprop="url" href="/micro">link</a></div>
<figure><img src="/img/hero.jpg" srcset="/img/hero-400.jpg 400w, /img/hero-1200.jpg 1200w" width="600" height="400" alt="Hero shot"><figcaption>The widget in use</figcaption></figure>
<img src="/img/pixel.gif" width="1" height="1"><img data-src="/img/lazy.png" alt="Lazy" width="300">
<picture><source srcset="/img/pic.webp 800w, /img/pic-small.webp 300w" type="image/webp"><img src="/img/pic.jpg" alt="Pic"></picture>
<video poster="/img/vposter.jpg"><source src="/media/clip.webm" type="video/webm"></video>
<audio src="/media/song.mp3"></audio>
<iframe src="https://www.youtube.com/embed/xyz" title="Demo"></iframe><iframe src="https://ads.example.net/frame"></iframe>
<a href="/files/manual.pdf">Manual (PDF)</a><a href="/page">Not media</a>
<table><caption>Specs</caption><thead><tr><th>Feature</th><th>Value</th></tr></thead><tbody><tr><td>Weight</td><td>2 kg</td></tr><tr><td>Color</td><td>Red</td></tr></tbody></table>
<table><tr><td>layout only</td></tr></table>
</main></body></html>`

func TestStructuredDataIsReadWithoutAModel(t *testing.T) {
	st, err := ExtractStructured(&Page{Body: sampleProduct, FinalURL: "https://shop.example.com/widget", ContentType: "text/html"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Title != "Acme Widget — Shop" || st.Description != "The best widget." || st.Canonical != "https://shop.example.com/widget" || st.Language != "en" {
		t.Fatalf("basics: %+v", st)
	}
	if st.Meta["og:title"] != "Acme Widget" || st.Meta["author"] != "Jane Doe" {
		t.Fatalf("meta: %v", st.Meta)
	}
	if len(st.JSONLD) != 2 {
		t.Fatalf("json-ld entities (graph flattened): %d %+v", len(st.JSONLD), st.JSONLD)
	}
	if p, _ := st.JSONLD[0].(map[string]any); p["sku"] != "W-1" {
		t.Fatalf("product: %+v", st.JSONLD[0])
	}
	names := map[string]bool{}
	for _, e := range st.Embedded {
		names[e.Name] = true
	}
	if !names["__NEXT_DATA__"] || !names["__INITIAL_STATE__"] {
		t.Fatalf("embedded json: %+v", st.Embedded)
	}
	if len(st.Tables) != 1 || st.Tables[0].Caption != "Specs" || st.Tables[0].Headers[0] != "Feature" || st.Tables[0].Rows[1][1] != "Red" {
		t.Fatalf("tables (the layout table must be ignored): %+v", st.Tables)
	}
	if len(st.Feeds) != 1 || st.Feeds[0].URL != "https://shop.example.com/feed.xml" {
		t.Fatalf("feeds: %+v", st.Feeds)
	}
	if len(st.Microdata) != 1 || st.Microdata[0]["name"] != "Micro Widget" || st.Microdata[0]["price"] != "5.50" || st.Microdata[0]["url"] != "https://shop.example.com/micro" || st.Microdata[0]["@type"] != "Product" {
		t.Fatalf("microdata: %+v", st.Microdata)
	}
	if st.Article == nil || st.Article.Author != "Jane Doe" || st.Article.Published != "2026-03-01T10:00:00Z" {
		t.Fatalf("article: %+v", st.Article)
	}
}

func TestMediaIsListedWithSizesAndSources(t *testing.T) {
	items, err := ExtractMedia(&Page{Body: sampleProduct, FinalURL: "https://shop.example.com/widget"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	has := func(kind, u string) *MediaItem {
		for i := range items {
			if items[i].Kind == kind && items[i].URL == u {
				return &items[i]
			}
		}
		return nil
	}
	for _, c := range []struct{ kind, url string }{
		{"image", "https://shop.example.com/img/widget-og.jpg"}, {"video", "https://cdn.example.com/promo.mp4"},
		{"image", "https://shop.example.com/img/hero-1200.jpg"}, {"image", "https://shop.example.com/img/lazy.png"}, {"image", "https://shop.example.com/img/pic.webp"},
		{"video", "https://shop.example.com/media/clip.webm"}, {"audio", "https://shop.example.com/media/song.mp3"}, {"image", "https://shop.example.com/img/vposter.jpg"},
		{"embed", "https://www.youtube.com/embed/xyz"}, {"embed", "https://www.youtube.com/embed/abc"}, {"file", "https://shop.example.com/files/manual.pdf"},
		{"stream", "https://stream.example.com/live/master.m3u8?token=1"}, {"image", "https://shop.example.com/img/a.jpg"},
	} {
		if has(c.kind, c.url) == nil {
			t.Errorf("missing %s %s in %d items", c.kind, c.url, len(items))
		}
	}
	if h := has("image", "https://shop.example.com/img/hero-1200.jpg"); h == nil || h.Alt != "Hero shot" || h.Caption != "The widget in use" || h.Width != 1200 {
		t.Errorf("hero image details: %+v", h)
	}
	if has("image", "https://shop.example.com/img/pixel.gif") != nil {
		t.Error("a 1x1 tracking pixel must be dropped")
	}
	if has("embed", "https://ads.example.net/frame") != nil {
		t.Error("an unrelated iframe is not media")
	}
	for _, m := range items {
		if strings.HasSuffix(m.URL, "/page") {
			t.Errorf("a plain link is not media: %+v", m)
		}
	}
	big, _ := ExtractMedia(&Page{Body: sampleProduct, FinalURL: "https://shop.example.com/widget"}, 500)
	for _, m := range big {
		if m.Kind == "image" && m.Width > 0 && m.Width < 500 {
			t.Errorf("min width not applied: %+v", m)
		}
	}
	if only := FilterMedia(items, []string{"videos", "audio"}); len(only) == 0 || only[0].Kind == "image" {
		t.Errorf("filter: %+v", only)
	}
}
