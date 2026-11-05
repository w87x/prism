package agent

import (
	"context"
	"fmt"
	"strings"

	"prism/internal/textutil"
)

// Acceptance criteria. A lead who hands out work says what "done" means, and checks the report against it before passing it
// on. A delegator may give delegate/ask_colleague a done_when; when the specialist finishes, the main model compares the result
// with it. A result that falls short goes back to the specialist once, with the gap named; if it still falls short the parent
// is told so in the result itself rather than receiving a confident-looking answer.

const acceptancePrompt = `You are the team lead checking a colleague's report against what "done" was agreed to mean.
Judge ONLY against the acceptance criteria, using the report and the list of what the colleague actually did. A claim the trail does not support (it says it saved or sent something but no such call appears) does not count as done.
Answer JSON only: {"met":true|false,"gap":"what is missing or unsupported, in one or two sentences (empty when met)"}`

const defaultDelegationCriteria = "Complete the requested work in the instruction; report each requested outcome and clearly identify anything unfinished or unsupported."

type acceptanceVerdict struct {
	Met *bool  `json:"met"`
	Gap string `json:"gap"`
}

func (e *Engine) judgeAcceptance(ctx context.Context, criteria, instruction, text string, taskID int64, session *int64) (met bool, gap string, ok bool) {
	if e.LLM == nil || e.LLM.RoleRef(ctx, "chat") == "" {
		return true, "", false
	}
	trail := ""
	if session != nil {
		trail = e.toolTrail(ctx, *session)
	}
	if taskID != 0 && e.DB != nil {
		rows, err := e.DB.Query(ctx, `WITH RECURSIVE descendants AS (SELECT id,to_agent,session_id FROM tasks WHERE parent_id=$1 UNION ALL SELECT t.id,t.to_agent,t.session_id FROM tasks t JOIN descendants d ON t.parent_id=d.id) SELECT to_agent,session_id FROM descendants WHERE session_id IS NOT NULL ORDER BY id LIMIT 40`, taskID)
		if err == nil {
			type descendant struct {
				agent   string
				session int64
			}
			var ds []descendant
			for rows.Next() {
				var d descendant
				if rows.Scan(&d.agent, &d.session) == nil {
					ds = append(ds, d)
				}
			}
			rows.Close()
			for _, d := range ds {
				trail += "\n[" + d.agent + "]\n" + e.toolTrail(ctx, d.session)
				if len(trail) > 9000 {
					break
				}
			}
		}
	}
	var v acceptanceVerdict
	cctx, cancel := context.WithTimeout(ctx, llmCallTimeout)
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
	meta := e.Tasks.MetaOf(ctx, t.ID)
	crit := meta.Criteria
	checksOK := true
	if t.SessionID != nil {
		checks := []func(context.Context, int64, string) (string, error){e.missingSavedArtifact, e.missingCreatedCron}
		for _, check := range checks {
			missing, err := check(ctx, *t.SessionID, res.Text)
			if err != nil {
				checksOK = false
				continue
			}
			if missing != "" {
				e.Tasks.SetCriteriaResult(ctx, t.ID, false, missing)
				res.Text = strings.TrimSpace(res.Text) + "\n\n[Acceptance check: NOT met — " + missing + ".]"
				return res
			}
		}
	}
	if crit == "" {
		// A resumed task may have had a prior deterministic failure. Clear it
		// once the check no longer finds a missing resource.
		if checksOK && (meta.CriteriaMet != nil || meta.Gap != "") {
			e.Tasks.SetCriteriaUnknown(ctx, t.ID, "")
		}
		return res
	}
	met, gap, ran := e.judgeAcceptance(ctx, crit, t.Input, res.Text, t.ID, t.SessionID)
	if !ran {
		e.Tasks.SetCriteriaUnknown(ctx, t.ID, "Acceptance check could not run or returned no verdict.")
		res.Text = strings.TrimSpace(res.Text) + "\n\n[Acceptance check unavailable; completion has not been independently checked.]"
		return res
	}
	if !met && !strings.HasPrefix(crit, defaultDelegationCriteria) {
		e.Emit("task.criteria", map[string]any{"task": t.ID, "met": false, "gap": gap})
		retry := spec
		retry.Input = fmt.Sprintf("[Lead's check] Your report does not meet the acceptance criteria.\nCriteria: %s\nWhat is missing: %s\nDo what is missing now, then give the complete final report.", crit, orNone(gap))
		if again, err := e.Run(ctx, retry); err == nil && again != nil && again.Aborted == "" && again.NeedsInput == "" && strings.TrimSpace(again.Text) != "" {
			res = again
			met, gap, ran = e.judgeAcceptance(ctx, crit, t.Input, res.Text, t.ID, t.SessionID)
			if !ran {
				e.Tasks.SetCriteriaUnknown(ctx, t.ID, "Acceptance re-check could not run or returned no verdict.")
				res.Text = strings.TrimSpace(res.Text) + "\n\n[Acceptance re-check unavailable; completion has not been independently checked.]"
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
