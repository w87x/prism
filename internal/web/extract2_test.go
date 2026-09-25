package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/settings"
	"prism/internal/testutil"
	"prism/internal/tools"
)

func TestParseNumberHandlesLocalFormats(t *testing.T) {
	for in, want := range map[string]float64{"1 299,90 €": 1299.9, "$1,299.90": 1299.9, "12,50": 12.5, "1.299,00": 1299, "1,299": 1299, "19.90 EUR": 19.9, "-5": -5} {
		if got, ok := parseNumber(in); !ok || got != want {
			t.Errorf("parseNumber(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	if _, ok := parseNumber("free"); ok {
		t.Error("no digits, no number")
	}
}

const listing1 = `<html><head><title>Shop</title>
<script type="application/ld+json">[{"@type":"Product","name":"Widget A","url":"/a","offers":{"price":"1 299,90 €"}},{"@type":"Product","name":"Widget B","url":"/b","offers":{"price":"20"}}]</script>
</head><body><main><ul><li>Widget A</li><li>Widget B</li></ul><a rel="next" href="/page2">Next</a></main></body></html>`

const listing2 = `<html><head><title>Shop 2</title></head><body><main><ul><li>Widget C — 30</li></ul></main></body></html>`

func newExtractor(t *testing.T) (*Extractor, *testutil.FakeLLM) {
	t.Helper()
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	return &Extractor{LLM: r}, fake
}

// Structured data answers the request when it is complete (no page-text call), values are coerced to the schema,
// an incomplete structured answer falls back to the page text, and an identical request is served from the cache.
func TestExtractUsesStructuredDataFirstAndCoercesToTheSchema(t *testing.T) {
	x, fake := newExtractor(t)
	structuredCalls, pageCalls := 0, 0
	complete := true
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		all := fmt.Sprint(req["messages"])
		if strings.Contains(all, "structured data that was found") {
			structuredCalls++
			return testutil.Reply{Content: fmt.Sprintf(`{"data":[{"name":"Widget A","price":"1 299,90 €","url":"/a"},{"name":"Widget B","price":"20","url":"/b"}],"complete":%v}`, complete)}
		}
		pageCalls++
		return testutil.Reply{Content: `{"data":[{"name":"Widget A","price":"1299.90","url":"https://shop.example/a"}]}`}
	}
	page := &Page{Body: listing1, FinalURL: "https://shop.example/list", ContentType: "text/html"}
	o := ExtractOpts{Instruction: "all products with name, price and link", Schema: `{"name":"string","price":"number","url":"url"}`}
	res, err := x.Run(context.Background(), page, o)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := res.Data.([]any)
	if res.Source != "structured" || len(items) != 2 || pageCalls != 0 || structuredCalls != 1 {
		t.Fatalf("result = %+v structured=%d page=%d", res, structuredCalls, pageCalls)
	}
	first := items[0].(map[string]any)
	if first["price"] != 1299.9 || first["url"] != "https://shop.example/a" {
		t.Fatalf("coercion: %+v", first)
	}
	// cached: no model call at all
	structuredCalls = 0
	again, _ := x.Run(context.Background(), page, o)
	if structuredCalls != 0 || !strings.Contains(strings.Join(again.Notes, " "), "cached") {
		t.Fatalf("cache: calls=%d notes=%v", structuredCalls, again.Notes)
	}
	// incomplete structured data -> the page text is read
	complete = false
	o2 := o
	o2.Instruction = "the same products again, phrased differently"
	res2, err := x.Run(context.Background(), page, o2)
	if err != nil || res2.Source != "page" || pageCalls == 0 {
		t.Fatalf("fallback: %+v page=%d err=%v", res2, pageCalls, err)
	}
	// source=structured never reads the page
	pageCalls = 0
	o3 := o
	o3.Instruction, o3.Source = "and once more", "structured"
	if r3, _ := x.Run(context.Background(), page, o3); r3.Source != "structured" || pageCalls != 0 {
		t.Fatalf("structured only: %+v page=%d", r3, pageCalls)
	}
}

func TestExtractFollowsNextPagesAndMerges(t *testing.T) {
	x, fake := newExtractor(t)
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		all := fmt.Sprint(req["messages"])
		switch {
		case strings.Contains(all, "structured data that was found"):
			return testutil.Reply{Content: `{"data":[{"name":"Widget A"},{"name":"Widget B"}],"complete":true}`}
		case strings.Contains(all, "Widget C"):
			return testutil.Reply{Content: `{"data":[{"name":"Widget C"}]}`}
		}
		return testutil.Reply{Content: `{"data":[]}`}
	}
	fetched := 0
	fetch := func(_ context.Context, u string) (*Page, error) {
		fetched++
		if u != "https://shop.example/page2" {
			t.Fatalf("unexpected next url %q", u)
		}
		return &Page{Body: listing2, FinalURL: u, Status: 200, ContentType: "text/html"}, nil
	}
	res, err := x.Run(context.Background(), &Page{Body: listing1, FinalURL: "https://shop.example/list", ContentType: "text/html"},
		ExtractOpts{Instruction: "all widget names", MaxPages: 3, Fetch: fetch})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := res.Data.([]any)
	if len(items) != 3 || res.Pages != 2 || res.Source != "mixed" || fetched != 1 {
		t.Fatalf("paginated result = %+v fetched=%d", res, fetched)
	}
}

