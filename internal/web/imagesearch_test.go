package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/settings"
)

func TestImageSearchDuckDuckGoAndTavily(t *testing.T) {
	ddg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			if r.URL.Query().Get("iax") != "images" {
				t.Errorf("the search page must be asked for images: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`<html><script>var x = {vqd="4-987654321"};</script></html>`))
		case "/i.js":
			if r.URL.Query().Get("vqd") != "4-987654321" || r.URL.Query().Get("q") != "red panda" {
				t.Errorf("token or query not passed: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"results":[{"image":"https://img.example/panda.jpg","title":"A red panda","url":"https://example.org/panda","width":1200,"height":800},{"image":"","title":"broken"},{"image":"https://img.example/p2.png","title":"Another","url":"https://example.org/p2","width":640,"height":480}]}`))
		}
	}))
	defer ddg.Close()
	oldP, oldJ := ddgImagePageURL, ddgImageJSONURL
	ddgImagePageURL, ddgImageJSONURL = ddg.URL+"/", ddg.URL+"/i.js"
	defer func() { ddgImagePageURL, ddgImageJSONURL = oldP, oldJ }()

	res, used, err := SearchImages(context.Background(), settings.Web{}, "red panda", 5)
	if err != nil || used != "DuckDuckGo" || len(res) != 2 || res[0].Image != "https://img.example/panda.jpg" || res[0].W != 1200 || res[0].Page != "https://example.org/panda" {
		t.Fatalf("ddg: %+v %q %v", res, used, err)
	}

	tav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer k") {
			t.Errorf("missing key")
		}
		_, _ = w.Write([]byte(`{"images":["https://t.example/a.jpg",{"url":"https://t.example/b.jpg","description":"B"}]}`))
	}))
	defer tav.Close()
	old := tavilySearchURL
	tavilySearchURL = tav.URL
	defer func() { tavilySearchURL = old }()
	res, used, err = SearchImages(context.Background(), settings.Web{TavilyKey: "k"}, "x", 5)
	if err != nil || used != "Tavily" || len(res) != 2 || res[1].Title != "B" {
		t.Fatalf("tavily: %+v %q %v", res, used, err)
	}
	if _, _, err := SearchImages(context.Background(), settings.Web{}, "  ", 5); err == nil {
		t.Fatal("an empty query must be refused")
	}
}
