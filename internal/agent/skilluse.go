package agent

import (
	"context"
	"fmt"
	"strings"

	"prism/internal/tasks"
	"prism/internal/textutil"
)

// Saved skills are only worth anything if they get used, and agents seldom went looking: the prompt lists names, and a model busy with a task
// does not stop to skill_load. So the skill that fits a task is put in front of the agent with the task (the best one with its procedure),
// and a long successful task that no skill covered is handed to Daedalus to turn into one.
const (
	skillBodyMax      = 3000
	skillAfterSteps   = 6 // a task needing this many model turns was worth writing down
	skillLearnPerDay  = 4
	skillLearnByAgent = 6 // hours between two skill requests for one agent
)

// skillAllow is the selector: an agent with skills pinned to it sees only those; one with none sees the skills nobody has claimed. So each
// agent's skills stay its own, and the prompt and the matching never carry the whole library.
func (e *Engine) skillAllow(ctx context.Context, p *Profile) func(string) bool {
	if len(p.Skills) > 0 {
		mine := map[string]bool{}
		for _, k := range p.Skills {
			mine[strings.ToLower(k)] = true
		}
		return func(n string) bool { return mine[strings.ToLower(n)] }
	}
	owned := map[string]bool{}
	if all, err := e.Profiles.List(ctx); err == nil {
		for _, o := range all {
			for _, k := range o.Skills {
				owned[strings.ToLower(k)] = true
			}
		}
	}
	return func(n string) bool { return !owned[strings.ToLower(n)] }
}

// skillNote returns the note appended to a task's input: the procedure of the best-fitting saved skill, plus the names of others that fit.
// It also says whether any skill matched, so a task that already had one is not offered to Daedalus as new ground.
func (e *Engine) skillNote(ctx context.Context, p *Profile, input string, taskID int64) (string, bool) {
	if e.Skills == nil || p.Name == "Daedalus" {
		return "", false
	}
	ms := e.Skills.Match(ctx, input, 3, e.skillAllow(ctx, p))
	if len(ms) == 0 {
		return "", false
	}
	var sb strings.Builder
	sb.WriteString("\n\n[Saved skill] A procedure that worked before fits this task. Follow it unless it clearly does not apply, and say so in your answer if it did not work.\n")
	best := ms[0]
	fmt.Fprintf(&sb, "# Skill: %s — %s\n%s\n", best.Name, textutil.Brief(best.Description, 160), textutil.Cut(best.Body, skillBodyMax))
	e.Skills.NoteUse(ctx, best.Name, p.Name, taskID, "injected")
	if len(ms) > 1 {
		sb.WriteString("Others that may help (skill_load to read): ")
		for i, m := range ms[1:] {
			if i > 0 {
				sb.WriteString("; ")
			}
			sb.WriteString(m.Name)
		}
		sb.WriteString("\n")
	}
	return sb.String(), true
}

// maybeLearnSkill asks Daedalus whether a long, successful task that no saved skill covered is a repeatable procedure worth writing down.
// At most a few a day, and one per agent every few hours; Daedalus does nothing when it was a one-off.
func (e *Engine) maybeLearnSkill(ctx context.Context, t tasks.Task, p *Profile, res *RunResult, covered bool) {
	if e.Skills == nil || covered || p.System || res == nil || res.Aborted != "" || res.NeedsInput != "" || res.Iterations < skillAfterSteps {
		return
	}
	if _, declined := notCapable(res.Text); declined {
		return
	}
	var perDay, perAgent int
	_ = e.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE title LIKE '%'||$1||'%' AND created_at>now()-make_interval(hours=>$2))
		FROM tasks WHERE to_agent='Daedalus' AND title LIKE 'Skill from task%' AND created_at>now()-interval '1 day'`, "("+p.Name+")", skillLearnByAgent).Scan(&perDay, &perAgent)
	if perDay >= skillLearnPerDay || perAgent > 0 {
		return
	}
	title := fmt.Sprintf("Skill from task #%d (%s)", t.ID, p.Name)
	input := fmt.Sprintf("Task #%d for agent %s finished successfully after %d steps (%q). Read it with task_transcript(id=%d). If it is a repeatable procedure that skill_search shows is not already covered, write a skill for it with skill_write (extend an existing one instead of forking it). If it was a one-off, write nothing and say so in one line. When you write one, pass agents=[%q] (add another agent only if the procedure clearly fits it too) so the skill is offered to that agent and not to everyone.",
		t.ID, p.Name, res.Iterations, textutil.Brief(t.Title, 80), t.ID, p.Name)
	_, _ = e.Enqueue(ctx, tasks.Task{FromKind: "system", FromName: "skills", ToAgent: "Daedalus", Title: title, Input: input})
}

// skillIndex is the skill list in an agent's prompt: its pinned skills, or the unclaimed ones when it has none pinned.
func (e *Engine) skillIndex(ctx context.Context, p *Profile) []SkillSummary {
	if len(p.Skills) > 0 {
		return e.Skills.Summaries(ctx, p.Skills)
	}
	allow := e.skillAllow(ctx, p)
	var out []SkillSummary
	for _, s := range e.Skills.Summaries(ctx, nil) {
		if allow(s.Name) {
			out = append(out, s)
		}
	}
	return out
}
