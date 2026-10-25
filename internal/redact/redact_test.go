package redact

import (
	"strings"
	"testing"
)

func TestSecretsAreFoundAndHashesAreNot(t *testing.T) {
	for in, kind := range map[string]string{
		"my key is sk-proj-abcdefghijklmnop1234":                                                       "an API key",
		"tavily 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":                                             "an API key",
		"bot 8275647559:AAGDbGi5cU_wJ0OruQsimqLk5lE8DBEkdlw here":                                      "a bot token",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789":                                   "a bearer token",
		"password: hunter2hunter2":                                                                     "a credential",
		`{"api_key": "abcd1234efgh5678"}`:                                                              "a credential",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----":                       "a private key",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U": "a JWT",
	} {
		out, found := Secrets(in)
		if len(found) == 0 || found[0] != kind || !strings.Contains(out, "[removed:") {
			t.Errorf("%q: found %v out %q", in, found, out)
		}
	}
	for _, in := range []string{
		"magnet:?xt=urn:btih:8f5e7a3c2b1d9e4f6a0c8d7e6f5a4b3c2d1e0f9a&dn=Show.S01",
		"commit 3f38afe7b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6",
		"set the password policy for the NAS to twelve characters",
		"token bucket rate limiting algorithm",
	} {
		if out, found := Secrets(in); len(found) != 0 || out != in {
			t.Errorf("must pass untouched: %q -> %q %v", in, out, found)
		}
	}
	// only the value of an assignment goes, not the word
	if out, _ := Secrets("password: hunter2hunter2 for the router"); !strings.HasPrefix(out, "password: [removed: a credential] for") {
		t.Errorf("keep the label, drop the value: %q", out)
	}
}
