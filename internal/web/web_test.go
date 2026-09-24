package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"prism/internal/settings"
	"prism/internal/testutil"
	"prism/internal/tools"
)

const page = `<!doctype html><html><head><title>Shop &amp; News</title><style>.x{}</style><script>alert(1)</script></head>
<body><nav>MENU HOME ABOUT</nav><main><h1>Latest</h1>
<div class="item"><a href="/a/1">First article</a><p>Desc one</p></div>
<div class="item"><a href="/a/2">Second article</a><p>Desc two</p></div>
<ul><li>alpha</li><li>beta</li></ul>
<table><tr><th>Item</th><th>Price</th></tr><tr><td>Milk</td><td>1.20</td></tr></table>
<pre>code  here</pre></main><footer>FOOTER JUNK</footer></body></html>`

func TestMarkdownAndCompact(t *testing.T) {
	doc, err := Parse(page)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://shop.example/news/")
	md := Markdown(doc, base, true)
	for _, want := range []string{"# Latest", "[First article](https://shop.example/a/1)", "- alpha", "| Milk | 1.20 |", "```\ncode  here\n```"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q in:\n%s", want, md)
		}
	}
	for _, bad := range []string{"MENU", "FOOTER", "alert(1)"} {
		if strings.Contains(md, bad) {
			t.Errorf("markdown should not contain %q", bad)
		}
	}
	if Title(doc) != "Shop & News" {
		t.Errorf("title = %q", Title(doc))
	}
	c := Compact(doc, base)
	if strings.Contains(c, "alert") || strings.Contains(c, "<style") || !strings.Contains(c, `href="https://shop.example/a/1"`) {
		t.Errorf("compact wrong: %s", c)
	}
	if len(c) > len(page) {
		t.Errorf("compact should be smaller than source")
	}
	j := DOMJSON(BuildDOM(doc, DOMOptions{Base: base}))
	var n DOMNode
	if err := json.Unmarshal([]byte(j), &n); err != nil || n.Tag != "html" {
		t.Fatalf("dom json: %v %s", err, j[:80])
	}
	chunks := Chunk(strings.Repeat("<p>hello world</p>", 500), 1000)
	if len(chunks) < 8 || strings.Join(chunks, "") == "" {
		t.Fatalf("chunking: %d", len(chunks))
	}
	for _, ch := range chunks {
		if len(ch) > 1000 {
			t.Fatalf("chunk too big: %d", len(ch))
		}
	}
}

func TestYandexXML(t *testing.T) {
	raw := `<yandexsearch><response><results><grouping><group><doc><url>https://a.example</url><title>A <hlword>hit</hlword></title><headline>head</headline></doc></group>
	<group><doc><url>https://b.example</url><title>B</title><passages><passage>pass <hlword>x</hlword></passage></passages></doc></group></grouping></results></response></yandexsearch>`
	res, err := parseYandexXML([]byte(raw), 5)
	if err != nil || len(res) != 2 || res[0].Title != "A hit" || res[1].Snippet != "pass x" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestExtractPipeline(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fake)
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		user := ms[len(ms)-1].(map[string]any)["content"].(string)
		if !strings.Contains(user, "https://shop.example/a/1") {
			return testutil.Reply{Content: `{"data": []}`}
		}
		return testutil.Reply{Content: `{"data":[{"title":"First article","link":"https://shop.example/a/1"},{"title":"Second article","link":"https://shop.example/a/2"}]}`}
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, strings.ReplaceAll(page, "/a/", "https://shop.example/a/"))
	}))
	defer site.Close()
	f := &Fetcher{Settings: st, AllowPrivate: true}
	p, err := f.Fetch(context.Background(), site.URL, "http", "", 0)
	if err != nil || p.Status != 200 {
		t.Fatalf("fetch: %v %+v", err, p)
	}
	res, err := (&Extractor{LLM: r}).Extract(context.Background(), p, "all articles with title and link", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), "Second article") {
		t.Fatalf("extract result: %s", b)
	}

	// SSRF guard blocks private targets by default
	f2 := &Fetcher{Settings: st}
	if _, err := f2.Fetch(context.Background(), site.URL, "http", "", 0); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("expected SSRF block, got %v", err)
	}
	_ = settings.KeyWeb
}

func TestNonUTF8PagesAreDecoded(t *testing.T) {
	// "Привет, мир" in windows-1251
	body := []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2, 0x2C, 0x20, 0xEC, 0xE8, 0xF0}
	if got := decodeBody(body, "text/html; charset=windows-1251"); got != "Привет, мир" {
		t.Fatalf("declared charset: %q", got)
	}
	meta := append([]byte(`<html><head><meta charset="windows-1251"></head><body>`), append(body, []byte(`</body></html>`)...)...)
	if got := decodeBody(meta, "text/html"); !strings.Contains(got, "Привет, мир") {
		t.Fatalf("meta charset: %q", got)
	}
	if got := decodeBody([]byte("plain ascii — ok"), "text/html; charset=utf-8"); got != "plain ascii — ok" {
		t.Fatalf("utf-8 must pass through: %q", got)
	}
}

func TestFetchRefusesLocalTargetsInEveryMode(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "secret") }))
	defer site.Close()
	f := &Fetcher{Settings: settings.New(testutil.DB(t).Pool)}
	for _, mode := range []string{"http", "auto", "browser"} { // browser mode used to skip the guard entirely
		if _, err := f.Fetch(context.Background(), site.URL, mode, "", 0); err == nil || !strings.Contains(err.Error(), "private") {
			t.Fatalf("mode %s must refuse a loopback target: %v", mode, err)
		}
	}
	if _, err := f.Fetch(context.Background(), "http://169.254.169.254/latest/meta-data/", "http", "", 0); err == nil {
		t.Fatal("cloud metadata address must be refused")
	}
}

