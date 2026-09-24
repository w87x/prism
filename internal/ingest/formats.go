// Package ingest teaches memory from documents the user provides: it works out what kind of document a file is
// (a Telegram or WhatsApp chat export, a plain chat log, notes, an article…), reads it in the way that kind
// needs and distils durable facts from it, slice by slice, as a background job with progress.
package ingest

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	KindTelegram = "telegram chat"
	KindWhatsApp = "whatsapp chat"
	KindChat     = "chat log"
	KindDocument = "document"
)

type Msg struct {
	At   time.Time
	From string
	Text string
}

type Participant struct {
	Name     string `json:"name"`
	Messages int    `json:"messages"`
}

type Info struct {
	Kind         string        `json:"kind"`
	Title        string        `json:"title"`
	Messages     int           `json:"messages"`
	Chars        int           `json:"chars"`
	Windows      int           `json:"windows"` // model passes the ingestion will take
	Participants []Participant `json:"participants"`
	From         *time.Time    `json:"from,omitempty"`
	To           *time.Time    `json:"to,omitempty"`
}

func participants(ms []Msg) []Participant {
	n := map[string]int{}
	for _, m := range ms {
		n[m.From]++
	}
	var out []Participant
	for k, v := range n {
		out = append(out, Participant{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Messages != out[j].Messages {
			return out[i].Messages > out[j].Messages
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ── Telegram JSON (Export chat history → JSON) ──────────────────────────────

type tgMessage struct {
	Type string          `json:"type"`
	Date string          `json:"date"`
	From string          `json:"from"`
	Text json.RawMessage `json:"text"`
}

func tgText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		var t string
		if json.Unmarshal(p, &t) == nil {
			sb.WriteString(t)
			continue
		}
		var o struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(p, &o) == nil {
			sb.WriteString(o.Text)
		}
	}
	return sb.String()
}

func parseTelegramJSON(data []byte) (msgs []Msg, title string, ok bool) {
	var one struct {
		Name     string      `json:"name"`
		Messages []tgMessage `json:"messages"`
		Chats    *struct {
			List []struct {
				Name     string      `json:"name"`
				Messages []tgMessage `json:"messages"`
			} `json:"list"`
		} `json:"chats"`
	}
	if json.Unmarshal(data, &one) != nil {
		return nil, "", false
	}
	add := func(ms []tgMessage) {
		for _, m := range ms {
			if m.Type != "" && m.Type != "message" {
				continue
			}
			t := strings.TrimSpace(tgText(m.Text))
			if t == "" || m.From == "" {
				continue
			}
			at, _ := time.Parse("2006-01-02T15:04:05", m.Date)
			msgs = append(msgs, Msg{At: at, From: m.From, Text: t})
		}
	}
	switch {
	case len(one.Messages) > 0:
		add(one.Messages)
		title = one.Name
	case one.Chats != nil && len(one.Chats.List) > 0: // whole-account export: take the chats one after another
		var names []string
		for _, c := range one.Chats.List {
			add(c.Messages)
			names = append(names, c.Name)
		}
		title = strings.Join(names, ", ")
		if len(names) > 3 {
			title = fmt.Sprintf("%d Telegram chats", len(names))
		}
	default:
		return nil, "", false
	}
	return msgs, title, len(msgs) > 0
}

// ── Telegram HTML (Export chat history → HTML, messages*.html) ──────────────

var (
	tgBlockRe = regexp.MustCompile(`<div class="message (default|service)[^"]*"[^>]*>`)
	tgFromRe  = regexp.MustCompile(`(?s)<div class="from_name">\s*(.*?)\s*</div>`)
	tgDateRe  = regexp.MustCompile(`class="pull_right date details" title="(\d\d)\.(\d\d)\.(\d{4}) (\d\d:\d\d:\d\d)`)
	tgTextRe  = regexp.MustCompile(`(?s)<div class="text">(.*?)</div>`)
	tgTitleRe = regexp.MustCompile(`(?s)<div class="page_header">.*?<div class="text bold">\s*(.*?)\s*</div>`)
	tagRe     = regexp.MustCompile(`(?s)<[^>]+>`)
	brRe      = regexp.MustCompile(`(?i)<br\s*/?>`)
)

func htmlToText(s string) string {
	s = brRe.ReplaceAllString(s, "\n")
	return strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(s, "")))
}

func parseTelegramHTML(data []byte) (msgs []Msg, title string, ok bool) {
	s := string(data)
	locs := tgBlockRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return nil, "", false
	}
	if m := tgTitleRe.FindStringSubmatch(s); m != nil {
		title = htmlToText(m[1])
	}
	prev := ""
	for i, l := range locs {
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := s[l[0]:end]
		if s[l[2]:l[3]] == "service" {
			continue
		}
		from := prev
		if m := tgFromRe.FindStringSubmatch(block); m != nil {
			from = htmlToText(m[1])
		}
		prev = from
		tm := tgTextRe.FindStringSubmatch(block)
		if tm == nil || from == "" {
			continue
		}
		text := htmlToText(tm[1])
		if text == "" {
			continue
		}
		var at time.Time
		if d := tgDateRe.FindStringSubmatch(block); d != nil {
			at, _ = time.Parse("2006-01-02 15:04:05", d[3]+"-"+d[2]+"-"+d[1]+" "+d[4])
		}
		msgs = append(msgs, Msg{At: at, From: from, Text: text})
	}
	return msgs, title, len(msgs) > 0
}

