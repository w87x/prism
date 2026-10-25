// Package redact finds credentials in text: API keys, tokens, passwords, private keys. It is used wherever text leaves PRISM's own store —
// a prompt sent to a cloud model, a message to Telegram, a query to a search engine — and wherever a credential must never be written down:
// memory. It looks for what credentials look like (known key formats, "password: …", bearer tokens), not for every long string: a torrent's
// info-hash or a commit id is not a secret and must pass untouched.
package redact

import "regexp"

type rule struct {
	re    *regexp.Regexp
	label string
	group int // 0: the whole match is replaced; n: only that capture group (the value of an assignment)
}

var rules = []rule{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`), "a private key", 0},
	{regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|sk_[A-Za-z0-9]{20,}|tvly-[A-Za-z0-9_-]{12,}|ghp_[A-Za-z0-9]{20,}|gho_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{12,}|AIza[0-9A-Za-z_-]{20,})\b`), "an API key", 0},
	{regexp.MustCompile(`\b\d{8,10}:AA[A-Za-z0-9_-]{30,}\b`), "a bot token", 0},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), "a JWT", 0},
	{regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/=-]{20,})`), "a bearer token", 1},
	{regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|passphrase|secret|api[_ -]?key|apikey|access[_ -]?token|auth[_ -]?token)\b["']?\s*[:=]\s*["']?([^\s"',;)]{6,})`), "a credential", 1},
	{regexp.MustCompile(`(?i)\btoken\b["']?\s*[:=]\s*["']?([A-Za-z0-9._~+/=-]{16,})`), "a token", 1},
}

// Secrets returns text with every credential replaced by a marker, and the kinds found.
func Secrets(s string) (string, []string) {
	var found []string
	seen := map[string]bool{}
	for _, r := range rules {
		s = r.re.ReplaceAllStringFunc(s, func(m string) string {
			if !seen[r.label] {
				seen[r.label] = true
				found = append(found, r.label)
			}
			if r.group == 0 {
				return "[removed: " + r.label + "]"
			}
			sub := r.re.FindStringSubmatchIndex(m)
			if len(sub) < 2*r.group+2 || sub[2*r.group] < 0 {
				return "[removed: " + r.label + "]"
			}
			return m[:sub[2*r.group]] + "[removed: " + r.label + "]" + m[sub[2*r.group+1]:]
		})
	}
	return s, found
}

// Has reports whether text holds a credential, and which kinds.
func Has(s string) ([]string, bool) {
	_, found := Secrets(s)
	return found, len(found) > 0
}
