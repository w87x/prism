package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/textmatch"
	"prism/internal/textutil"
)

// When a run ends because it used its whole iteration budget (or the loop guard cut it off), simply marking
// the task partial and waiting for a human wastes the work done so far and leaves a delegating parent with
// nothing useful. The engine first analyses the run — what was achieved, why it stalled — and rewrites the
// instruction into a tighter one that carries the results so far and steers around the dead end, then runs
// it once more in a fresh session (the old transcript is what filled the context). If that also fails, a
// delegated task ends as failed with the analysis, so the parent agent can decide and tell the user; a
// top-level task stays partial for the user's review.
const recoverPrompt = `An AI agent ran out of its iteration budget (or was stopped for repeating itself) before finishing a task. You get the task, how it ended and the tail of its transcript.
Decide whether ONE more attempt with a better instruction can plausibly finish it ("retry"), or whether it cannot ("fail": it lacks a tool or access, the job is impossible or far too large for one agent, or the same blocker will recur).
For "retry", write "prompt": a complete replacement instruction for a fresh agent that (1) restates the goal, (2) states concisely everything already achieved and found, so nothing is redone, (3) names what failed or looped and what to do instead, (4) asks for a focused plan of at most 8 tool calls and a final answer that reports plainly what could not be done rather than continuing forever.
Always give "lesson" (one or two sentences: why it stalled) and "achieved" (what was accomplished, one short paragraph).
Always give "pitfall": ONE imperative sentence (at most 40 words) the same agent can follow the next time it meets a similar task, in the form "When <kind of task>, do not <what it looped on>; instead <what to do, or stop and report what is missing>". It is stored as a lesson and shown to the agent when a similar task starts, so it must be general enough to match a similar task and specific enough to act on.
Also give "triggers": two to four lower-case words that would appear in a similar future request (they decide when the pitfall is shown again).
Answer JSON only: {"decision":"retry|fail","lesson":"...","achieved":"...","pitfall":"...","triggers":["..."],"prompt":"..."}`

type recovery struct {
	Decision string   `json:"decision"`
	Lesson   string   `json:"lesson"`
	Achieved string   `json:"achieved"`
	Pitfall  string   `json:"pitfall"`
	Triggers []string `json:"triggers"`
	Prompt   string   `json:"prompt"`
	Retried  bool     `json:"-"`
}

func (e *Engine) autoRetryOn(ctx context.Context) bool {
	return !settings.Load(ctx, e.Settings, settings.KeyGuardrails, settings.DefaultGuardrails()).AutoRetryOff
}

func (e *Engine) planRecovery(ctx context.Context, t tasks.Task, sess *Session, res *RunResult) recovery {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Agent: %s\nTask: %s\nInstruction: %s\nEnded: %s\n\nLAST WORDS OF THE AGENT:\n%s\n\nTRANSCRIPT (tail):\n", t.ToAgent, t.Title, textutil.Brief(t.Input, 800), res.Aborted, textutil.Brief(res.Text, 800))
	if ms, err := e.Sessions.Messages(ctx, sess.ID); err == nil {
		if len(ms) > 20 {
			ms = ms[len(ms)-20:]
		}
		for _, m := range ms {
			line := m.Content
			for _, c := range m.ToolCalls {
				line += " [call " + c.Name + " " + textutil.Brief(string(c.Arguments), 120) + "]"
			}
			fmt.Fprintf(&sb, "%s: %s\n", m.Role, textutil.Brief(line, 400))
		}
	}
	plan := recovery{Decision: "retry", Lesson: "It used its whole budget (" + res.Aborted + ") before finishing.", Achieved: textutil.Brief(res.Text, 600),
		Pitfall: fmt.Sprintf("A task like “%s” stalled (%s): if you find yourself repeating calls without new information, stop and report what is blocking you instead.", textutil.Brief(t.Title, 80), res.Aborted)}
	fallback := fmt.Sprintf("%s\n\n(An earlier attempt stopped early: %s. What it had reached:\n%s\nContinue from there without redoing it, take the shortest path, and if something blocks you, say exactly what instead of retrying it again and again.)", t.Input, res.Aborted, textutil.Brief(res.Text, 1500))
	if e.LLM == nil || e.LLM.RoleRef(ctx, "fast") == "" {
		plan.Prompt = fallback
		return plan
	}
	var p recovery
	if err := e.LLM.CompleteJSON(ctx, e.LLM.ActionRef(ctx, "recover", "fast"), recoverPrompt, sb.String(), &p); err != nil {
		plan.Prompt = fallback
		return plan
	}
	if strings.TrimSpace(p.Lesson) != "" {
		plan.Lesson = strings.TrimSpace(p.Lesson)
	}
	if strings.TrimSpace(p.Achieved) != "" {
		plan.Achieved = strings.TrimSpace(p.Achieved)
	}
	if pf := strings.TrimSpace(p.Pitfall); len(pf) > 20 {
		plan.Pitfall = pf
		plan.Triggers = p.Triggers
	}
	if strings.EqualFold(strings.TrimSpace(p.Decision), "fail") {
		plan.Decision = "fail"
		return plan
	}
	plan.Prompt = strings.TrimSpace(p.Prompt)
	if plan.Prompt == "" {
		plan.Prompt = fallback
	}
	return plan
}

