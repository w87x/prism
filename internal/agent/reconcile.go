package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"prism/internal/memory"
	"prism/internal/tasks"
	"prism/internal/tools"
)

// After a restart a task that was running is requeued and resumes its old session. The model that resumes it only
// has the transcript — and a call that was cut off mid-flight has no result in it, a download that finished while
// the model was not looking is not in it at all — so a bare "check before repeating" note was not enough: it
// asked for a download again that had already completed. This builds the evidence first: calls left without a
// result, effects that did complete, downloads and background processes started for the task, what memory knows
// about it — and has the fast model say whether the work looks finished. The model resuming the task reads that
// before it does anything.

const restartNote = "[system] PRISM restarted while you were working on this task. Before doing ANYTHING with a real effect (a download, a message sent, a file written, a purchase, a deletion…) read the findings below. If the work already happened, do NOT repeat it: confirm it with a read-only check if you can (download_status, file_list, a search) and finish with a short answer saying so. Repeat a step only when the evidence shows it did not complete — and say what you are unsure about rather than guessing."

type restartVerdict struct {
	Verdict   string `json:"verdict"` // finished | unfinished | unsure
	Why       string `json:"why"`
	Remaining string `json:"remaining"`
}

const restartReviewPrompt = `PRISM was restarted while an agent was working on a task. Judge from the EVIDENCE only whether the task's work has actually been completed already.
- "finished": the evidence shows everything the task asked for was done (a completed download whose file exists, a result already obtained…).
- "unfinished": the evidence shows it was not done, or only partly — say what remains.
- "unsure": the evidence does not settle it.
An unfinished or cut-off tool call is NOT evidence that the effect did not happen. Never assume; quote what you rely on.
Answer JSON only: {"verdict":"finished|unfinished|unsure","why":"...","remaining":"..."}`

// restartFindings is the deterministic part: facts gathered from the transcript and the system.
func (e *Engine) restartFindings(ctx context.Context, t tasks.Task, agent string, hist []Msg) string {
	var sb strings.Builder
	results := map[string]string{}
	for _, m := range hist {
		if m.Role == "tool" && m.ToolCallID != "" {
			results[m.ToolCallID] = m.Content
		}
	}
	var open, done []string
	for _, m := range hist {
		for _, tc := range m.ToolCalls {
			line := fmt.Sprintf("%s(%s)", tc.Name, brief(tc.Arguments, 140))
			res, ok := results[tc.ID]
			if !ok {
				open = append(open, "- "+line)
				continue
			}
			if tool, found := e.Tools.Get(tc.Name); found && tool.Risk != tools.RiskRead {
				done = append(done, fmt.Sprintf("- %s → %s", line, brief(res, 160)))
			}
		}
	}
	if len(open) > 0 {
		sb.WriteString("Tool calls that were still running or had no result recorded when PRISM stopped (they may or may not have taken effect):\n" + strings.Join(open, "\n") + "\n")
	}
	if len(done) > 5 {
		done = done[len(done)-5:]
	}
	if len(done) > 0 {
		sb.WriteString("Calls with a real effect that DID return a result (don't repeat these):\n" + strings.Join(done, "\n") + "\n")
	}
	if e.DB != nil {
		if rows, err := e.DB.Query(ctx, `SELECT id,url,dest,status,bytes,total,error FROM downloads WHERE owner=$1 AND created_at >= $2 ORDER BY id DESC LIMIT 8`, agent, t.CreatedAt); err == nil {
			var lines []string
			for rows.Next() {
				var id, bytes, total int64
				var url, dest, status, errText string
				if rows.Scan(&id, &url, &dest, &status, &bytes, &total, &errText) != nil {
					continue
				}
				l := fmt.Sprintf("- download #%d %s → %s: %s", id, brief(url, 100), dest, status)
				if status == "done" {
					if st, err := os.Stat(dest); err == nil {
						l += fmt.Sprintf(" (file exists, %d bytes)", st.Size())
					} else {
						l += " (but the file is NOT there now)"
					}
				} else if errText != "" {
					l += " — " + brief(errText, 100)
				} else if total > 0 {
					l += fmt.Sprintf(" (%d of %d bytes)", bytes, total)
				}
				lines = append(lines, l)
			}
			rows.Close()
			if len(lines) > 0 {
				sb.WriteString("Downloads this agent started since the task began:\n" + strings.Join(lines, "\n") + "\n")
			}
		}
		if rows, err := e.DB.Query(ctx, `SELECT id,command,status,exit_code FROM bg_processes WHERE task_id=$1 ORDER BY id DESC LIMIT 5`, t.ID); err == nil {
			var lines []string
			for rows.Next() {
				var id int64
				var cmd, status string
				var code *int
				if rows.Scan(&id, &cmd, &status, &code) != nil {
					continue
				}
				l := fmt.Sprintf("- process #%d `%s`: %s", id, brief(cmd, 100), status)
				if code != nil {
					l += fmt.Sprintf(" (exit %d)", *code)
				}
				lines = append(lines, l)
			}
			rows.Close()
			if len(lines) > 0 {
				sb.WriteString("Background processes started for this task (status 'unknown' = PRISM lost track of it at the restart):\n" + strings.Join(lines, "\n") + "\n")
			}
		}
	}
	if e.Memory != nil {
		var banks []string
		if e.DefaultBanks != nil {
			banks = e.DefaultBanks(ctx, agent)
		}
		if len(banks) == 0 {
			banks = []string{"user", "profile:" + agent}
		}
		rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		facts, err := e.Memory.Find(rctx, memory.FindReq{Query: brief(t.Input, 300), Banks: banks, Agent: agent, K: 6, MinRel: 0.35, NoLinks: true, NoReinforce: true})
		cancel()
		if err == nil && len(facts) > 0 {
			sb.WriteString("What memory knows that may bear on this task:\n")
			for _, b := range memory.BundleFacts(facts) {
				sb.WriteString(b.Line())
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

// restartReview asks the fast model whether the evidence says the task is already done. Best effort: no model, no
// answer, no review.
func (e *Engine) restartReview(ctx context.Context, t tasks.Task, findings string) *restartVerdict {
	if findings == "" || e.LLM == nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var v restartVerdict
	in := "TASK: " + brief(t.Input, 600) + "\n\nEVIDENCE:\n" + brief(findings, 5000)
	if err := e.LLM.CompleteJSON(rctx, "role:fast", restartReviewPrompt, in, &v); err != nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(v.Verdict)) {
	case "finished", "unfinished", "unsure":
		v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
		return &v
	}
	return nil
}

// reconcileNote is the message appended to a restart-requeued task's session.
func (e *Engine) reconcileNote(ctx context.Context, t tasks.Task, agent string, hist []Msg) string {
	f := e.restartFindings(ctx, t, agent, hist)
	var sb strings.Builder
	sb.WriteString(restartNote)
	if f != "" {
		sb.WriteString("\n\n## Findings after the restart\n" + f)
	} else {
		sb.WriteString("\n\n## Findings after the restart\nNothing in the transcript, downloads, processes or memory shows what had already happened — so check before acting.")
	}
	if v := e.restartReview(ctx, t, f); v != nil {
		sb.WriteString(fmt.Sprintf("\n\n## Review of the evidence (a quick automatic judgement — verify it)\nverdict: %s. %s", v.Verdict, strings.TrimSpace(v.Why)))
		if strings.TrimSpace(v.Remaining) != "" && v.Verdict != "finished" {
			sb.WriteString(" Remaining: " + strings.TrimSpace(v.Remaining))
		}
	}
	return sb.String()
}
