package web

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"prism/internal/netguard"
	"prism/internal/redact"
	"prism/internal/tools"
)

// The search guard. A web search sends its words to a third party, and the agents write those words from everything they know about the user:
// "Jackett 192.168.88.2:8080 torrent indexing" told a search engine the layout of a home network and found nothing. The guard CORRECTS a query
// before it leaves: private details are cut out by rule (addresses of the user's own network, emails, keys), and a query that looks personal is
// read by a small model that says whether it is a public question, can be rewritten into one, or can only be answered from the user's own data.
// The agent is told what was searched instead, so it can say so. A model that is slow or down never blocks a search: the rule-based cleaning has
// already happened, and the model step is skipped.

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// an address of the user's own network, with a scheme and a port when there is one
	netAddrRe = regexp.MustCompile(`(?i)(?:https?://)?(?:(?:\d{1,3}\.){3}\d{1,3}|localhost|(?:[\w-]+\.)+(?:local|lan|internal|localhost))(?::\d{2,5})?(?:/\S*)?`)
	emptyPair = regexp.MustCompile(`"\s*"|'\s*'|\(\s*\)|\[\s*\]|“\s*”`)
)

// The model is asked a question that has a one-word answer, so even a slow local model answers in moments (a short prompt, a few tokens out).
// Only a REWRITE needs a second, equally short call to write the corrected query.
const guardPrompt = `A web search query is about to be sent to a public search engine by an AI assistant. Answer with ONE WORD only.
SEARCH - it asks about the public world (a product, film, place, technology, news) and a web search can answer it. Mentioning a show or a tool is not private.
REWRITE - it contains private details of the user (their name, family, address, accounts, devices, files, home network, private projects) but the question itself is public.
PRIVATE - it can only be answered from the user's own data (passwords, files, relatives). No public search can help.
SKIP - a web search is not worth it: the answer is in the user's own memory, files or tools (their media library, NAS, indexer), or the same thing was already searched, or it is too vague to search for.
When in doubt answer SEARCH. Answer only: SEARCH, REWRITE, PRIVATE or SKIP.`

const rewritePrompt = `Rewrite this web search query as a general public question: remove every private detail (names, addresses, accounts, devices, file paths, home network, private projects) and keep what is being asked. Answer with the rewritten query only, nothing else.`

type guardVerdict struct {
	Verdict string `json:"verdict"`
	Safer   string `json:"safer_query"`
}

// Searches already made in a task: the guard compares each new one with them (near-duplicates are refused without any model) and counts them.
const (
	searchWarnAt = 12 // a note: stop and report if the answers are not coming
	searchMaxAt  = 25 // refused for the rest of the task
)

type runSearches struct {
	queries []string
	at      time.Time
}

var (
	searchLog   = map[int64]*runSearches{}
	searchLogMu sync.Mutex
)

func recordSearch(runID int64, q string) (prev []string, n int) {
	if runID == 0 {
		return nil, 0
	}
	searchLogMu.Lock()
	defer searchLogMu.Unlock()
	for id, r := range searchLog { // forget old runs
		if time.Since(r.at) > 6*time.Hour {
			delete(searchLog, id)
		}
	}
	r := searchLog[runID]
	if r == nil {
		r = &runSearches{}
		searchLog[runID] = r
	}
	prev = append([]string(nil), r.queries...)
	r.queries = append(r.queries, q)
	r.at = time.Now()
	return prev, len(r.queries)
}

func tokenSet(q string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 3 {
			out[w] = true
		}
	}
	return out
}

// nearDuplicate: almost the same words as an earlier search (a rephrasing finds the same pages).
func nearDuplicate(q string, prev []string) (string, bool) {
	a := tokenSet(q)
	if len(a) == 0 {
		return "", false
	}
	for _, p := range prev {
		b := tokenSet(p)
		inter := 0
		for w := range a {
			if b[w] {
				inter++
			}
		}
		union := len(a) + len(b) - inter
		if union > 0 && float64(inter)/float64(union) >= 0.75 {
			return p, true
		}
	}
	return "", false
}

type guardEntry struct {
	v  guardVerdict
	at time.Time
}

var guardCache sync.Map // lower-cased query → guardEntry (an hour)

