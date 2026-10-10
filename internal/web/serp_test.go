package web

import (
	"context"
	"os"
	"strings"
	"testing"

	"prism/internal/settings"
)

// the fixtures are trimmed copies of real result pages (Bing's results and image tiles, October 2026)
func TestParseBingResultsDecodesTrackingLinks(t *testing.T) {
	b, _ := os.ReadFile("testdata/bing.html")
	res, err := parseBing(string(b), 10)
	if err != nil || len(res) < 2 {
		t.Fatalf("parseBing: %+v %v", res, err)
	}
	if res[0].URL != "https://en.wikipedia.org/wiki/Red_panda" || !strings.Contains(res[0].Title, "Red panda") || !strings.Contains(res[0].Snippet, "lesser panda") {
		t.Fatalf("first result: %+v", res[0])
	}
	for _, r := range res {
		if strings.Contains(r.URL, "bing.com/ck/") {
			t.Fatalf("a tracking link must be decoded: %+v", r)
		}
	}
}

func TestParseBingImages(t *testing.T) {
	b, _ := os.ReadFile("testdata/bingimg.html")
	res, err := parseBingImages(string(b), 10)
	if err != nil || len(res) != 1 || !strings.HasPrefix(res[0].Image, "https://i.ytimg.com/") || !strings.HasPrefix(res[0].Page, "https://www.youtube.com/") || res[0].Title == "" {
		t.Fatalf("bing images: %+v %v", res, err)
	}
}

// Google prints the real address as a breadcrumb under the title; its link is an opaque /goto token
func TestParseGoogleRebuildsAddressesFromBreadcrumbs(t *testing.T) {
	page := `<html><body><div id="rso">
	<div class="srKDX"><a href="/goto?url=CAESYg"><h3>Red panda</h3><br><div><span>Wikipedia</span><span>https://en.wikipedia.org › wiki › Red_panda</span></div></a>
	  <div>The red panda (Ailurus fulgens), also known as the lesser panda, is a small mammal native to the eastern Himalayas.Read more</div></div>
	<div class="N54PNb"><a href="/goto?url=CAESfQ"><h3>Red Panda | Charming Forest Dweller in ARTIS</h3><br><div><span>ARTIS-Park</span><span>https://www.artis.nl › ... › What to explore in ARTIS Zoo</span></div></a>
	  <div>The red panda in a nutshell eats bamboo, grasses, roots and fruits and lives in South and Southeast Asia.</div></div>
	<div><a href="https://direct.example/page"><h3>Direct link</h3></a><div>A result whose link is already the real address and has a long enough snippet text to count.</div></div>
	</div></body></html>`
	res, err := parseGoogle(page, 10)
	if err != nil || len(res) != 3 {
		t.Fatalf("parseGoogle: %+v %v", res, err)
	}
	if res[0].URL != "https://en.wikipedia.org/wiki/Red_panda" || !strings.Contains(res[0].Snippet, "lesser panda") || strings.Contains(res[0].Snippet, "Read more") {
		t.Fatalf("complete breadcrumb: %+v", res[0])
	}
	if res[1].URL != "https://www.artis.nl/" || !strings.Contains(res[1].Snippet, "abbreviated") {
		t.Fatalf("an abbreviated breadcrumb gives the site and says so: %+v", res[1])
	}
	if res[2].URL != "https://direct.example/page" {
		t.Fatalf("a direct link is kept: %+v", res[2])
	}
}

func TestBrowserSearchProvidersNeedABrowserAsAppropriate(t *testing.T) {
	old := BrowserHTML
	BrowserHTML = nil
	defer func() { BrowserHTML = old }()
	if _, err := searchGoogle(context.Background(), settings.Web{}, "x", 5); err == nil || !strings.Contains(err.Error(), "browser") {
		t.Fatalf("google without a browser must say so: %v", err)
	}
	if _, err := searchBing(context.Background(), settings.Web{}, "red panda habitat", 5); err == nil || !strings.Contains(err.Error(), "browser") {
		t.Fatalf("bing web results need a browser (plain HTTP answers only the first word of a query): %v", err)
	}
	for _, p := range ProviderList(settings.Web{}) {
		if (p.ID == "google" || p.ID == "bing") && p.Available {
			t.Fatalf("%s must be unavailable without a browser", p.ID)
		}
	}
	BrowserHTML = func(ctx context.Context, u, w string) (string, error) { return "<html><body></body></html>", nil }
	found := false
	for _, p := range ProviderList(settings.Web{}) {
		found = found || (p.ID == "google" && p.Available)
	}
	if !found {
		t.Fatal("google must be available once a browser is wired")
	}
}
