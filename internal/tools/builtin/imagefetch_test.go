package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"prism/internal/settings"
	"prism/internal/tools"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)

func TestRasterKindOnlyAcceptsSafeFormats(t *testing.T) {
	for name, c := range map[string]struct {
		b    []byte
		want string
	}{
		"png": {pngBytes, "image/png"}, "jpeg": {[]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}, "image/jpeg"}, "gif": {[]byte("GIF89a\x01\x00"), "image/gif"},
		"webp": {[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"}, "svg": {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), ""},
		"html": {[]byte("<html><body>hi</body></html>"), ""}, "empty": {nil, ""},
	} {
		if got, _ := rasterKind(c.b); got != c.want {
			t.Errorf("%s: rasterKind = %q, want %q", name, got, c.want)
		}
	}
}

// image_fetch saves a real picture as an artifact and returns its marker; it refuses other content, oversized files,
// private addresses, and (after web content was read) URLs that never appeared in it.
func TestImageFetchSavesPicturesSafely(t *testing.T) {
	reg, deps, _, _ := setup(t)
	ctx := context.Background()
	_ = deps.Settings.Set(ctx, settings.KeyWeb, settings.Web{AllowPrivate: true}) // the test server is on loopback
	mux := http.NewServeMux()
	mux.HandleFunc("/cat.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes)
	})
	mux.HandleFunc("/evil.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	})
	mux.HandleFunc("/big.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 9<<20)...))
	})
	site := httptest.NewServer(mux)
	defer site.Close()
	tool, _ := reg.Get("image_fetch")
	run := func(env *tools.Env, u string) (string, error) {
		b, _ := json.Marshal(map[string]string{"url": u})
		return tool.Run(ctx, env, b)
	}
	out, err := run(&tools.Env{Agent: "t"}, site.URL+"/cat.png")
	if err != nil || !strings.Contains(out, "[image:") {
		t.Fatalf("fetch: %q %v", out, err)
	}
	m := regexp.MustCompile(`\[image:(\d+)\]`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no marker in %q", out)
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	art, err := GetArtifact(ctx, deps.DB, id)
	if err != nil || art.Mime != "image/png" || !art.Tainted || art.ExpiresAt == nil {
		t.Fatalf("artifact = %+v err=%v", art, err)
	}
	if _, err := run(&tools.Env{Agent: "t"}, site.URL+"/evil.svg"); err == nil {
		t.Fatal("SVG must be refused: it can carry script")
	}
	if _, err := run(&tools.Env{Agent: "t"}, site.URL+"/big.png"); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("oversized image: %v", err)
	}
	// after web content was read, a URL that did not appear in it is refused
	src := &tools.Sources{}
	if _, err := run(&tools.Env{Agent: "t", Tainted: true, Sources: src}, site.URL+"/cat.png"); err == nil || !strings.Contains(err.Error(), "appeared") {
		t.Fatalf("unseen url while tainted: %v", err)
	}
	src.Note("typed: " + site.URL + "/cat.png?leak=secret")
	if _, err := run(&tools.Env{Agent: "t", Tainted: true, Sources: src}, site.URL+"/cat.png?leak=secret"); err == nil {
		t.Fatal("an address the agent composed must not count as seen")
	}
	src.NoteResult("see the picture at " + site.URL + "/cat.png")
	if _, err := run(&tools.Env{Agent: "t", Tainted: true, Sources: src}, site.URL+"/cat.png"); err != nil {
		t.Fatalf("a URL from the run's own sources is allowed: %v", err)
	}
	// the private-address guard applies unless the user allowed it
	_ = deps.Settings.Set(ctx, settings.KeyWeb, settings.Web{})
	if _, err := run(&tools.Env{Agent: "t"}, site.URL+"/cat.png"); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private address: %v", err)
	}
}