// cleanQuery cuts what can be cut by rule and reports what was removed.
func cleanQuery(q string) (string, []string) {
	var removed []string
	cut := func(re *regexp.Regexp, label string) {
		q = re.ReplaceAllStringFunc(q, func(m string) string {
			if label == "address" {
				if _, ok := netguard.PrivateInText(m); !ok {
					return m // a public address or hostname stays
				}
			}
			removed = append(removed, label)
			return " "
		})
	}
	if clean, found := redact.Secrets(q); len(found) > 0 { // keys, tokens, passwords: never in a search query
		q = strings.ReplaceAll(clean, "[removed: ", "[")
		q = regexp.MustCompile(`\[[^\]]*\]`).ReplaceAllString(q, " ")
		removed = append(removed, "a key or token")
	}
	cut(emailRe, "an email address")
	cut(netAddrRe, "an address on the user's network")
	q = emptyPair.ReplaceAllString(q, " ") // the quotes or brackets that held a removed address
	q = strings.Join(strings.Fields(q), " ")
	q = strings.Trim(q, ` "'.,;:-`)
	return q, removed
}

func words(q string) int {
	n := 0
	for _, w := range strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 2 {
			n++
		}
	}
	return n
}

// Guard returns the query to search for and, when it differs from what was asked, a note saying why. An error means the search must not be
// made at all. runID ties the searches of one task together (0: no tracking).
func (s *Service) Guard(ctx context.Context, runID int64, query string) (use, note string, err error) {
	cfg := s.cfg(ctx)
	if cfg.GuardOff {
		return query, "", nil
	}
	clean, removed := cleanQuery(query)
	if len(removed) > 0 && words(clean) < 2 {
		return "", "", fmt.Errorf("the query is about the user's own setup (%s): a web search cannot find it and would tell a third party about it. Use memory_find for what is known, the tools of the service itself, or web_fetch with the address if the user allows it", strings.Join(uniq(removed), ", "))
	}
	if len(removed) > 0 {
		note = "private details were left out of the search (" + strings.Join(uniq(removed), ", ") + ")"
	}
	// what this task has searched so far: a near-repeat is refused outright, and the count is the budget
	prev, n := recordSearch(runID, clean)
	if p, dup := nearDuplicate(clean, prev); dup {
		return "", "", fmt.Errorf("this is almost the same search as one already made in this task (“%s”): it finds the same pages. Read what it returned, or ask a different question", p)
	}
	if n > searchMaxAt {
		return "", "", fmt.Errorf("%d searches have been made in this task — enough. Stop and report what was found, and what is still unknown", n-1)
	}
	if n == searchWarnAt {
		note = strings.TrimSpace(note + "; " + fmt.Sprintf("this is search %d in this task — if the answers are not turning up, stop and report what you have", n))
		note = strings.TrimLeft(note, "; ")
	}
	if s.LLM == nil || cfg.GuardModelOff {
		return clean, note, nil
	}
	v := s.judge(ctx, clean, prev)
	switch v.Verdict {
	case "private":
		return "", "", errors.New("this search was held back: it can only be answered from the user's own data, and sending it would reveal private details. Use memory_find, the user's own tools, or ask the user")
	case "skip":
		return "", "", errors.New("a web search is not worth it here: the answer is most likely in the user's own memory, files or tools, or was already searched, or the question is too vague to search for. Try memory_find or the tools of the service itself, or stop and report what you have")
	case "rewrite":
		if safer := strings.TrimSpace(v.Safer); words(safer) >= 2 {
			return safer, "the query named private details, so the search asked this instead: “" + safer + "”", nil
		}
		return "", "", errors.New("this search was held back: it names private details — rephrase it as a general question without them")
	}
	return clean, note, nil
}