func TestNextPageDetection(t *testing.T) {
	if u := nextPageURL(&Page{Body: listing1, FinalURL: "https://shop.example/list"}); u != "https://shop.example/page2" {
		t.Fatalf("rel=next: %q", u)
	}
	if u := nextPageURL(&Page{Body: `<a href="/p/3">Next »</a>`, FinalURL: "https://x.example/p/2"}); u != "https://x.example/p/3" {
		t.Fatalf("text link: %q", u)
	}
	if u := nextPageURL(&Page{Body: `<a rel="next" href="https://evil.example/x">Next</a>`, FinalURL: "https://x.example/"}); u != "" {
		t.Fatalf("other hosts are never followed: %q", u)
	}
}

// The fetch method that worked for a site is remembered (and survives a restart), and can be forgotten.
func TestFetcherLearnsWhichMethodWorksPerSite(t *testing.T) {
	ctx := context.Background()
	st := settings.New(testutil.DB(t).Pool)
	f := &Fetcher{Settings: st}
	if f.learned(ctx, "shop.example") != "" {
		t.Fatal("nothing learned yet")
	}
	f.learn(ctx, "shop.example", "browser")
	if f.learned(ctx, "shop.example") != "browser" {
		t.Fatal("not remembered")
	}
	if again := (&Fetcher{Settings: st}); again.learned(ctx, "shop.example") != "browser" {
		t.Fatal("must be stored in settings so it survives a restart")
	}
	f.learn(ctx, "shop.example", "")
	if f.learned(ctx, "shop.example") != "" {
		t.Fatal("not forgotten")
	}
}

// The two new tools run end to end against a local page: no model, real fetch.
func TestStructuredAndMediaToolsReadALocalPage(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, sampleProduct)
	}))
	defer site.Close()
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	s := &Service{Settings: st, Fetcher: &Fetcher{Settings: st, AllowPrivate: true}}
	reg := tools.NewRegistry(d.Pool)
	s.RegisterTools(reg)
	call := func(name string, args map[string]any) string {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		b, _ := json.Marshal(args)
		out, err := tool.Run(context.Background(), &tools.Env{Agent: "t"}, b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	for _, n := range []string{"web_structured", "web_media"} {
		if tool, _ := reg.Get(n); !tool.Untrusted {
			t.Fatalf("%s reads the web: its output must taint the turn", n)
		}
	}
	out := call("web_structured", map[string]any{"url": site.URL, "render": "http"})
	for _, want := range []string{`"sku": "W-1"`, "__NEXT_DATA__", `"caption": "Specs"`, "feed.xml", `"author": "Jane Doe"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("web_structured missing %s in:\n%s", want, out)
		}
	}
	only := call("web_structured", map[string]any{"url": site.URL, "render": "http", "sections": []string{"tables"}})
	if strings.Contains(only, "json_ld") || !strings.Contains(only, "Weight") {
		t.Fatalf("sections filter: %s", only)
	}
	small := call("web_structured", map[string]any{"url": site.URL, "render": "http", "max_chars": 1500})
	if len(small) > 2600 || !strings.Contains(small, "omitted") && !strings.Contains(small, "truncated") {
		t.Fatalf("the budget must be respected (%d chars): %s", len(small), small)
	}
	media := call("web_media", map[string]any{"url": site.URL, "render": "http", "kinds": []string{"video", "streams"}})
	if !strings.Contains(media, "promo.mp4") || !strings.Contains(media, "master.m3u8") || strings.Contains(media, "hero-1200.jpg") {
		t.Fatalf("web_media kinds filter: %s", media)
	}
	if _, err := func() (string, error) {
		tool, _ := reg.Get("web_media")
		b, _ := json.Marshal(map[string]any{"url": site.URL, "capture": true})
		return tool.Run(context.Background(), &tools.Env{Agent: "t"}, b)
	}(); err == nil || !strings.Contains(err.Error(), "browser") {
		t.Fatalf("capture without a browser must say what is missing, got %v", err)
	}
}