// recoverRun analyses an aborted run and, when a retry looks worthwhile, runs it once more in a fresh
// session with the rewritten instruction. It returns the result to record and the analysis.
func (e *Engine) recoverRun(ctx context.Context, t tasks.Task, p *Profile, sess *Session, res *RunResult, spec RunSpec) (*RunResult, recovery) {
	plan := e.planRecovery(ctx, t, sess, res)
	e.rememberPitfall(ctx, t, p, plan)
	note := "[system] Automatic recovery: " + plan.Lesson
	if plan.Decision == "fail" {
		note += " Not retried."
	} else {
		note += " Retrying once in a fresh session with a rewritten instruction."
	}
	_, _ = e.Sessions.Append(ctx, sess.ID, Msg{Message: llm.Message{Role: "user", Content: note}, Provenance: "system"})
	if plan.Decision == "fail" {
		return res, plan
	}
	ns, err := e.Sessions.Create(ctx, p.Name, "task", "", t.ID)
	if err != nil {
		return res, plan
	}
	_ = e.Tasks.SetSession(ctx, t.ID, ns.ID)
	spec.Session, spec.Input, spec.Provenance = ns, plan.Prompt, "system"
	spec.Tainted = spec.Tainted || res.Tainted
	res2, err := e.Run(ctx, spec)
	if err != nil || res2 == nil {
		return res, plan
	}
	plan.Retried = true
	return res2, plan
}

// learnFromStall turns an aborted run into improvement work, so the same stall is less likely next time: when the
// automatic retry also failed, Metis reviews the agent (soul/tools); when the retry succeeded, Daedalus is asked
// whether the way it finally worked deserves a reusable skill. System agents are skipped (no loops of maintainers
// improving maintainers) and each agent is reviewed at most once a day.
func (e *Engine) learnFromStall(ctx context.Context, t tasks.Task, p *Profile, res *RunResult, plan recovery) {
	if p.System || plan.Decision == "" {
		return
	}
	worker, title, input := "Metis", "Improve "+p.Name, ""
	switch {
	case res.Aborted != "":
		input = fmt.Sprintf("Task #%d for agent %s stopped early (%s)%s. Analysis: %s Read it with task_transcript(id=%d), work out what the agent kept getting wrong or lacked, and propose a soul/tool improvement with evolve_propose only if the evidence is clear.", t.ID, p.Name, res.Aborted, map[bool]string{true: " even after an automatic retry"}[plan.Retried], plan.Lesson, t.ID)
	case plan.Retried:
		worker, title = "Daedalus", "Skill from recovered task #"+fmt.Sprint(t.ID)
		input = fmt.Sprintf("Task #%d for agent %s first ran out of budget (%s) and then succeeded on a retry with a rewritten instruction. Read it with task_transcript(id=%d); if the approach that worked is a repeatable procedure, distil it into a skill for %s, otherwise do nothing. Why it stalled: %s", t.ID, p.Name, plan.Lesson, t.ID, p.Name, plan.Lesson)
	default:
		return
	}
	var n int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE to_agent=$1 AND title=$2 AND created_at>now()-interval '1 day'`, worker, title).Scan(&n); err != nil || n > 0 {
		return
	}
	_, _ = e.Enqueue(ctx, tasks.Task{FromKind: "system", FromName: "recovery", ToAgent: worker, Title: title, Input: input})
}

// rememberPitfall keeps what a stall taught as a lesson (immune memory): one store, shown with the task only when its trigger fires, so it
// costs no context otherwise. (It used to be written a second time as a "Pitfall:" memory fact; two copies of one lesson drifted apart.)
// Maintenance agents are skipped (no maintainers learning from maintainers). A lesson the model gave no triggers for gets the telling
// words of the task it came from, so it never turns into an always-on line.
func (e *Engine) rememberPitfall(ctx context.Context, t tasks.Task, p *Profile, plan recovery) {
	if p.System || strings.TrimSpace(plan.Pitfall) == "" {
		return
	}
	trig := plan.Triggers
	if len(trig) == 0 {
		trig = telltaleWords(t.Title+" "+t.Input, 4)
	}
	if len(trig) == 0 {
		return
	}
	_ = e.AddAntibody(ctx, p.Name, trig, strings.TrimSpace(plan.Pitfall), "a stalled task (#"+fmt.Sprint(t.ID)+")")
}

// telltaleWords are the longest distinct words of a text: what a later, similar request is likely to contain too.
func telltaleWords(text string, n int) []string {
	seen := map[string]bool{}
	var ws []string
	for _, w := range textmatch.Tokens(text) {
		if len([]rune(w)) >= 5 && !seen[w] {
			seen[w] = true
			ws = append(ws, w)
		}
	}
	sort.SliceStable(ws, func(i, j int) bool { return len(ws[i]) > len(ws[j]) })
	if len(ws) > n {
		ws = ws[:n]
	}
	return ws
}