func uniq(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (s *Service) judge(ctx context.Context, q string, prev []string) guardVerdict {
	key := strings.ToLower(q) + "|" + fmt.Sprint(len(prev) > 0)
	if e, ok := guardCache.Load(key); ok && time.Since(e.(guardEntry).at) < time.Hour {
		return e.(guardEntry).v
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	ref := s.LLM.ActionRef(cctx, "web_guard", "fast")
	out, err := s.LLM.Complete(cctx, ref, guardPrompt, judgeInput(q, prev), false)
	if err != nil {
		return guardVerdict{Verdict: "search"} // a slow or absent model never blocks a search: the rule-based cleaning has already run
	}
	v := guardVerdict{Verdict: "search"}
	switch word := firstWord(out); word {
	case "REWRITE", "PRIVATE", "SKIP":
		v.Verdict = strings.ToLower(word)
	}
	if v.Verdict == "rewrite" {
		if r, err := s.LLM.Complete(cctx, ref, rewritePrompt, q, false); err == nil {
			v.Safer = strings.Trim(strings.TrimSpace(strings.SplitN(strings.TrimSpace(r), "\n", 2)[0]), `"'`)
		}
	}
	guardCache.Store(key, guardEntry{v, time.Now()})
	return v
}

// firstWord is the answer's first word in capitals, whatever the model wrapped around it ("**Search**.", "Answer: SKIP").
func firstWord(s string) string {
	for _, w := range strings.FieldsFunc(strings.ToUpper(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
		switch w {
		case "SEARCH", "REWRITE", "PRIVATE", "SKIP":
			return w
		}
	}
	return ""
}

func judgeInput(q string, prev []string) string {
	in := "QUERY: " + q
	if len(prev) > 0 {
		if len(prev) > 10 {
			prev = prev[len(prev)-10:]
		}
		in += "\n\nALREADY SEARCHED IN THIS TASK:\n- " + strings.Join(prev, "\n- ")
	}
	return in
}

// ── pages that refuse us ──
//
// A site that answers 403/439/451 to a fetch will answer it again: 42 recent fetches of one marketplace came back "HTTP 439" while agents tried
// the same host with other tools, other URLs and other renderers. Three such refusals from a host the agent CHOSE close it for the rest of the
// task, and a host that has refused many times lately (across tasks) is left alone for a while. But a host the task itself names — the user or the
// delegator said "check avito.ru" — is the only place the answer can come from: it is never refused, only flagged, so the agent can report that it
// is blocked instead of trying tricks. A 429 (slow down) or a server error is not a block and is never counted.
const (
	hostStrikes    = 3
	hostCooldownAt = 8
	hostWindow     = 15 * time.Minute
	hostCooldown   = 10 * time.Minute
)

var (
	hostPerRun = map[string]int{}
	hostRecent = map[string][]time.Time{}
	hostClosed = map[string]time.Time{}
	hostMu     sync.Mutex
)

func hostOf(raw string) string {
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil {
		return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	}
	return ""
}

// registrable is the last two labels of a host ("shop.avito.ru" → "avito.ru").
func registrable(h string) string {
	p := strings.Split(h, ".")
	if len(p) <= 2 {
		return h
	}
	return strings.Join(p[len(p)-2:], ".")
}

func blocked(status int) bool { return status == 403 || status == 439 || status == 451 }

// mandated: the task text names this host.
func (s *Service) mandated(ctx context.Context, taskID int64, host string) bool {
	if s.TaskText == nil || taskID == 0 {
		return false
	}
	text := strings.ToLower(s.TaskText(ctx, taskID))
	return text != "" && (strings.Contains(text, host) || strings.Contains(text, registrable(host)))
}

// fetchBefore says whether a fetch of raw may be made, and may return a note to put in front of the result.
func (s *Service) fetchBefore(ctx context.Context, env *tools.Env, raw string) (note string, err error) {
	if s.cfg(ctx).GuardOff {
		return "", nil
	}
	h := hostOf(raw)
	if h == "" {
		return "", nil
	}
	named := s.mandated(ctx, env.TaskID, h)
	hostMu.Lock()
	defer hostMu.Unlock()
	if until, ok := hostClosed[h]; ok {
		if time.Now().Before(until) {
			if named {
				return h + " has refused many requests in the last minutes — it is blocking PRISM. The task names it, so this try goes ahead, but if it refuses again, stop and report that it is blocked.", nil
			}
			return "", fmt.Errorf("%s has refused many requests in the last minutes (it is blocking PRISM) and is being left alone until %s. Use another source, or report that it is blocked", h, until.Format("15:04"))
		}
		delete(hostClosed, h)
	}
	if env.RunID != 0 && hostPerRun[fmt.Sprintf("%d|%s", env.RunID, h)] >= hostStrikes {
		if named {
			return h + " has refused " + fmt.Sprint(hostStrikes) + " requests in this task: it is blocking us. The task names this site, so it is tried again, but another page or another renderer will not change that — if it refuses again, stop and report that it is blocked.", nil
		}
		return "", fmt.Errorf("%s has refused %d requests in this task: it is blocking us, and trying again (another page, another renderer) will not change that. Use another source, a different tool, or report that it is blocked", h, hostStrikes)
	}
	return "", nil
}

// fetchAfter records how a fetch went; it returns a note when the site is rate-limiting (a 429 is "wait", not "blocked").
func (s *Service) fetchAfter(runID int64, raw string, status int, err error) (note string) {
	h := hostOf(raw)
	if h == "" {
		return ""
	}
	hostMu.Lock()
	defer hostMu.Unlock()
	key := fmt.Sprintf("%d|%s", runID, h)
	switch {
	case status == 429:
		return h + " is rate-limiting (HTTP 429): wait a little before the next request to it, and ask for less"
	case !blocked(status):
		if err == nil && status/100 == 2 {
			delete(hostPerRun, key) // it let us in
		}
		return ""
	}
	if runID != 0 {
		hostPerRun[key]++
	}
	now := time.Now()
	keep := hostRecent[h][:0]
	for _, t := range hostRecent[h] {
		if now.Sub(t) < hostWindow {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	hostRecent[h] = keep
	if len(keep) >= hostCooldownAt {
		hostClosed[h] = now.Add(hostCooldown)
		hostRecent[h] = nil
	}
	return ""
}
