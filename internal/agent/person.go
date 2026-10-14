package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"prism/internal/settings"
)

// The person model. The user bank holds facts about the user, but facts are not a working relationship: how they like to be spoken
// to, when they are around, what annoys them, when not to bother them. That is learned from how they actually use PRISM — their own
// messages, when they write, what they rated badly and why, the facts memory holds — and kept as one short core-memory block
// ("working_with_user") that is in every agent's prompt. It is refreshed weekly with the standup, or on demand.

const personBlock = "working_with_user"

const personPrompt = `You maintain a short profile of how to work with one particular person, for AI agents that will read it before every task. You get samples of their own messages, when they are active, their ratings and complaints about results, facts memory holds about them, and the current profile.
Write the updated profile: at most 650 characters, plain statements about "the user", no headings, no filler. Cover only what the evidence supports: how they write and like to be answered (language, length, tone), their rhythm (when they are around, when not to interrupt), what they dislike or complained about, and one or two standing do/don't rules that follow. Keep what the current profile says when it is still supported, drop what the evidence contradicts, never invent. Reply with the profile text only.`

// activeHours summarises when the user writes: the hours that hold a real share of their messages.
func activeHours(ts []time.Time) string {
	if len(ts) < 20 {
		return ""
	}
	var h [24]int
	for _, t := range ts {
		h[t.Hour()]++
	}
	var busy []int
	for hr, n := range h {
		if float64(n)/float64(len(ts)) >= 0.05 {
			busy = append(busy, hr)
		}
	}
	sort.Ints(busy)
	if len(busy) == 0 {
		return ""
	}
	var parts []string
	start, prev := busy[0], busy[0]
	for _, hr := range busy[1:] {
		if hr != prev+1 {
			parts = append(parts, fmt.Sprintf("%02d:00–%02d:00", start, prev+1))
			start = hr
		}
		prev = hr
	}
	parts = append(parts, fmt.Sprintf("%02d:00–%02d:00", start, prev+1))
	quiet := 0
	for hr := 0; hr < 24; hr++ {
		if h[hr] == 0 {
			quiet++
		}
	}
	return fmt.Sprintf("writes mostly %s (%d messages in 30 days; %d hours of the day with none)", strings.Join(parts, ", "), len(ts), quiet)
}

// RefreshPersonModel rebuilds the profile from the evidence and stores it as a core block. It returns the new text ("" when there
// was too little evidence to say anything).
func (e *Engine) RefreshPersonModel(ctx context.Context) (string, error) {
	if e.Memory == nil || e.LLM == nil || e.LLM.RoleRef(ctx, "chat") == "" {
		return "", nil
	}
	loc := time.Local
	if tz := settings.Load(ctx, e.Settings, settings.KeyGeneral, settings.General{}).Timezone; tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	rows, err := e.DB.Query(ctx, `SELECT text, created_at FROM chat_messages WHERE role='user' AND created_at>now()-interval '30 days' ORDER BY id DESC LIMIT 600`)
	if err != nil {
		return "", err
	}
	var msgs []string
	var when []time.Time
	for rows.Next() {
		var t string
		var at time.Time
		if rows.Scan(&t, &at) == nil {
			if len(msgs) < 40 {
				msgs = append(msgs, brief(strings.Join(strings.Fields(t), " "), 180))
			}
			when = append(when, at.In(loc))
		}
	}
	rows.Close()
	if len(msgs) < 12 {
		return "", nil
	}
	var sb strings.Builder
	sb.WriteString("THEIR RECENT MESSAGES (newest first):\n")
	for _, m := range msgs {
		sb.WriteString("- " + m + "\n")
	}
	if ah := activeHours(when); ah != "" {
		sb.WriteString("\nRHYTHM: " + ah + "\n")
	}
	if fr, err := e.DB.Query(ctx, `SELECT t.to_agent, left(t.title,50), m.rating, m.rating_note FROM task_meta m JOIN tasks t ON t.id=m.task_id WHERE m.rating<>0 AND m.rated_at>now()-interval '30 days' ORDER BY m.rated_at DESC LIMIT 25`); err == nil {
		first := true
		for fr.Next() {
			var a, title, note string
			var r int
			if fr.Scan(&a, &title, &r, &note) == nil {
				if first {
					sb.WriteString("\nTHEIR RATINGS OF RESULTS:\n")
					first = false
				}
				fmt.Fprintf(&sb, "- %s on %s “%s”%s\n", map[bool]string{true: "liked", false: "disliked"}[r > 0], a, title, map[bool]string{true: " — " + note}[note != ""])
			}
		}
		fr.Close()
	}
	if ff, err := e.DB.Query(ctx, `SELECT f.text FROM memory_facts f JOIN memory_banks b ON b.id=f.bank_id WHERE b.kind='user' AND f.kind='fact' AND f.valid_to IS NULL AND f.status<>'proposed' AND f.confidence>=0.6 ORDER BY f.id DESC LIMIT 40`); err == nil {
		first := true
		for ff.Next() {
			var t string
			if ff.Scan(&t) == nil {
				if first {
					sb.WriteString("\nFACTS MEMORY HOLDS ABOUT THEM:\n")
					first = false
				}
				sb.WriteString("- " + brief(t, 160) + "\n")
			}
		}
		ff.Close()
	}
	cur := ""
	if blocks, err := e.Memory.CoreBlocks(ctx); err == nil {
		for _, b := range blocks {
			if b.Label == personBlock {
				cur = b.Text
			}
		}
	}
	sb.WriteString("\nCURRENT PROFILE:\n" + orNone(cur))
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := e.LLM.Complete(cctx, e.LLM.ActionRef(cctx, "person_model", "chat"), personPrompt, sb.String(), false)
	text := strings.TrimSpace(out)
	if err != nil || len(text) < 30 {
		return "", err
	}
	if r := []rune(text); len(r) > 780 {
		text = string(r[:780])
	}
	if err := e.Memory.SetCoreBlock(ctx, personBlock, text, "How to work with the user, learned from how they actually use PRISM (refreshed weekly)", "person-model"); err != nil {
		return "", err
	}
	return text, nil
}
