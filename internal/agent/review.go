package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"prism/internal/settings"
	"prism/internal/textutil"
	"prism/internal/tools"
)

// Four eyes. Approval prompts for every small action train people to click "allow" without reading, which makes the prompts that
// matter worthless. With the reviewer switched on (Settings → Autonomy → Reviewer), a tool call that would ask the user is first
// put to the main model, a second pair of eyes that knows the request: it approves what clearly follows from what the user asked,
// refuses what clearly does not, and passes everything else to the user as before. Some things never go to it: anything while
// untrusted content is in the turn (the prompt-injection case the prompt exists for), shell/exec tools, and tools that send,
// delete, pay or publish.

// neverAuto matches whole words of a snake_case tool name (email_send, gh_post_comment), not fragments of other words
// (payload, format-free "information", compost): the filter is meant to be strict about what it covers, not noisy about the rest.
var neverAuto = regexp.MustCompile(`(?i)(^|[_\-. ])(send|delete|remove|wipe|format|pay|buy|purchase|transfer|publish|post|drop|kill|uninstall|install|sudo|push|force|reset|revoke|grant|upload)(s|es|ed|ing)?($|[_\-. ])`)

const reviewPrompt = `You are the second pair of eyes before an AI agent runs a tool that would normally ask its user for permission.
Decide from the user's request and the call itself:
- "approve": the call is clearly part of doing what the user asked, limited in scope, and easy to undo or harmless.
- "deny": the call is clearly NOT what the user asked for, or is clearly harmful or far out of proportion.
- "ask": anything else — unclear, broader than the request, hard to undo, touching other people or accounts, or you are not sure. When in doubt, ask.
Answer JSON only: {"verdict":"approve|deny|ask","reason":"one short sentence"}`

// preReview returns "approve", "deny" or "ask" (and why) for a call that would otherwise go to the user.
func (e *Engine) preReview(ctx context.Context, env *tools.Env, tool *tools.Tool, tc string, args string, request string) (verdict, reason string) {
	cfg := settings.Load(ctx, e.Settings, settings.KeyReview, settings.Review{})
	if !cfg.Enabled || e.LLM == nil || e.LLM.RoleRef(ctx, "chat") == "" || e.LLMDown() {
		return "ask", ""
	}
	if neverAuto.MatchString(tc) {
		return "ask", ""
	}
	for _, n := range cfg.AlwaysAsk {
		if strings.EqualFold(strings.TrimSpace(n), tc) {
			return "ask", ""
		}
	}
	// trust earned by how this agent's earlier work with the tool turned out: a good record goes through, a bad one is asked about
	switch level, w, l := e.trustLevel(ctx, env.Agent, tc); level {
	case TrustDistrusted:
		return "ask", ""
	case TrustTrusted:
		if tool.Risk != tools.RiskExec {
			reason := fmt.Sprintf("trusted: %d good runs with it, %d bad", w, l)
			_, _ = e.DB.Exec(ctx, `INSERT INTO review_log (agent, tool, args, verdict, reason, task_id) VALUES ($1,$2,$3,'approve',$4,NULLIF($5,0))`, env.Agent, tc, textutil.Brief(args, 400), reason, env.TaskID)
			return "approve", reason
		}
	}
	var v struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	in := "THE USER'S REQUEST (what the agent is working on):\n" + textutil.Brief(request, 1500) +
		"\n\nAGENT: " + env.Agent + "\nTOOL: " + tc + " [risk: " + tool.Risk.String() + "] — " + textutil.Brief(tool.Description, 240) +
		"\nARGUMENTS:\n" + textutil.Brief(args, 900)
	if err := e.LLM.CompleteJSON(ctx, e.LLM.ActionRef(ctx, "tool_review", "chat"), reviewPrompt, in, &v); err != nil {
		return "ask", ""
	}
	verdict, reason = strings.ToLower(strings.TrimSpace(v.Verdict)), strings.TrimSpace(v.Reason)
	switch verdict {
	case "approve":
		if tool.Risk == tools.RiskExec {
			verdict = "ask" // code that runs, reaching outside: the reviewer may refuse it but never wave it through
		}
	case "deny":
		e.noteTrustLoss(ctx, env.Agent, tc, 1)
	default:
		verdict = "ask"
	}
	_, _ = e.DB.Exec(ctx, `INSERT INTO review_log (agent, tool, args, verdict, reason, task_id) VALUES ($1,$2,$3,$4,$5,NULLIF($6,0))`, env.Agent, tc, textutil.Brief(args, 400), verdict, reason, env.TaskID)
	return verdict, reason
}

// ReviewLogRow is one reviewer decision for the settings page.
type ReviewLogRow struct {
	ID      int64  `json:"id"`
	At      string `json:"at"`
	Agent   string `json:"agent"`
	Tool    string `json:"tool"`
	Args    string `json:"args"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

func (e *Engine) ReviewLog(ctx context.Context, limit int) ([]ReviewLogRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := e.DB.Query(ctx, `SELECT id, to_char(at,'YYYY-MM-DD"T"HH24:MI:SSOF'), agent, tool, args, verdict, reason FROM review_log ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReviewLogRow{}
	for rows.Next() {
		var r ReviewLogRow
		if rows.Scan(&r.ID, &r.At, &r.Agent, &r.Tool, &r.Args, &r.Verdict, &r.Reason) == nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}
