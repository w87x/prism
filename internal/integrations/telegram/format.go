package telegram

import (
	"html"
	"regexp"
	"strings"
)

var (
	fenceRe  = regexp.MustCompile("(?s)```[a-zA-Z0-9_+-]*\\n?(.*?)```")
	codeRe   = regexp.MustCompile("`([^`\\n]+)`")
	boldRe   = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	italRe   = regexp.MustCompile(`(^|[\s(])\*([^*\s][^*\n]*[^*\s]|[^*\s])\*($|[\s).,!?:;])`)
	linkRe   = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	headRe   = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	bulletRe = regexp.MustCompile(`(?m)^(\s*)[-*]\s+`)
)

// ToHTML converts a Markdown subset (code, bold, italic, links, headings, bullets) to
// Telegram's HTML dialect, escaping everything else.
func ToHTML(md string) string {
	// stash code so its content is not re-formatted
	var stash []string
	put := func(s string) string {
		stash = append(stash, s)
		return "\x00" + string(rune('A'+len(stash)-1)) + "\x00"
	}
	md = fenceRe.ReplaceAllStringFunc(md, func(m string) string {
		return put("<pre>" + html.EscapeString(strings.TrimSpace(fenceRe.FindStringSubmatch(m)[1])) + "</pre>")
	})
	md = codeRe.ReplaceAllStringFunc(md, func(m string) string {
		return put("<code>" + html.EscapeString(codeRe.FindStringSubmatch(m)[1]) + "</code>")
	})
	md = html.EscapeString(md)
	md = headRe.ReplaceAllString(md, "<b>$1</b>")
	md = boldRe.ReplaceAllString(md, "<b>$1</b>")
	md = italRe.ReplaceAllString(md, "$1<i>$2</i>$3")
	md = linkRe.ReplaceAllStringFunc(md, func(m string) string {
		p := linkRe.FindStringSubmatch(m)
		return `<a href="` + strings.ReplaceAll(p[2], `"`, "%22") + `">` + p[1] + `</a>`
	})
	md = bulletRe.ReplaceAllString(md, "$1• ")
	for i, s := range stash {
		md = strings.Replace(md, "\x00"+string(rune('A'+i))+"\x00", s, 1)
	}
	return md
}

// Split breaks text into Telegram-sized messages (≤ limit runes) at line boundaries.
func Split(s string, limit int) []string {
	r := []rune(s)
	if len(r) <= limit {
		return []string{s}
	}
	var out []string
	for len(r) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if r[i-1] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(r[:cut]), "\n"))
		r = []rune(strings.TrimLeft(string(r[cut:]), "\n"))
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}
