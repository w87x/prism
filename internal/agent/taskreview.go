package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"prism/internal/llm"
	"prism/internal/tasks"
)

// A task that ends "partial" (iteration budget exhausted, or stopped by the loop guard) is not a real
// answer, and nobody is necessarily watching. ReviewTask reads what the run actually did and proposes what
// to do about it; ResolveTask carries out the option the user picks. Both work without a model — the
// options then fall back to a fixed, always-valid set.
type ReviewOption struct {
	Action string `json:"action"` // continue | split | evolve | hire | dismiss
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

type TaskReview struct {
	TaskID  int64          `json:"task_id"`
	Cause   string         `json:"cause"` // budget | loop | error | other
	Summary string         `json:"summary"`
	Options []ReviewOption `json:"options"`
}

var reviewActions = map[string]string{
	"continue": "Continue from where it stopped",
	"split":    "Split it into smaller tasks",
	"evolve":   "Ask Metis to improve this agent",
	"hire":     "Have Forge design a better-suited agent",
	"dismiss":  "Dismiss (nothing more to do)",
}

const taskReviewPrompt = `You review a task an AI agent did not finish. You get the task, how it ended and the tail of its transcript.
Explain in 2-4 plain sentences what it accomplished, and WHY it stopped (used its whole iteration budget on an over-large job? repeated the same failing call? kept getting the same tool error? lacked a tool?).
Then propose 2-4 concrete options, most useful first, chosen ONLY from these actions:
- "continue": run it again from what was already achieved (best when real progress was made)
- "split": break the job into smaller tasks (best when it was simply too big)
- "evolve": ask Metis to improve the agent's soul/tools (best when the agent kept making the same mistake)
- "hire": have Forge design a better-suited agent (best when no existing agent has the right skills/tools)
- "dismiss": nothing more is needed
Give each a short "label" phrased for the user and a one-sentence "detail".
Answer JSON only: {"cause":"budget|loop|error|other","summary":"...","options":[{"action":"continue","label":"...","detail":"..."}]}`

func (e *Engine) ReviewTask(ctx context.Context, id int64) (*TaskReview, error) {
	t, err := e.Tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Status != tasks.Partial && t.Status != tasks.Failed {
		return nil, fmt.Errorf("task #%d is %s — only partial or failed tasks are reviewed", t.ID, t.Status)
	}
	rv := &TaskReview{TaskID: t.ID, Cause: causeOf(t)}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Agent: %s\nTask: %s\nInput: %s\nEnded: %s (%s)\n\nTRANSCRIPT (tail):\n", t.ToAgent, t.Title, brief(t.Input, 600), t.Status, t.Error)
	if t.SessionID != nil {
		if ms, merr := e.Sessions.Messages(ctx, *t.SessionID); merr == nil {
			if len(ms) > 24 {
				ms = ms[len(ms)-24:]
			}
			for _, m := range ms {
				line := m.Content
				if len(m.ToolCalls) > 0 {
					b, _ := json.Marshal(m.ToolCalls)
					line += " [calls " + string(b) + "]"
				}
				fmt.Fprintf(&sb, "%s: %s\n", m.Role, brief(line, 500))
			}
		}
	}
	if e.LLM != nil && e.LLM.RoleRef(ctx, "fast") != "" {
		if out, lerr := e.LLM.Complete(ctx, "role:fast", taskReviewPrompt, sb.String(), true); lerr == nil {
			var p struct {
				Cause   string         `json:"cause"`
				Summary string         `json:"summary"`
				Options []ReviewOption `json:"options"`
			}
			if json.Unmarshal([]byte(llm.ExtractJSON(out)), &p) == nil {
				rv.Summary = strings.TrimSpace(p.Summary)
				if p.Cause != "" {
					rv.Cause = p.Cause
				}
				for _, o := range p.Options {
					if _, ok := reviewActions[o.Action]; ok && strings.TrimSpace(o.Label) != "" {
						rv.Options = append(rv.Options, o)
					}
				}
			}
		}
	}
	if rv.Summary == "" {
		rv.Summary = "The run ended early (" + t.Error + ")."
	}
	// whatever the model said, the user can always continue or dismiss
	have := map[string]bool{}
	for _, o := range rv.Options {
		have[o.Action] = true
	}
	for _, a := range []string{"continue", "dismiss"} {
		if !have[a] {
			rv.Options = append(rv.Options, ReviewOption{Action: a, Label: reviewActions[a]})
		}
	}
	return rv, nil
}

func causeOf(t tasks.Task) string {
	switch {
	case strings.Contains(t.Error, "budget"):
		return "budget"
	case strings.Contains(t.Error, "loop"):
		return "loop"
	case t.Status == tasks.Failed:
		return "error"
	}
	return "other"
}

// ResolveTask carries out a review option. Every path acknowledges the task so it leaves "needs attention".
// note is optional extra guidance from the user, passed on to whoever picks the work up.
func (e *Engine) ResolveTask(ctx context.Context, id int64, action, note string) (*tasks.Task, error) {
	t, err := e.Tasks.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, ok := reviewActions[action]; !ok {
		return nil, fmt.Errorf("unknown action %q", action)
	}
	ctxNote := ""
	if n := strings.TrimSpace(note); n != "" {
		ctxNote = "\n\nGuidance from the user: " + n
	}
	var next *tasks.Task
	enqueue := func(agent, title, input string) error {
		nt, err := e.Enqueue(ctx, tasks.Task{FromKind: "user", FromName: "user", ToAgent: agent, Title: title, Input: input})
		if err == nil {
			next = &nt
		}
		return err
	}
	switch action {
	case "continue":
		err = enqueue(t.ToAgent, t.Title, fmt.Sprintf("%s\n\n(Earlier attempt, task #%d, stopped early: %s. Its partial result:\n%s\nContinue from there instead of starting over, and keep it efficient.)%s",
			t.Input, t.ID, t.Error, brief(t.Result, 1500), ctxNote))
	case "split":
		err = enqueue("Atlas", "Split: "+t.Title, fmt.Sprintf("Task #%d for %s stopped early (%s) because it was too large. Break the job into smaller, independent steps and run them (delegate each to a suitable agent), then combine the results. Original request:\n%s\n\nPartial result so far:\n%s%s",
			t.ID, t.ToAgent, t.Error, t.Input, brief(t.Result, 1500), ctxNote))
	case "evolve":
		err = enqueue("Metis", "Improve "+t.ToAgent, fmt.Sprintf("Task #%d for agent %s stopped early (%s). Read it with task_transcript(id=%d), work out what the agent kept getting wrong or lacked, and propose a soul/tool improvement with evolve_propose only if the evidence is clear.%s",
			t.ID, t.ToAgent, t.Error, t.ID, ctxNote))
	case "hire":
		err = enqueue("Forge", "Better agent for: "+t.Title, fmt.Sprintf("Task #%d (%s) could not be finished by %s (%s). Design an agent better suited to this kind of work if a real gap exists; otherwise say why not.%s",
			t.ID, brief(t.Input, 400), t.ToAgent, t.Error, ctxNote))
	}
	if err != nil {
		return nil, err
	}
	if aerr := e.Tasks.Acknowledge(ctx, id); aerr != nil && !errors.Is(aerr, context.Canceled) {
		return next, aerr
	}
	return next, nil
}
