package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/llm"
)

const (
	// foldKeepRecent is how many of the newest messages are always sent verbatim: the agent must see the
	// full text of the failure it is reacting to right now.
	foldKeepRecent = 8
	// foldErrChars bounds the error text kept once an attempt is folded.
	foldErrChars = 160
	// foldArgChars: arguments of a failed call longer than this are replaced (a base64 payload sent
	// to a tool that then failed is pure dead weight on every later turn).
	foldArgChars = 240
)

// foldFailures rewrites the model's view of OLD failed tool attempts into one-line facts: "tool X failed
// (attempt k): brief error", with the oversized arguments of the failed call elided. Only what is sent to
// the model changes — the stored history (and the transcript the user reads) keeps the full text. Messages
// are never removed, so every tool_call keeps its matching tool result.
//
// The point is retry loops: an agent that fails the same step six times used to carry six full error
// outputs plus six full argument payloads in every later request.
func foldFailures(ms []Msg, out []llm.Message) {
	cutoff := len(ms) - foldKeepRecent
	if cutoff <= 0 {
		return
	}
	// which assistant message / call index produced each tool_call_id
	type loc struct{ msg, call int }
	calls := map[string]loc{}
	for i := 0; i < cutoff; i++ {
		for j, tc := range out[i].ToolCalls {
			calls[tc.ID] = loc{i, j}
		}
	}
	attempts := map[string]int{}
	for i := 0; i < cutoff; i++ {
		m := out[i]
		if m.Role != "tool" || !strings.HasPrefix(m.Content, "Error:") {
			continue
		}
		name := m.Name
		if l, ok := calls[m.ToolCallID]; ok && name == "" {
			name = out[l.msg].ToolCalls[l.call].Name
		}
		if name == "" {
			name = "tool"
		}
		attempts[name]++
		first := strings.TrimSpace(strings.TrimPrefix(m.Content, "Error:"))
		if k := strings.IndexByte(first, '\n'); k >= 0 {
			first = first[:k]
		}
		if r := []rune(first); len(r) > foldErrChars {
			first = string(r[:foldErrChars]) + "…"
		}
		out[i].Content = fmt.Sprintf("[%s failed (attempt %d): %s]", name, attempts[name], first)
		if l, ok := calls[m.ToolCallID]; ok {
			// copy the slice before editing: out[l.msg].ToolCalls shares its backing array with ms
			tcs := append([]llm.ToolCall(nil), out[l.msg].ToolCalls...)
			if n := len(tcs[l.call].Arguments); n > foldArgChars {
				b, _ := json.Marshal(fmt.Sprintf("%d chars of arguments from a failed attempt, elided", n))
				tcs[l.call].Arguments = `{"_elided":` + string(b) + `}`
				out[l.msg].ToolCalls = tcs
			}
		}
	}
}

// Tool results are capped so one fat output cannot flood the context. The cap is generous while the run
// still has plenty of room (a first big read is worth having whole) and tight once the context has filled
// up, where every extra token is resent on every remaining turn.
const (
	toolResultCapTight = 6000
	toolResultCapRoomy = maxToolResultChars
)

func toolResultCap(ctxTokens, window int64) int {
	if window > 0 && float64(ctxTokens) < 0.15*float64(window) {
		return toolResultCapRoomy
	}
	return toolResultCapTight
}

// dedupeToolOutputs replaces a repeat of an earlier identical tool output (same tool, same text, over
// dedupeMinChars) with a pointer to it, in the model's view only. The FIRST copy stays untouched, so the
// prompt prefix is stable as a run grows; copies in the newest messages stay verbatim. Run it after
// foldFailures so a folded failure is not mistaken for content.
func dedupeToolOutputs(ms []Msg, out []llm.Message) {
	cutoff := len(ms) - foldKeepRecent
	seen := map[string]bool{}
	for i := range out {
		m := out[i]
		if m.Role != "tool" || len(m.Content) <= dedupeMinChars {
			continue
		}
		key := m.Name + "\x00" + m.Content
		if !seen[key] {
			seen[key] = true
			continue
		}
		if i < cutoff {
			out[i].Content = fmt.Sprintf("[identical to an earlier %s output (%d chars) — not repeated]", m.Name, len(m.Content))
		}
	}
}

const dedupeMinChars = 400

const (
	elideKeepRecent = 12   // newest messages never elided
	elideMinChars   = 2500 // only bulky outputs are worth it
	elideHead       = 700
	elideTail       = 300
)

// elideStaleOutputs keeps the head and tail of an old, bulky, SUCCESSFUL tool output and drops the middle,
// in the model's view only: by then the agent has acted on it, and re-reading is one tool call away.
// Failures and duplicates are already one-liners by this point (they start with "[").
func elideStaleOutputs(ms []Msg, out []llm.Message) {
	cutoff := len(ms) - elideKeepRecent
	for i := 0; i < cutoff; i++ {
		m := out[i]
		if m.Role != "tool" || strings.HasPrefix(m.Content, "[") {
			continue
		}
		r := []rune(m.Content)
		if len(r) <= elideMinChars {
			continue
		}
		out[i].Content = string(r[:elideHead]) + fmt.Sprintf("\n…[%d chars of this older %s output omitted — call it again if you need them]…\n", len(r)-elideHead-elideTail, m.Name) + string(r[len(r)-elideTail:])
	}
}

// foldedView is the history as the model should see it: stored messages with old failures folded, repeats
// deduped and stale bulky outputs elided. The stored history itself is never changed.
func foldedView(ms []Msg) []llm.Message {
	out := msgsOf(ms)
	foldFailures(ms, out)
	dedupeToolOutputs(ms, out)
	elideStaleOutputs(ms, out)
	return out
}

// ctxBreakdown splits the estimated prompt into where the tokens go, for the live-run view.
func ctxBreakdown(sys, toolSpecs int, view []llm.Message) map[string]int {
	b := map[string]int{"system": sys, "tools": toolSpecs}
	for _, m := range view {
		k := m.Role
		if k == "system" {
			k = "user"
		}
		b[k] += llm.EstimateMessages([]llm.Message{m})
	}
	return b
}
