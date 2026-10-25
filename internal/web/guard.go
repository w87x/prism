package web

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"prism/internal/netguard"
	"prism/internal/settings"
)

// The search guard. A web search sends its words to a third party, and the agents write those words from everything they know about the user:
// "Jackett 192.168.88.2:8080 torrent indexing" told a search engine the layout of a home network and found nothing. The guard CORRECTS a query
// before it leaves: private details are cut out by rule (addresses of the user's own network, emails, keys), and a query that looks personal is
// read by a small model that says whether it is a public question, can be rewritten into one, or can only be answered from the user's own data.
// The agent is told what was searched instead, so it can say so. A model that is slow or down never blocks a search: the rule-based cleaning has
// already happened, and the model step is skipped.

var (
	emailRe  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	secretRe = regexp.MustCompile(`\b(?:sk|pk|tvly|ghp|gho|xox[bap]|AKIA|AIza)[-_A-Za-z0-9]{12,}\b|\b[A-Fa-f0-9]{32,}\b|\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
	// an address of the user's own network, with a scheme and a port when there is one
	netAddrRe = regexp.MustCompile(`(?i)(?:https?://)?(?:(?:\d{1,3}\.){3}\d{1,3}|localhost|(?:[\w-]+\.)+(?:local|lan|internal|localhost))(?::\d{2,5})?(?:/\S*)?`)
	emptyPair = regexp.MustCompile(`"\s*"|'\s*'|\(\s*\)|\[\s*\]|“\s*”`)
	// words that make a query personal enough to be worth the model's look
	personalRe = regexp.MustCompile(`(?i)\b(my|our|mine|me|myself|i'm|i've|i|user|owner|family|wife|husband|girlfriend|boyfriend|son|daughter|kid|kids|mother|father|boss|neighbou?r|home|house|apartment|flat|address|password|passwd|passport|salary|diagnos\w*|medical|doctor|bank|account|login|credentials?|token|secret|nas|router|server|vpn|ssh|my own)\b`)
)

const guardPrompt = `You stand between an AI assistant and a public search engine. The assistant is about to send the web search query below. Decide whether it should.
- "search": it asks about the public world (a product, a film, a place, a technology, a news item), a web search can plausibly answer it, and it is not a repeat of what was already searched. Mentioning a show or a tool is not private.
- "rewrite": it contains private details (the person's name, family, address, accounts, devices, files, home network, private projects) but the underlying question is public. Give "safer_query": the same question with every private detail removed, in general words.
- "private": it can only be answered from the person's own data (their passwords, what is on their disk, who their relatives are). No public search can help.
- "skip": a web search is not worth it: the answer is in the person's own memory, files or tools (their media library, their NAS, their indexer), or it was effectively searched already in this task, or it is common knowledge, or it is too vague to search for. Say in "instead" what to do (read memory, call a specific kind of tool, stop and report what was found).
Be strict about "skip" only when the search would clearly be wasted; when in doubt answer "search".
Answer JSON only: {"verdict":"search|rewrite|private|skip","safer_query":"","why":"one short sentence","instead":""}`

type guardVerdict struct {
	Verdict string `json:"verdict"`
	Safer   string `json:"safer_query"`
	Why     string `json:"why"`
	Instead string `json:"instead"`
}

// Searches already made in a task: the guard compares each new one with them (near-duplicates are refused without any model) and counts them.
const (
	searchWarnAt = 12 // a note: stop and report if the answers are not coming
	searchMaxAt  = 25 // refused for the rest of the task
	modelAfter   = 5  // from this many searches on, every further one is looked at by the model
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

// redact cuts what can be cut by rule and reports what was removed.
func redact(q string) (string, []string) {
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
	cut(secretRe, "a key or token")
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
	clean, removed := redact(query)
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
	if s.LLM == nil || !(cfg.GuardStrict || n > modelAfter || s.needsReview(ctx, clean)) {
		return clean, note, nil
	}
	v := s.judge(ctx, clean, prev)
	switch v.Verdict {
	case "private":
		return "", "", fmt.Errorf("this search was held back: %s. It can only be answered from the user's own data, and sending it would reveal private details — use memory_find or ask the user", textOr(v.Why, "it asks for something private"))
	case "skip":
		msg := "a web search is not worth it here: " + textOr(v.Why, "it would not help")
		if strings.TrimSpace(v.Instead) != "" {
			msg += ". Instead: " + strings.TrimRight(strings.TrimSpace(v.Instead), ".")
		}
		return "", "", errors.New(msg)
	case "rewrite":
		if safer := strings.TrimSpace(v.Safer); words(safer) >= 2 {
			return safer, "the query named private details, so the search asked this instead: “" + safer + "”" + because(v.Why), nil
		}
		return "", "", fmt.Errorf("this search was held back: %s — rephrase it as a general question without the private details", textOr(v.Why, "it names private details"))
	}
	return clean, note, nil
}

func because(why string) string {
	if strings.TrimSpace(why) == "" {
		return ""
	}
	return " (" + strings.TrimSpace(why) + ")"
}

func textOr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimRight(strings.TrimSpace(a), ".")
	}
	return b
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

// needsReview: only a query that looks personal is worth a model call (on a slow local model that is half a minute): one with a personal
// word, the user's own name, or a long number (a phone, an account, an id).
func (s *Service) needsReview(ctx context.Context, q string) bool {
	if personalRe.MatchString(q) || regexp.MustCompile(`\d{6,}`).MatchString(q) {
		return true
	}
	if g := settings.Load(ctx, s.Settings, settings.KeyGeneral, settings.General{}); strings.TrimSpace(g.UserName) != "" {
		for _, part := range strings.Fields(strings.ToLower(g.UserName)) {
			if len([]rune(part)) >= 3 && strings.Contains(strings.ToLower(q), part) {
				return true
			}
		}
	}
	return false
}

func (s *Service) judge(ctx context.Context, q string, prev []string) guardVerdict {
	key := strings.ToLower(q) + "|" + fmt.Sprint(len(prev) > 0)
	if e, ok := guardCache.Load(key); ok && time.Since(e.(guardEntry).at) < time.Hour {
		return e.(guardEntry).v
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var v guardVerdict
	if err := s.LLM.CompleteJSON(cctx, s.LLM.ActionRef(cctx, "web_guard", "fast"), guardPrompt, judgeInput(q, prev), &v); err != nil {
		return guardVerdict{Verdict: "search"} // a slow or absent model never blocks a search: the rule-based cleaning has already run
	}
	v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
	if v.Verdict != "rewrite" && v.Verdict != "private" && v.Verdict != "skip" {
		v.Verdict = "search"
	}
	guardCache.Store(key, guardEntry{v, time.Now()})
	return v
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
