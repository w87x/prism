package agent

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"prism/internal/tasks"
	"prism/internal/textutil"
)

// Saved skills are only worth anything if they get used, and agents seldom went looking: the prompt lists names, and a model busy with a task
// does not stop to skill_load. So the skill that fits a task is put in front of the agent with the task (the best one with its procedure),
// and a long successful task that no skill covered is handed to Daedalus to turn into one.
const (
	skillBodyMax      = 2200
	skillAfterSteps   = 6 // a task needing this many model turns was worth writing down
	skillMaxTokens    = 500000
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
	// skills are the procedures of the specialists; the staff (briefings, memory, skills, evolution) and the entry agent, which delegates,
	// have their own jobs — a skill about Avito listings was being put in front of Oneiros's dream and Atlas's bookmarking
	if e.Skills == nil || p.Name == "Daedalus" || p.Role == RoleMaint || p.Role == RoleEntry {
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
	// a run that burned through the budget (a runaway search loop) is no model of how to do the job
	if res.TokensIn > skillMaxTokens || res.Extensions > 0 {
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

// tidySkills merges skills that teach the same procedure, at most once a week and only when new skills have appeared since.
func (e *Engine) tidySkills(ctx context.Context) {
	if e.Skills == nil || e.LLMDown() {
		return
	}
	var last struct{ At time.Time }
	_, _ = e.Settings.Get(ctx, "skill_tidy", &last)
	if time.Since(last.At) < 7*24*time.Hour {
		return
	}
	var fresh int
	_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM skills WHERE enabled AND created_at > $1`, last.At).Scan(&fresh)
	if fresh == 0 && !last.At.IsZero() {
		return
	}
	last.At = time.Now()
	_ = e.Settings.Set(ctx, "skill_tidy", last)
	if msg, err := e.Skills.Tidy(ctx); err == nil && !strings.Contains(msg, "nothing to merge") {
		e.NotifyHousekeeping(ctx, "info", "Skills merged", Notice{Agent: "Daedalus", Level: "info", Text: msg}, false)
	}
}

// ── recurring work ──
//
// A skill written from one long task misses the jobs done many times in short runs: "search Jackett for a show, queue the torrent on
// Download Station" runs ten times a week in five steps each, and no single run is worth a skill. So once a day the finished tasks of the
// last weeks are grouped by what they were for (embedding of the goal), and a group of three or more, on at least two days, that no skill
// covers is handed to Daedalus once, with the task ids, to write one skill and pin it to the agents that did the work.
const (
	recurringDays       = 45
	recurringMinSize    = 3
	recurringMinSim     = 0.65 // measured on real task goals: the same job reads 0.65+, a different job on the same tools less
	recurringPerRun     = 2
	recurringCoveredSim = 0.62 // a skill this alike to the job covers it
	recurringOfferKey   = "skill_recurring"
)

type recurringState struct {
	At      time.Time `json:"at"`
	Offered []int64   `json:"offered"` // task ids already handed to Daedalus
}

type goalRow struct {
	task  int64
	agent string
	goal  string
	day   string
	vec   []float32
}

// learnRecurringSkills finds recurring work no skill covers. It reports how many requests it sent to Daedalus.
func (e *Engine) learnRecurringSkills(ctx context.Context) int {
	if e.Skills == nil || e.LLM == nil || e.LLMDown() || !e.LLM.HasEmbedding(ctx) {
		return 0
	}
	var st recurringState
	_, _ = e.Settings.Get(ctx, recurringOfferKey, &st)
	if time.Since(st.At) < 24*time.Hour {
		return 0
	}
	st.At = time.Now()
	_ = e.Settings.Set(ctx, recurringOfferKey, st)

	rows, err := e.DB.Query(ctx, `SELECT s.task_id, s.agent, s.goal, to_char(s.created_at,'YYYY-MM-DD') FROM task_summaries s
		JOIN agent_profiles p ON p.name=s.agent AND p.role='worker'
		WHERE s.status='done' AND btrim(s.goal)<>'' AND s.created_at > now()-make_interval(days=>$1) ORDER BY s.id DESC LIMIT 300`, recurringDays)
	if err != nil {
		return 0
	}
	var gs []goalRow
	for rows.Next() {
		var g goalRow
		if rows.Scan(&g.task, &g.agent, &g.goal, &g.day) == nil {
			gs = append(gs, g)
		}
	}
	rows.Close()
	if len(gs) < recurringMinSize {
		return 0
	}
	ref := e.LLM.RoleRef(ctx, "embedding")
	for i := 0; i < len(gs); i += 32 {
		end := min(i+32, len(gs))
		in := make([]string, 0, end-i)
		for _, g := range gs[i:end] {
			in = append(in, textutil.Cut(g.goal, 500))
		}
		vs, err := e.LLM.Embed(ctx, ref, in)
		if err != nil || len(vs) != len(in) {
			return 0
		}
		for j, v := range vs {
			n := float32(0)
			for _, x := range v {
				n += x * x
			}
			n = float32(math.Sqrt(float64(n)))
			if n > 0 {
				for k := range v {
					v[k] /= n
				}
			}
			gs[i+j].vec = v
		}
	}
	dot := func(a, b []float32) float64 {
		if len(a) != len(b) {
			return -1
		}
		var s float64
		for i := range a {
			s += float64(a[i]) * float64(b[i])
		}
		return s
	}
	var clusters [][]int
	for i := range gs {
		placed := false
		for ci, c := range clusters {
			if dot(gs[i].vec, gs[c[0]].vec) >= recurringMinSim {
				clusters[ci] = append(c, i)
				placed = true
				break
			}
		}
		if !placed {
			clusters = append(clusters, []int{i})
		}
	}
	sort.SliceStable(clusters, func(a, b int) bool { return len(clusters[a]) > len(clusters[b]) })
	offered := map[int64]bool{}
	for _, id := range st.Offered {
		offered[id] = true
	}
	sent := 0
	for _, c := range clusters {
		if len(c) < recurringMinSize || sent >= recurringPerRun {
			continue
		}
		days, agents := map[string]bool{}, map[string]int{}
		seen := false
		for _, i := range c {
			days[gs[i].day] = true
			agents[gs[i].agent]++
			seen = seen || offered[gs[i].task]
		}
		if len(days) < 2 || seen {
			continue
		}
		rep := gs[c[0]]
		// "covers" is stricter than "worth showing": a skill about polling downloads does not cover searching for and queueing one
		if ms := e.Skills.Match(ctx, rep.goal, 1, nil); len(ms) > 0 && (ms[0].Score == 0 || ms[0].Score >= recurringCoveredSim) {
			for _, i := range c {
				st.Offered = append(st.Offered, gs[i].task)
			}
			continue
		}
		var who []string
		for a := range agents {
			who = append(who, a)
		}
		sort.Slice(who, func(i, j int) bool { return agents[who[i]] > agents[who[j]] })
		if len(who) > 4 {
			who = who[:4]
		}
		var sb strings.Builder
		for n, i := range c {
			if n >= 8 {
				break
			}
			fmt.Fprintf(&sb, "- task #%d (%s, %s): %s\n", gs[i].task, gs[i].agent, gs[i].day, textutil.Brief(gs[i].goal, 160))
		}
		input := fmt.Sprintf("%d finished tasks over %d days look like ONE recurring job that nobody has written down, and no skill covers it:\n%s\nRead two or three of them with task_transcript(id) — prefer the cleanest runs — and work out the procedure that is the same each time: the tools in order, the arguments that mattered, the checks, the gotchas that cost time. If they are really different jobs, or nothing in them is repeatable (a plain lookup), write nothing and say so in one line. Otherwise write ONE skill for the job with skill_write, generalised (no specific title, URL or id), and pass agents=%q so it is offered to the agents that do this work.",
			len(c), len(days), sb.String(), who)
		title := fmt.Sprintf("Skill from task #%d (recurring: %s)", rep.task, strings.Join(who, ", "))
		if _, err := e.Enqueue(ctx, tasks.Task{FromKind: "system", FromName: "skills", ToAgent: "Daedalus", Title: title, Input: input}); err == nil {
			sent++
			for _, i := range c {
				st.Offered = append(st.Offered, gs[i].task)
			}
		}
	}
	if len(st.Offered) > 600 {
		st.Offered = st.Offered[len(st.Offered)-600:]
	}
	_ = e.Settings.Set(ctx, recurringOfferKey, st)
	return sent
}