// web_search must check bookmarks first: a saved link answering the query is worth surfacing even when the
// live search fails outright, and must never be lost silently when it succeeds either.
func TestWebSearchChecksBookmarksFirst(t *testing.T) {
	st := settings.New(testutil.DB(t).Pool)
	s := &Service{Settings: st}
	reg := tools.NewRegistry(testutil.DB(t).Pool)
	s.RegisterTools(reg)
	tool, ok := reg.Get("web_search")
	if !ok {
		t.Fatal("web_search not registered")
	}
	args, _ := json.Marshal(map[string]any{"query": "price comparison tool"})

	// a context that is already done makes every provider's HTTP call fail immediately — no real network,
	// deterministic "live search failed" without needing to mock provider endpoints.
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("bookmark hit survives a failed live search", func(t *testing.T) {
		s.Bookmarks = func(ctx context.Context, query string, limit int) []BookmarkHit {
			return []BookmarkHit{{Title: "PriceComparo", URL: "https://pricecomparo.example", Description: "compares prices across shops"}}
		}
		out, err := tool.Run(dead, &tools.Env{Agent: "t"}, args)
		if err != nil {
			t.Fatalf("a bookmark hit must be returned even when the live search fails: %v", err)
		}
		if !strings.Contains(out, "From your bookmarks") || !strings.Contains(out, "PriceComparo") || !strings.Contains(out, "pricecomparo.example") {
			t.Fatalf("expected the bookmark surfaced: %q", out)
		}
	})

	t.Run("no bookmark match: a failed live search is still a real error", func(t *testing.T) {
		s.Bookmarks = func(ctx context.Context, query string, limit int) []BookmarkHit { return nil }
		_, err := tool.Run(dead, &tools.Env{Agent: "t"}, args)
		if err == nil {
			t.Fatal("expected an error: nothing to fall back to and the live search failed")
		}
	})

	t.Run("Bookmarks unwired (nil): no panic, live search error still surfaces", func(t *testing.T) {
		s.Bookmarks = nil
		_, err := tool.Run(dead, &tools.Env{Agent: "t"}, args)
		if err == nil {
			t.Fatal("expected an error from the failed live search")
		}
	})
}

// isHostNotFound must trip only on a genuine "this host does not exist" DNS answer — a slow/timing-out
// resolver could still succeed a moment later and must not be treated the same as a dead host.
func TestIsHostNotFound(t *testing.T) {
	if !isHostNotFound(&net.DNSError{IsNotFound: true}) {
		t.Fatal("expected a DNS not-found error to be detected")
	}
	if isHostNotFound(&net.DNSError{IsTimeout: true}) {
		t.Fatal("a DNS timeout is not the same as \"no such host\" and must not trip the breaker")
	}
	if isHostNotFound(errors.New("connection refused")) {
		t.Fatal("a non-DNS error must not be treated as host-not-found")
	}
}

// A host recorded as down must expire once the cooldown passes — this is a short-lived circuit breaker, not
// a permanent block, so a host that comes back is tried again normally.
func TestHostDownExpiresAfterCooldown(t *testing.T) {
	f := &Fetcher{}
	f.markHostDown("stale.example")
	if _, down := f.hostDownSince("stale.example"); !down {
		t.Fatal("a freshly marked host should read as down")
	}
	f.downMu.Lock()
	f.down["stale.example"] = time.Now().Add(-hostDownCooldown - time.Minute)
	f.downMu.Unlock()
	if _, down := f.hostDownSince("stale.example"); down {
		t.Fatal("an entry past the cooldown must expire, not stay down forever")
	}
}

// Fetch must short-circuit a host already known down without attempting the network call again — repeatedly
// hammering a host that just failed DNS resolution (or its resolver) helps nobody. Using a pre-marked host
// and asserting on the circuit-breaker's own error message (rather than triggering a real DNS failure, which
// would make this test depend on the test environment's network/DNS policy) keeps this deterministic.
func TestFetchShortCircuitsAKnownDownHost(t *testing.T) {
	st := settings.New(testutil.DB(t).Pool)
	f := &Fetcher{Settings: st, AllowPrivate: true}
	f.markHostDown("127.0.0.1")
	_, err := f.Fetch(context.Background(), "http://127.0.0.1:1/page", "http", "", 0)
	if err == nil || !strings.Contains(err.Error(), "not retrying yet") {
		t.Fatalf("expected the circuit breaker to short-circuit with its own message, got: %v", err)
	}
}

// A DNS "no such host" failure from a real fetch must mark that host down so the next call short-circuits.
func TestFetchMarksHostDownOnDNSFailure(t *testing.T) {
	st := settings.New(testutil.DB(t).Pool)
	f := &Fetcher{Settings: st, AllowPrivate: true}
	const host = "this-host-should-never-resolve.invalid" // RFC 2606 reserved TLD: guaranteed never to resolve
	if _, down := f.hostDownSince(host); down {
		t.Fatal("sanity: host must not start marked down")
	}
	_, err := f.Fetch(context.Background(), "http://"+host+"/page", "http", "", 0)
	if err == nil {
		t.Fatal("expected a DNS resolution failure")
	}
	if !isHostNotFound(err) {
		t.Skipf("this environment's resolver did not return a plain \"no such host\" for a .invalid domain (got: %v) — cannot exercise the marking path without real DNS behavior", err)
	}
	if _, down := f.hostDownSince(host); !down {
		t.Fatal("a DNS not-found failure must mark the host down")
	}
}
