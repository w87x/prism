package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// Bug: index.html was served without Cache-Control, so iOS kept a stale copy that pointed at old hashed JS.
func TestServeUICacheHeaders(t *testing.T) {
	s := &Server{UI: fstest.MapFS{
		"index.html":           {Data: []byte("<html></html>")},
		"assets/app.js":        {Data: []byte("x")},
		"manifest.webmanifest": {Data: []byte("{}")},
	}}
	for path, want := range map[string]string{
		"/":                     "no-cache",
		"/manifest.webmanifest": "no-cache",
		"/assets/app.js":        "public, max-age=31536000, immutable",
		"/some/spa/route":       "no-cache",
	} {
		w := httptest.NewRecorder()
		s.serveUI(w, httptest.NewRequest(http.MethodGet, path, nil))
		if got := w.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control = %q, want %q", path, got, want)
		}
	}
}
