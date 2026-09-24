package agent

import (
	"context"
	"fmt"
	"strings"

	"prism/internal/tasks"
	"prism/internal/tasksum"
)

// transcript renders the full step-by-step record of a finished task: every tool call, its result, and the
// reasoning in between. Shared by the task_transcript tool and the automatic task-summary pipeline below —
// calls returns how many tool calls the run actually made, used to skip summarizing tasks with no real work
// (a plain chat reply has nothing a summary would add beyond the task's own title and result).
func (e *Engine) transcript(ctx context.Context, id int64) (t tasks.Task, text string, calls int, tainted bool, err error) {
	t, err = e.Tasks.Get(ctx, id)
	if err != nil {
		return t, "", 0, false, err
	}
	if t.SessionID == nil {
		return t, fmt.Sprintf("Task #%d %s (%s → %s): %s\n(no session transcript recorded)", t.ID, t.Status, t.FromKind, t.ToAgent, brief(t.Input, 400)), 0, false, nil
	}
	msgs, err := e.Sessions.Messages(ctx, *t.SessionID)
	if err != nil {
		return t, "", 0, false, err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Task #%d — %s → %s [%s]\nGoal: %s\n\n", t.ID, t.FromKind, t.ToAgent, t.Status, t.Input)
	for _, m := range msgs {
		tainted = tainted || m.Tainted
		switch {
		case len(m.ToolCalls) > 0:
			for _, tc := range m.ToolCalls {
				calls++
				fmt.Fprintf(&sb, "→ %s(%s)\n", tc.Name, brief(tc.Arguments, 300))
			}
		case m.Role == "tool":
			fmt.Fprintf(&sb, "  = %s\n", brief(m.Content, 500))
		case strings.TrimSpace(m.Content) != "":
			fmt.Fprintf(&sb, "[%s] %s\n", m.Role, brief(m.Content, 500))
		}
	}
	if t.Result != "" {
		fmt.Fprintf(&sb, "\nFinal result: %s\n", brief(t.Result, 800))
	}
	if t.Error != "" {
		fmt.Fprintf(&sb, "\nError: %s\n", brief(t.Error, 400))
	}
	return t, sb.String(), calls, tainted, nil
}

const summaryPrompt = `You distill a finished task into a searchable record of what was tried, so a later task can answer "what did we do about this before, and why?" without re-reading the whole transcript.

Read the transcript: the goal, every tool call and result, corrections along the way, and the final outcome. Answer JSON only, each field plain prose (1-4 sentences, empty string if nothing applies), no markdown:
{"title":"a short, distinctive title (not a restatement of the goal, not \"task 123\")",
 "goal":"what was actually being asked for",
 "decisions":"key choices made and why, if any",
 "attempts":"what was tried, including approaches that did not work and were abandoned",
 "outcome":"what actually happened / was delivered",
 "unfinished":"what was left undone, deferred, or would need to happen next"}`

// SummarizeTask distills one finished task's transcript into a task_summaries row (see internal/tasksum):
// its goal, decisions, attempts, outcome and unfinished work — the "what did we try, and why?" question a
// single atomic memory fact cannot answer. Returns whether a summary was actually stored; false with a nil
// error means it was skipped (no real work in the transcript, or no model configured yet — the latter
// leaves the task pending for a later cycle rather than spending a retry on it).
func (e *Engine) SummarizeTask(ctx context.Context, id int64) (bool, error) {
	t, text, calls, _, err := e.transcript(ctx, id)
	if err != nil {
		return false, err
	}
	if calls == 0 {
		return false, e.Tasks.MarkSummarized(ctx, id)
	}
	if e.LLM.RoleRef(ctx, "fast") == "" {
		return false, nil
	}
	var parsed struct{ Title, Goal, Decisions, Attempts, Outcome, Unfinished string }
	if err := e.LLM.CompleteJSON(ctx, "role:fast", summaryPrompt, text, &parsed); err != nil {
		return false, fmt.Errorf("task summary: %w", err)
	}
	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		title = brief(t.Input, 80)
	}
	if _, err := e.TaskSum.Save(ctx, tasksum.Summary{
		TaskID: t.ID, Agent: t.ToAgent, Title: title, Status: t.Status,
		Goal:       strings.TrimSpace(parsed.Goal),
		Decisions:  strings.TrimSpace(parsed.Decisions),
		Attempts:   strings.TrimSpace(parsed.Attempts),
		Outcome:    strings.TrimSpace(parsed.Outcome),
		Unfinished: strings.TrimSpace(parsed.Unfinished),
	}); err != nil {
		return false, err
	}
	return true, e.Tasks.MarkSummarized(ctx, id)
}

// SummarizeDue processes up to n terminal tasks awaiting a summary (see tasks.Store.NeedsSummary), giving up
// on ones that keep failing rather than retrying them forever (see maxSummaryAttempts). Returns how many
// summaries were actually stored.
func (e *Engine) SummarizeDue(ctx context.Context, n int) (int, error) {
	due, err := e.Tasks.NeedsSummary(ctx, n)
	if err != nil {
		return 0, err
	}
	stored := 0
	var firstErr error
	for _, t := range due {
		ok, err := e.SummarizeTask(ctx, t.ID)
		if err != nil {
			_ = e.Tasks.BumpSummaryAttempts(ctx, t.ID)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok {
			stored++
		}
	}
	return stored, firstErr
}
