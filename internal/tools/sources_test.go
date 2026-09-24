package tools

import "testing"

func TestOriginIsTheRegistrableDomain(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.example.com/a/b?q=1": "example.com",
		"http://news.bbc.co.uk/x":         "bbc.co.uk",
		"https://Sub.Domain.Example.ORG.": "example.org",
		"https://user.github.io/repo":     "user.github.io", // a public suffix: sites under it are different sites
	} {
		if got, ok := Origin(in); !ok || got != want {
			t.Errorf("Origin(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "ftp://example.com", "http://localhost/x", "not a url", "https://"} {
		if o, ok := Origin(bad); ok {
			t.Errorf("Origin(%q) accepted: %q", bad, o)
		}
	}
}

func TestSourcesRememberOnlyWhatWasSeen(t *testing.T) {
	var s *Sources
	s.Note("https://a.com/x") // a nil set is harmless
	if s.Has("https://a.com") {
		t.Fatal("nil set claims a site")
	}
	s = &Sources{}
	s.Note(`{"url":"https://www.alpha.com/page"}`, "results: [Beta](https://beta.org/r?x=1), see also http://gamma.net/z).")
	for _, u := range []string{"https://alpha.com/other", "https://news.beta.org/", "http://gamma.net"} {
		if !s.Has(u) {
			t.Errorf("should have seen %s", u)
		}
	}
	for _, u := range []string{"https://delta.io", "garbage", ""} {
		if s.Has(u) {
			t.Errorf("must not have seen %q", u)
		}
	}
}