// ── WhatsApp text export and generic "Name: text" logs ──────────────────────

var (
	waRe = regexp.MustCompile(`^\[?(\d{1,2})[./](\d{1,2})[./](\d{2,4}),? (\d{1,2}:\d{2}(?::\d{2})?)(?:\s?([APap][Mm]))?\]?(?: -)? ([^:]{1,40}): (.*)$`)
	nmRe = regexp.MustCompile(`^([^:\n\[\]]{1,30}): (\S.*)$`)
)

func waTime(a, b, y, hm, ampm string) time.Time {
	var d, m, yr int
	fmt.Sscan(a, &d)
	fmt.Sscan(b, &m)
	fmt.Sscan(y, &yr)
	if yr < 100 {
		yr += 2000
	}
	if m > 12 { // month/day order
		d, m = m, d
	}
	layout := "2006-1-2 15:04"
	if strings.Count(hm, ":") == 2 {
		layout = "2006-1-2 15:04:05"
	}
	v := fmt.Sprintf("%d-%d-%d %s", yr, m, d, hm)
	if ampm != "" {
		layout = strings.Replace(layout, "15", "3", 1) + "PM"
		v += strings.ToUpper(ampm)
	}
	t, _ := time.Parse(layout, v)
	return t
}

func parseWhatsApp(text string) (msgs []Msg, ok bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	hits := 0
	for _, ln := range lines {
		ln = strings.TrimPrefix(ln, "\u200e")
		if m := waRe.FindStringSubmatch(ln); m != nil {
			hits++
			msgs = append(msgs, Msg{At: waTime(m[1], m[2], m[3], m[4], m[5]), From: strings.TrimSpace(m[6]), Text: m[7]})
		} else if len(msgs) > 0 && strings.TrimSpace(ln) != "" {
			msgs[len(msgs)-1].Text += "\n" + strings.TrimSpace(ln)
		}
	}
	return msgs, hits >= 10 && hits*3 >= len(lines)
}

func parseNameLog(text string) (msgs []Msg, ok bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	nonblank, hits := 0, 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		nonblank++
		if m := nmRe.FindStringSubmatch(ln); m != nil {
			hits++
			msgs = append(msgs, Msg{From: strings.TrimSpace(m[1]), Text: m[2]})
		} else if len(msgs) > 0 {
			msgs[len(msgs)-1].Text += "\n" + strings.TrimSpace(ln)
		}
	}
	return msgs, hits >= 10 && hits*10 >= nonblank*6 && len(participants(msgs)) >= 2 && len(participants(msgs)) <= 12
}

// ── windows ─────────────────────────────────────────────────────────────────

const (
	windowGap   = 6 * time.Hour
	windowMsgs  = 60
	windowChars = 7000
)

// windows cuts a chat into slices the model can read in one go: a long silence starts a new one, and so does a
// size limit.
func windows(ms []Msg) [][]Msg {
	var out [][]Msg
	var cur []Msg
	chars := 0
	for _, m := range ms {
		if len(cur) > 0 {
			last := cur[len(cur)-1]
			gap := !m.At.IsZero() && !last.At.IsZero() && m.At.Sub(last.At) > windowGap
			if gap || len(cur) >= windowMsgs || chars >= windowChars {
				out = append(out, cur)
				cur, chars = nil, 0
			}
		}
		cur = append(cur, m)
		chars += len(m.Text) + len(m.From) + 8
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func render(ms []Msg, me string) string {
	var sb strings.Builder
	day := ""
	for _, m := range ms {
		if !m.At.IsZero() {
			if d := m.At.Format("2006-01-02"); d != day {
				day = d
				fmt.Fprintf(&sb, "— %s —\n", d)
			}
			fmt.Fprintf(&sb, "[%s] ", m.At.Format("15:04"))
		}
		name := m.From
		if me != "" && strings.EqualFold(m.From, me) {
			name = m.From + " (ME)"
		}
		t := m.Text
		if len(t) > 1200 {
			t = t[:1200] + "…"
		}
		fmt.Fprintf(&sb, "%s: %s\n", name, t)
	}
	return sb.String()
}

// textWindows cuts running text into passages at paragraph breaks.
func textWindows(text string) []string {
	var out []string
	var cur strings.Builder
	for _, p := range strings.Split(text, "\n\n") {
		if cur.Len() > 0 && cur.Len()+len(p) > windowChars {
			out = append(out, cur.String())
			cur.Reset()
		}
		for len(p) > windowChars*2 { // an enormous paragraph
			out = append(out, p[:windowChars])
			p = p[windowChars:]
		}
		cur.WriteString(p + "\n\n")
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}
