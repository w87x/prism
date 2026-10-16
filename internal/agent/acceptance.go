package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"prism/internal/textutil"
)

// Acceptance criteria. A lead who hands out work says what "done" means, and checks the report against it before passing it
// on. A delegator may give delegate/ask_colleague a done_when; when the specialist finishes, the main model compares the result
// with it. A result that falls short goes back to the specialist once, with the gap named; if it still falls short the parent
// is told so in the result itself rather than receiving a confident-looking answer.

const acceptancePrompt = `You are the team lead checking a colleague's report against what "done" was agreed to mean.
Judge ONLY against the acceptance criteria, using the report and the list of what the colleague actually did. A claim the trail does not support (it says it saved or sent something but no such call appears) does not count as done.
Answer JSON only: {"met":true|false,"gap":"what is missing or unsupported, in one or two sentences (empty when met)"}`

type acceptanceVerdict struct {
	Met *bool  `json:"met"`
	Gap string `json:"gap"`
}

func (e *Engine) judgeAcceptance(ctx context.Context, criteria, instruction, text string, session *int64) (met bool, gap string, ok bool) {
	if e.LLM == nil || e.LLM.RoleRef(ctx, "chat") == "" {
		return true, "", false
	}
	trail := ""
	if session != nil {
		trail = e.toolTrail(ctx, *session)
	}
	var v acceptanceVerdict
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	in := fmt.Sprintf("ACCEPTANCE CRITERIA:\n%s\n\nTHE REQUEST:\n%s\n\nWHAT THE COLLEAGUE DID (tool calls):\n%s\n\nTHE REPORT:\n%s", criteria, textutil.Brief(instruction, 800), orNone(trail), textutil.Brief(text, 3500))
	if err := e.LLM.CompleteJSON(cctx, e.LLM.ActionRef(cctx, "acceptance", "chat"), acceptancePrompt, in, &v); err != nil || v.Met == nil {
		return true, "", false // a check that cannot run never blocks the work
	}
	return *v.Met, strings.TrimSpace(v.Gap), true
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

// checkAcceptance applies the task's criteria, if it has any, to a finished run. It returns the (possibly improved) result.
func (e *Engine) checkAcceptance(ctx context.Context, spec RunSpec, res *RunResult) *RunResult {
	t := spec.Task
	if t == nil || res == nil || res.NeedsInput != "" || res.Aborted != "" || res.Sleep != nil {
		return res
	}
	if _, declined := notCapable(res.Text); declined {
		return res
	}
	crit := e.Tasks.MetaOf(ctx, t.ID).Criteria
	if crit == "" {
		return res
	}
	met, gap, ran := e.judgeAcceptance(ctx, crit, t.Input, res.Text, t.SessionID)
	if !ran {
		return res
	}
	if !met {
		e.Emit("task.criteria", map[string]any{"task": t.ID, "met": false, "gap": gap})
		retry := spec
		retry.Input = fmt.Sprintf("[Lead's check] Your report does not meet the acceptance criteria.\nCriteria: %s\nWhat is missing: %s\nDo what is missing now, then give the complete final report.", crit, orNone(gap))
		if again, err := e.Run(ctx, retry); err == nil && again != nil && again.Aborted == "" && again.NeedsInput == "" && strings.TrimSpace(again.Text) != "" {
			res = again
			met, gap, ran = e.judgeAcceptance(ctx, crit, t.Input, res.Text, t.SessionID)
			if !ran {
				return res
			}
		}
	}
	e.Tasks.SetCriteriaResult(ctx, t.ID, met, gap)
	if !met {
		res.Text = strings.TrimSpace(res.Text) + fmt.Sprintf("\n\n[Acceptance check: NOT met — %s. Do not report this as fully done.]", orNone(gap))
	}
	return res
}
