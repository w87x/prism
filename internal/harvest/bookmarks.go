// Package harvest mines what the agents already did for things worth keeping — currently the pages they
// fetched, which become bookmarks — as part of the memory jobs, so the Library fills itself.
package harvest

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/tools/builtin"
)

const (
	keyBookmarks  = "harvest.bookmarks"
	scanBatch     = 400 // tool results looked at per pass
	maxCandidates = 40  // pages shown to the model per pass
	maxKept       = 8   // bookmarks made per pass
)

type state struct {
	LastID int64 `json:"last_id"`
}

type candidate struct {
	URL, Title, Snippet, Why string
	Hits                     int
	Blocked                  bool // the plain fetch was refused (Cloudflare, a bot check…): only a browser-capable agent can read it
}

const curatePrompt = `You curate the user's bookmarks for a personal assistant. Below are web pages its agents fetched while working, each with the title, the start of its text and the task it was fetched for. Pages marked BLOCKED could not be read by a plain fetch (bot protection); judge those by their address and the task alone.
Keep only pages worth returning to as a reference: documentation, tools and services, guides and tutorials, reliable sources, official pages of things the user works with or is interested in. Skip one-off pages: search results, listings that change, news items with no lasting value, error / login / consent pages, redirects, and anything that only made sense for a single lookup. Most pages should be skipped.
For each page you keep give a clean "title", a one-sentence "description" of what it is useful for, and 2-5 lowercase "keywords" someone would search with.
Answer JSON only: {"keep":[{"n":1,"title":"...","description":"...","keywords":["..."]}]}`

const agentTask = `Bookmark these pages the assistant visited earlier. Each was judged worth keeping as a reference, but a plain fetch could not read it (bot protection) or it lacked a title:
%s
For each URL: web_fetch it (render=auto tries a real browser and FlareSolverr when the site blocks plain requests; if it still fails, retry with render=browser or web_extract). Read what the page is, then call bookmark_add with the url, a clean title, a one-sentence description of what it is useful for, and 3-5 lowercase keywords. Skip a page that turns out to be a login wall, an error or an unrelated one-off, and say nothing about pages you could not open. Reply NO_REPLY when done.`

var (
	urlRe    = regexp.MustCompile(`(?m)^URL: (\S+)`)
	statusRe = regexp.MustCompile(`(?m)^Status: (\d+)`)
	titleRe  = regexp.MustCompile(`(?m)^Title: (.+)$`)
	spaceRe  = regexp.MustCompile(`\s+`)
	skipPath = regexp.MustCompile(`(?i)/(login|signin|sign-in|signup|auth|oauth|consent|captcha|cart|checkout)(/|$|\?)`)
)

var searchHosts = map[string]bool{"google.com": true, "bing.com": true, "duckduckgo.com": true, "yandex.ru": true, "yandex.com": true, "yahoo.com": true, "baidu.com": true, "startpage.com": true, "brave.com": true, "kagi.com": true}

