package consult

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"prism/internal/settings"
)

// Site is a chat website as the web provider sees it.
type Site struct {
	Name string `json:"name"`
	settings.ConsultSite
	Builtin  bool `json:"builtin"`
	Verified bool `json:"verified"` // selectors confirmed against the live site
}

// builtinSites are starting points; the user can override any field (Settings → Consult). Only chatgpt's
// selectors have been confirmed against the live site — the others rely on PRISM's generic composer and
// answer detection plus a hint or two, and each Settings row has a Test button to find out if it works.
func builtinSites() map[string]Site {
	mk := func(name, label, url string, c settings.ConsultSite, verified bool) Site {
		c.Label, c.URL = label, url
		return Site{Name: name, ConsultSite: c, Builtin: true, Verified: verified}
	}
	sites := []Site{
		mk("chatgpt", "ChatGPT", "https://chatgpt.com/", settings.ConsultSite{
			Input: "#prompt-textarea", Send: `[data-testid="send-button"]`, Stop: `[data-testid="stop-button"]`,
			Answer: `[data-message-author-role="assistant"]`, Login: `[data-testid="login-button"]`}, true),
		mk("deepseek", "DeepSeek", "https://chat.deepseek.com/", settings.ConsultSite{Input: "textarea", Answer: ".ds-markdown"}, false),
		mk("kimi", "Kimi", "https://www.kimi.com/", settings.ConsultSite{Answer: ".markdown"}, false),
		mk("grok", "Grok", "https://grok.com/", settings.ConsultSite{Input: "textarea"}, false),
		mk("gemini", "Gemini", "https://gemini.google.com/app", settings.ConsultSite{
			Input: `.ql-editor, rich-textarea [contenteditable="true"]`, Answer: "message-content", Stop: `button[aria-label*="Stop"]`}, false),
		mk("claude", "Claude", "https://claude.ai/new", settings.ConsultSite{Input: `[contenteditable="true"]`, Stop: `button[aria-label*="Stop"]`}, false),
	}
	out := map[string]Site{}
	for _, s := range sites {
		out[s.Name] = s
	}
	return out
}

var siteName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)

// Sites returns the effective chat sites: built-ins with the user's overrides applied, plus custom ones.
func Sites(cfg settings.Consult) []Site {
	m := builtinSites()
	for name, o := range cfg.Sites {
		name = strings.ToLower(strings.TrimSpace(name))
		if !siteName.MatchString(name) || name == "codex" {
			continue
		}
		s, ok := m[name]
		if !ok {
			s = Site{Name: name}
		}
		set := func(dst *string, v string) {
			if strings.TrimSpace(v) != "" {
				*dst = strings.TrimSpace(v)
			}
		}
		set(&s.Label, o.Label)
		set(&s.URL, o.URL)
		set(&s.Input, o.Input)
		set(&s.Send, o.Send)
		set(&s.Stop, o.Stop)
		set(&s.Answer, o.Answer)
		set(&s.Login, o.Login)
		s.Disabled = o.Disabled
		if o.Input != "" || o.Send != "" || o.Stop != "" || o.Answer != "" || o.Login != "" {
			s.Verified = false // customised: whatever was confirmed no longer applies
		}
		if s.Label == "" {
			s.Label = name
		}
		m[name] = s
	}
	out := make([]Site, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func findSite(cfg settings.Consult, name string) (Site, error) {
	var names []string
	for _, s := range Sites(cfg) {
		if s.Name == name {
			if s.Disabled {
				return s, fmt.Errorf("site %q is disabled in Settings", name)
			}
			if !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
				return s, fmt.Errorf("site %q has no http(s) URL", name)
			}
			return s, nil
		}
		if !s.Disabled {
			names = append(names, s.Name)
		}
	}
	return Site{}, fmt.Errorf("unknown provider %q (available: codex, %s)", name, strings.Join(names, ", "))
}
