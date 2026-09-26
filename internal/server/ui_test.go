package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// A missing asset is a 404, never index.html (which the browser rejects with a MIME-type error), and a binary
// built without the UI says so instead of serving nothing useful.
func TestServeUIMissingAssetsAre404(t *testing.T) {
	with := &Server{UI: fstest.MapFS{"index.html": {Data: []byte("<html>ui</html>")}}}
	get := func(s *Server, p string) int {
		w := httptest.NewRecorder()
		s.serveUI(w, httptest.NewRequest(http.MethodGet, p, nil))
		return w.Code
	}
	if c := get(with, "/assets/index-gone.css"); c != http.StatusNotFound {
		t.Fatalf("missing asset = %d", c)
	}
	if c := get(with, "/chat"); c != http.StatusOK {
		t.Fatalf("SPA route = %d", c)
	}
	if c := get(&Server{UI: fstest.MapFS{".gitkeep": {}}}, "/"); c != http.StatusServiceUnavailable {
		t.Fatalf("no UI built = %d", c)
	}
}
