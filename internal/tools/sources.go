package tools

import (
	"net/url"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/net/publicsuffix"
)

// Sources remembers which web hosts showed up in the untrusted output of a run. A fact an agent learned from the
// web may only name a source that really appeared in its turn, so corroboration cannot be forged by inventing URLs.
type Sources struct {
	mu    sync.Mutex
	hosts map[string]bool
}

var urlRe = regexp.MustCompile(`https?://[^\s"'<>)\]\\]+`)

// Note records the hosts of every URL mentioned in the texts (tool arguments and results).
func (s *Sources) Note(texts ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hosts == nil {
		s.hosts = map[string]bool{}
	}
	for _, t := range texts {
		for _, m := range urlRe.FindAllString(t, 400) {
			if o, ok := Origin(m); ok && len(s.hosts) < 2000 {
				s.hosts[o] = true
			}
		}
	}
}

// Has reports whether the URL's site appeared in this run.
func (s *Sources) Has(rawURL string) bool {
	if s == nil {
		return false
	}
	o, ok := Origin(rawURL)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hosts[o]
}

// Origin reduces a URL to the site it belongs to (registrable domain: news.bbc.co.uk → bbc.co.uk), so two pages of
// one site count as one source. IP addresses and unusual hosts are kept as they are.
func Origin(rawURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if h == "" || !strings.Contains(h, ".") {
		return "", false
	}
	if d, err := publicsuffix.EffectiveTLDPlusOne(h); err == nil {
		return d, true
	}
	return h, true
}
