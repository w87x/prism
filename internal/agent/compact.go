package agent

import (
	"context"
	"fmt"
	"strings"

	"prism/internal/llm"
)

// Tools whose results can be re-fetched at will: dropped first during compaction.
var refetchable = map[string]bool{"memory_find": true, "memory_list": true, "memory_banks": true, "tool_search": true, "skill_search": true, "agent_find": true, "clock": true}

const summaryTemplate = `Summarize the conversation so far so the agent can continue seamlessly. Use EXACTLY this template, be dense, under 450 words, keep concrete values (ids, URLs, paths, numbers, names):

## Goal
## Established facts and results
## Decisions and constraints
## Done so far
## Pending / next steps
## Key references

If an earlier summary is included, merge it into the new one instead of repeating it.`

// compact shrinks history toward targetTokens using tiered strategies:
//  1. drop stale, re-fetchable tool results (memory reads…) and dedupe identical results
//  2. elide bulky old tool outputs
//  3. summarize the oldest part with the fast model into a fixed template
//
// The system prompt (soul, scratchpad, skills) is rebuilt from the database on
// every model call, so nothing needs re-injecting afterwards.
func (e *Engine) compact(ctx context.Context, spec RunSpec, sess *Session, history []Msg, target int, force bool) ([]Msg, error) {
	est := func(ms []Msg) int { return llm.EstimateMessages(msgsOf(ms)) }
	keepTail := 6
	if len(history) <= keepTail {
		return history, nil
	}
	cutoff := len(history) - keepTail
	ms := make([]Msg, len(history))
	copy(ms, history)

	// tier 1: refetchable results + duplicates
	seen := map[string]bool{}
	for i := 0; i < cutoff; i++ {
		m := &ms[i]
		if m.Role != "tool" {
			continue
		}
		if refetchable[m.Name] && len(m.Content) > 60 {
			m.Content = "[" + m.Name + " result dropped during compaction — query again if needed]"
			continue
		}
		if h := m.Name + "\x00" + m.Content; len(m.Content) > 200 {
			if seen[h] {
				m.Content = "[duplicate of an earlier " + m.Name + " result]"
			}
			seen[h] = true
		}
	}
	if !force && est(ms) <= target {
		return e.persistCompaction(ctx, sess, ms)
	}
	// tier 2: elide bulky old tool outputs, keeping a stub
	for i := 0; i < cutoff; i++ {
		m := &ms[i]
		if m.Role == "tool" && len(m.Content) > 500 && !strings.HasPrefix(m.Content, "[") {
			// len() above is bytes; multi-byte text (CJK, emoji…) can be well over 500 bytes with fewer than
			// 220 runes, so the cut point must be bounded by the actual rune count, not assumed from the byte check.
			r := []rune(m.Content)
			cut := min(220, len(r))
			m.Content = string(r[:cut]) + fmt.Sprintf("\n…[%s result elided during compaction, %d chars]", m.Name, len(r)-cut)
		}
	}
	if est(ms) <= target {
		return e.persistCompaction(ctx, sess, ms)
	}

	// tier 3: summarize the head, keep a tail that fits the budget
	reserve := 900
	budget := target - reserve
	if budget < 400 {
		budget = 400
	}
	tailStart := len(ms)
	acc := 0
	for i := len(ms) - 1; i >= 1; i-- {
		t := est(ms[i : i+1])
		if acc+t > budget && tailStart < len(ms)-1 {
			break
		}
		acc += t
		tailStart = i
	}
	// never start the tail on a tool result (orphan) — walk back to its assistant call
	for tailStart > 0 && ms[tailStart].Role == "tool" {
		tailStart--
	}
	if tailStart <= 0 {
		return e.persistCompaction(ctx, sess, ms)
	}
	head, tail := ms[:tailStart], ms[tailStart:]
	summary, tainted := e.summarize(ctx, head)
	out := make([]Msg, 0, len(tail)+1)
	out = append(out, Msg{Message: llm.Message{Role: "user", Content: "[Conversation summary — earlier turns were compacted]\n" + summary}, Provenance: "system", Tainted: tainted})
	out = append(out, tail...)
	return e.persistCompaction(ctx, sess, out)
}

func (e *Engine) persistCompaction(ctx context.Context, sess *Session, ms []Msg) ([]Msg, error) {
	if sess == nil || sess.ID == 0 {
		return ms, nil
	}
	return e.Sessions.Replace(ctx, sess.ID, ms)
}

func render(ms []Msg, toolCap int) string {
	var sb strings.Builder
	for _, m := range ms {
		switch m.Role {
		case "user":
			sb.WriteString("USER: " + m.Content + "\n")
		case "assistant":
			if m.Content != "" {
				sb.WriteString("ASSISTANT: " + m.Content + "\n")
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&sb, "ASSISTANT called %s(%s)\n", tc.Name, brief(tc.Arguments, 200))
			}
		case "tool":
			fmt.Fprintf(&sb, "TOOL %s → %s\n", m.Name, brief(m.Content, toolCap))
		}
	}
	return sb.String()
}

// summarize condenses msgs; it falls back to a heuristic digest without a model.
func (e *Engine) summarize(ctx context.Context, msgs []Msg) (string, bool) {
	tainted := false
	for _, m := range msgs {
		tainted = tainted || m.Tainted
	}
	ref := "role:fast"
	window := e.LLM.Window(ctx, ref)
	chunkChars := window * 3 / 2 // ~60% of the window in chars (≈4 chars/token)
	if chunkChars < 6000 {
		chunkChars = 6000
	}
	text := render(msgs, 500)
	summary := ""
	for len(text) > 0 {
		part := text
		if len(part) > chunkChars {
			cut := strings.LastIndexByte(part[:chunkChars], '\n')
			if cut <= 0 {
				cut = chunkChars
			}
			part = part[:cut]
		}
		text = text[len(part):]
		prompt := part
		if summary != "" {
			prompt = "EARLIER SUMMARY:\n" + summary + "\n\nNEW CONVERSATION PART:\n" + part
		}
		out, err := e.LLM.Complete(ctx, ref, summaryTemplate, prompt, false)
		if err != nil || strings.TrimSpace(out) == "" {
			return heuristicSummary(summary, msgs), tainted
		}
		summary = strings.TrimSpace(out)
	}
	return summary, tainted
}

func heuristicSummary(prev string, msgs []Msg) string {
	var sb strings.Builder
	if prev != "" {
		sb.WriteString(prev + "\n")
	}
	sb.WriteString("## Digest (automatic; model summarization was unavailable)\n")
	for _, m := range msgs {
		switch {
		case m.Role == "user" && m.Provenance != "system":
			sb.WriteString("- User: " + brief(m.Content, 160) + "\n")
		case m.Role == "assistant" && m.Content != "":
			sb.WriteString("- Agent: " + brief(m.Content, 160) + "\n")
		}
	}
	return sb.String()
}

// CompactNow lets the UI force compaction of a chat session.
func (e *Engine) CompactNow(ctx context.Context, sess *Session, p *Profile) error {
	hist, err := e.Sessions.Messages(ctx, sess.ID)
	if err != nil {
		return err
	}
	window := e.LLM.Window(ctx, p.Model)
	_, err = e.compact(ctx, RunSpec{Profile: p, Session: sess}, sess, hist, window/5, true)
	return err
}