// clean returns a normalised page URL worth considering, or "".
func clean(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".lan") || !strings.Contains(host, ".") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return ""
	}
	if searchHosts[strings.TrimPrefix(host, "www.")] && (strings.Contains(u.Path, "search") || u.Path == "/" || u.Query().Get("q") != "") {
		return ""
	}
	if skipPath.MatchString(u.Path) {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// Bookmarks looks at the pages fetched since its last pass and bookmarks the ones worth keeping. It returns how
// many bookmarks it added. Without a fast model it does nothing and does not move on, so the pages are seen
// once a model is configured.
//
// A page the plain fetch could not read (bot protection) or that came back without a title is not saved from
// what little is known: it goes, together with its siblings, to delegate — an agent task that fetches each page
// through the browser and FlareSolverr fallbacks, reads its title, description and keywords and saves the bookmark
// itself. Without a delegate such pages are skipped.
func Bookmarks(ctx context.Context, db *pgxpool.Pool, r *llm.Router, st *settings.Store, delegate func(ctx context.Context, title, input string) error) (int, error) {
	if r == nil || r.RoleRef(ctx, "fast") == "" {
		return 0, nil
	}
	wm := settings.Load(ctx, st, keyBookmarks, state{})
	rows, err := db.Query(ctx, `SELECT m.id, m.content, COALESCE(t.title, s.agent)
		FROM session_messages m JOIN sessions s ON s.id=m.session_id LEFT JOIN tasks t ON t.id=s.task_id
		WHERE m.role='tool' AND m.name='web_fetch' AND m.id>$1 ORDER BY m.id LIMIT $2`, wm.LastID, scanBatch)
	if err != nil {
		return 0, err
	}
	cands := map[string]*candidate{}
	last := wm.LastID
	for rows.Next() {
		var id int64
		var content, why string
		if err := rows.Scan(&id, &content, &why); err != nil {
			rows.Close()
			return 0, err
		}
		last = id
		m := urlRe.FindStringSubmatch(content)
		sm := statusRe.FindStringSubmatch(content)
		if m == nil || sm == nil {
			continue
		}
		blocked := false
		if !strings.HasPrefix(sm[1], "2") {
			if sm[1] != "403" && sm[1] != "429" && sm[1] != "503" && !strings.Contains(content, "Just a moment") {
				continue
			}
			blocked = true
		}
		u := clean(m[1])
		if u == "" {
			continue
		}
		c := cands[u]
		if c == nil {
			c = &candidate{URL: u, Why: why, Blocked: blocked}
			if tm := titleRe.FindStringSubmatch(content); tm != nil {
				c.Title = strings.TrimSpace(tm[1])
			}
			body := content
			if i := strings.Index(body, "\n\n"); i >= 0 {
				body = body[i+2:]
			}
			c.Snippet = spaceRe.ReplaceAllString(body, " ")
			if rs := []rune(c.Snippet); len(rs) > 240 {
				c.Snippet = string(rs[:240])
			}
			cands[u] = c
		}
		if !blocked {
			c.Blocked = false // a later fetch got through
		}
		c.Hits++
	}
	rows.Close()
	if last == wm.LastID {
		return 0, nil
	}
	list := make([]*candidate, 0, len(cands))
	if len(cands) > 0 {
		have := map[string]bool{}
		br, err := db.Query(ctx, `SELECT url FROM bookmarks`)
		if err != nil {
			return 0, err
		}
		for br.Next() {
			var u string
			if br.Scan(&u) == nil {
				have[strings.TrimRight(u, "/")] = true
			}
		}
		br.Close()
		for u, c := range cands {
			if !have[strings.TrimRight(u, "/")] {
				list = append(list, c)
			}
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Hits != list[j].Hits {
			return list[i].Hits > list[j].Hits
		}
		return list[i].URL < list[j].URL
	})
	if len(list) > maxCandidates {
		list = list[:maxCandidates]
	}
	added := 0
	var later []string
	if len(list) > 0 {
		var sb strings.Builder
		for i, c := range list {
			if c.Blocked {
				fmt.Fprintf(&sb, "%d. %s\n   BLOCKED (bot protection)\n   fetched for: %s (%d time(s))\n", i+1, c.URL, c.Why, c.Hits)
				continue
			}
			fmt.Fprintf(&sb, "%d. %s\n   title: %s\n   text: %s\n   fetched for: %s (%d time(s))\n", i+1, c.URL, c.Title, c.Snippet, c.Why, c.Hits)
		}
		var out struct {
			Keep []struct {
				N           int      `json:"n"`
				Title       string   `json:"title"`
				Description string   `json:"description"`
				Keywords    []string `json:"keywords"`
			} `json:"keep"`
		}
		if err := r.CompleteJSON(ctx, "role:fast", curatePrompt, sb.String(), &out); err != nil {
			return 0, err // the watermark stays: try these pages again next pass
		}
		seen := map[int]bool{}
		for _, k := range out.Keep {
			if added >= maxKept || k.N < 1 || k.N > len(list) || seen[k.N] {
				continue
			}
			seen[k.N] = true
			c := list[k.N-1]
			if c.Blocked || (c.Title == "" && strings.TrimSpace(k.Title) == "") {
				later = append(later, c.URL)
				continue
			}
			title := strings.TrimSpace(k.Title)
			if title == "" {
				title = c.Title
			}
			kw := []string{}
			for _, w := range k.Keywords {
				if w = strings.ToLower(strings.TrimSpace(w)); w != "" && len(kw) < 6 {
					kw = append(kw, w)
				}
			}
			if _, err := builtin.SaveBookmark(ctx, db, builtin.Bookmark{URL: c.URL, Title: title, Description: strings.TrimSpace(k.Description), Keywords: kw, Rank: 1}); err == nil {
				added++
			}
		}
	}
	if len(later) > 0 && delegate != nil {
		var sb strings.Builder
		for _, u := range later {
			fmt.Fprintf(&sb, "- %s\n", u)
		}
		if err := delegate(ctx, "Bookmark pages the agents visited", fmt.Sprintf(agentTask, sb.String())); err != nil {
			return added, err
		}
	}
	return added, st.Set(ctx, keyBookmarks, state{LastID: last})
}
