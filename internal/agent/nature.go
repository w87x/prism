package agent

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"prism/internal/tasks"
	"prism/internal/textmatch"
	"prism/internal/textutil"
	"prism/internal/tools"
)

// What a team can borrow from animals.
//
//	pain reflex    a tool that keeps failing is cut off for a while, for everyone, instead of being retried by every agent
//	ant trails     what worked leaves a trace that fades; routing prefers agents whose traces are fresh
//	immune memory  a short lesson with a trigger, shown only when the trigger shows up again, expiring when it never does
//	scouting       before a risky or ambiguous job, a cheap read-only look is taken and handed to the agent doing the real work

// ── pain reflex ──

type toolBreaker struct {
	mu sync.Mutex
	m  map[string]*breakState
}

type breakOutcome struct {
	at time.Time
	ok bool
}

type breakState struct {
	out       []breakOutcome
	openUntil time.Time
	trips     int
	last      string
}

// Tools whose failures say something about agents or requests, not about the tool.
var breakerExempt = map[string]bool{"delegate": true, "ask_colleague": true, "ask_user": true, "escalate": true, "sleep": true, "notify_user": true,
	"agent_find": true, "task_status": true, "task_steer": true, "task_cancel": true, "decisions": true}

const breakWindow = 30 * time.Minute

// allow reports whether a call may go ahead; when not, msg is what the agent is told.
func (b *toolBreaker) allow(tool string) (bool, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.m[tool]
	if s == nil || !time.Now().Before(s.openUntil) {
		return true, ""
	}
	fails := 0
	for _, o := range s.out {
		if !o.ok {
			fails++
		}
	}
	return false, fmt.Sprintf("Error: %s is paused for everyone until %s — it kept failing (%d of its last %d calls; last error: %s). Do not retry it now: use another way, or report that it is unavailable.",
		tool, s.openUntil.Format("15:04"), fails, len(s.out), textutil.Brief(s.last, 160))
}

// infraFailure tells a tool that is broken (down, timing out, refusing connections, rate-limited, erroring on its side) from a call
// that was simply wrong (a missing file, a 404, bad arguments): the second kind says something about the request, not the tool.
var infraFailure = regexp.MustCompile(`(?i)(timeout|timed out|deadline exceeded|connection (refused|reset)|no such host|unreachable|broken pipe|EOF|\b5\d\d\b|rate.?limit|too many requests|\b429\b|unavailable|temporarily|overloaded|not running|crashed|panic)`)

// record notes one finished call and reports whether it tripped the breaker. A failure that looks like the request's own fault is
// ignored, so five bad file paths or dead links cannot switch a healthy tool off for everyone.
func (b *toolBreaker) record(tool string, ok bool, msg string) (tripped bool, until time.Time) {
	if breakerExempt[tool] || (!ok && !infraFailure.MatchString(msg)) {
		return false, until
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]*breakState{}
	}
	s := b.m[tool]
	if s == nil {
		s = &breakState{}
		b.m[tool] = s
	}
	now := time.Now()
	keep := s.out[:0]
	for _, o := range s.out {
		if now.Sub(o.at) < breakWindow {
			keep = append(keep, o)
		}
	}
	s.out = append(keep, breakOutcome{now, ok})
	if len(s.out) > 12 {
		s.out = s.out[len(s.out)-12:]
	}
	if ok {
		if s.trips > 0 && now.After(s.openUntil) && len(s.out) >= 2 && s.out[len(s.out)-2].ok {
			s.trips--
		}
		return false, until
	}
	s.last = msg
	need := 5
	if s.trips > 0 {
		need = 3
	}
	fails := 0
	for _, o := range s.out {
		if !o.ok {
			fails++
		}
	}
	if len(s.out) >= need && float64(fails)/float64(len(s.out)) >= 0.9 && !now.Before(s.openUntil) {
		d := 20 * time.Minute << min(s.trips, 4)
		s.openUntil, s.trips = now.Add(d), s.trips+1
		s.out = nil
		return true, s.openUntil
	}
	return false, until
}

type BreakerRow struct {
	Tool  string    `json:"tool"`
	Until time.Time `json:"until"`
	Trips int       `json:"trips"`
	Last  string    `json:"last"`
}

// PausedTools lists the tools currently cut off.
func (e *Engine) PausedTools() []BreakerRow {
	e.brk.mu.Lock()
	defer e.brk.mu.Unlock()
	out := []BreakerRow{}
	for name, s := range e.brk.m {
		if time.Now().Before(s.openUntil) {
			out = append(out, BreakerRow{name, s.openUntil, s.trips, s.last})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

// ResumeTool lifts the pause on a tool (the user fixed whatever was wrong).
func (e *Engine) ResumeTool(tool string) {
	e.brk.mu.Lock()
	defer e.brk.mu.Unlock()
	if s := e.brk.m[tool]; s != nil {
		s.openUntil, s.trips, s.out = time.Time{}, 0, nil
	}
	e.Emit("tool.resumed", map[string]any{"tool": tool})
}

// ── ant trails ──

const trailHalfLifeDays = 14.0

// depositTrail leaves a trace for the agent (kind "*") and for each tool it used: a success strengthens it, a failure weakens it.
// Older traces fade by half every two weeks (applied here, in SQL, whenever the trace is touched or read).
func (e *Engine) depositTrail(ctx context.Context, agent string, kinds []string, ok bool) {
	delta := 1.0
	if !ok {
		delta = -1.5
	}
	for _, k := range kinds {
		w, l := 1, 0
		if !ok {
			w, l = 0, 1
		}
		_, _ = e.DB.Exec(ctx, `INSERT INTO trails (agent, kind, weight, wins, losses) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (agent, kind) DO UPDATE SET weight = LEAST(20, GREATEST(-3, trails.weight*power(0.5, extract(epoch FROM now()-trails.updated_at)/86400/$6) + $3)),
				wins = trails.wins+$4, losses = trails.losses+$5, updated_at = now()`, agent, k, max(delta, -3), w, l, trailHalfLifeDays)
	}
}

// trailWeight is the agent's current (faded) trace, about -3 to 20.
func (e *Engine) trailWeight(ctx context.Context, agent string) float64 {
	var w, age float64
	if e.DB.QueryRow(ctx, `SELECT weight, extract(epoch FROM now()-updated_at)/86400 FROM trails WHERE agent=$1 AND kind='*'`, agent).Scan(&w, &age) != nil {
		return 0
	}
	return w * math.Pow(0.5, age/trailHalfLifeDays)
}

// trailBonus turns a trace into a modest nudge for routing (about -0.4 to +0.4): it breaks ties and shades close calls, it does not
// override who actually holds the tool for the job.
func (e *Engine) trailBonus(ctx context.Context, agent string) float64 {
	return 0.4 * math.Tanh(e.trailWeight(ctx, agent)/6)
}

// TrailRow is one agent's trace for the settings page.
type TrailRow struct {
	Agent  string  `json:"agent"`
	Weight float64 `json:"weight"`
	Wins   int     `json:"wins"`
	Losses int     `json:"losses"`
}

func (e *Engine) Trails(ctx context.Context) []TrailRow {
	rows, err := e.DB.Query(ctx, `SELECT agent, round((weight*power(0.5, extract(epoch FROM now()-updated_at)/86400/$1))::numeric, 2)::float8, wins, losses FROM trails WHERE kind='*' ORDER BY 2 DESC`, trailHalfLifeDays)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []TrailRow{}
	for rows.Next() {
		var r TrailRow
		if rows.Scan(&r.Agent, &r.Weight, &r.Wins, &r.Losses) == nil {
			out = append(out, r)
		}
	}
	return out
}

// toolsUsed lists the distinct tools a session called (the ones that say something about trust: not delegation plumbing).
func (e *Engine) toolsUsed(ctx context.Context, session int64) []string {
	msgs, err := e.Sessions.Messages(ctx, session)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			if !seen[c.Name] && !breakerExempt[c.Name] && len(out) < 12 {
				seen[c.Name] = true
				out = append(out, c.Name)
			}
		}
	}
	return out
}

// learnFromOutcome is called when a task ends: it leaves the trail and updates trust. A declination (NOT_CAPABLE) and a
// cancellation say nothing about the agent.
func (e *Engine) learnFromOutcome(ctx context.Context, t tasks.Task) {
	if t.Status != tasks.Done && t.Status != tasks.Failed && t.Status != tasks.Partial {
		return
	}
	if _, declined := notCapable(t.Result); declined {
		return
	}
	ok := t.Status == tasks.Done
	var used []string
	if t.SessionID != nil {
		used = e.toolsUsed(ctx, *t.SessionID)
	}
	e.depositTrail(ctx, t.ToAgent, append([]string{"*"}, used...), ok)
	e.noteTrust(ctx, t.ToAgent, used, ok)
}

// ── immune memory ──

type Antibody struct {
	ID       int64      `json:"id"`
	Agent    string     `json:"agent"`
	Triggers []string   `json:"triggers"`
	Rule     string     `json:"rule"`
	Source   string     `json:"source"`
	Hits     int        `json:"hits"`
	LastHit  *time.Time `json:"last_hit,omitempty"`
	Created  time.Time  `json:"created_at"`
}

const antibodyTTL = 60 * 24 * time.Hour

// AddAntibody stores a lesson. agent "*" applies to everyone; no triggers means it always applies to that agent. A trigger is a word
// that must appear in the task, or "tool:<name>" for an agent that holds that tool.
func (e *Engine) AddAntibody(ctx context.Context, agent string, triggers []string, rule, source string) error {
	rule = strings.TrimSpace(rule)
	if r := []rune(rule); len(r) > 320 {
		rule = string(r[:320])
	}
	if len([]rune(rule)) < 12 {
		return fmt.Errorf("a lesson needs a sentence")
	}
	var tr []string
	for _, t := range triggers {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" && len(tr) < 6 {
			tr = append(tr, t)
		}
	}
	if agent = strings.TrimSpace(agent); agent == "" {
		agent = "*"
	}
	_, err := e.DB.Exec(ctx, `INSERT INTO antibodies (agent, triggers, rule, source) VALUES ($1,$2,$3,$4) ON CONFLICT (agent, rule) DO UPDATE SET triggers=EXCLUDED.triggers, last_hit=now()`, agent, tr, rule, source)
	return err
}

func (e *Engine) Antibodies(ctx context.Context) ([]Antibody, error) {
	rows, err := e.DB.Query(ctx, `SELECT id, agent, triggers, rule, source, hits, last_hit, created_at FROM antibodies ORDER BY COALESCE(last_hit, created_at) DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Antibody{}
	for rows.Next() {
		var a Antibody
		if rows.Scan(&a.ID, &a.Agent, &a.Triggers, &a.Rule, &a.Source, &a.Hits, &a.LastHit, &a.Created) == nil {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

func (e *Engine) DeleteAntibody(ctx context.Context, id int64) error {
	_, err := e.DB.Exec(ctx, `DELETE FROM antibodies WHERE id=$1`, id)
	return err
}

// antibodyNote returns the lessons whose trigger shows up in this task, as a note for the agent's input (empty when none).
func (e *Engine) antibodyNote(ctx context.Context, p *Profile, input string) string {
	rows, err := e.DB.Query(ctx, `SELECT id, triggers, rule FROM antibodies WHERE agent='*' OR lower(agent)=lower($1) ORDER BY id DESC LIMIT 60`, p.Name)
	if err != nil {
		return ""
	}
	words := map[string]bool{}
	for _, w := range textmatch.Tokens(input) {
		words[w] = true
	}
	lower := strings.ToLower(input)
	held := map[string]bool{}
	for _, t := range p.Tools {
		held[strings.ToLower(t)] = true
	}
	var fired []int64
	var lines []string
	for rows.Next() {
		var id int64
		var trig []string
		var rule string
		if rows.Scan(&id, &trig, &rule) != nil {
			continue
		}
		hit := len(trig) == 0
		for _, t := range trig {
			switch {
			case strings.HasPrefix(t, "tool:"):
				hit = hit || held[strings.TrimPrefix(t, "tool:")]
			case strings.Contains(t, " "):
				hit = hit || strings.Contains(lower, t)
			default:
				hit = hit || words[t]
			}
		}
		if hit && len(lines) < 4 {
			fired, lines = append(fired, id), append(lines, "- "+rule)
		}
	}
	rows.Close()
	if len(lines) == 0 {
		return ""
	}
	_, _ = e.DB.Exec(ctx, `UPDATE antibodies SET hits=hits+1, last_hit=now() WHERE id=ANY($1)`, fired)
	return "\n\n[Lessons from earlier mistakes that apply to this job]\n" + strings.Join(lines, "\n")
}

// ── scouting ──

const scoutBrief = "Recon only. Another agent will do the real work in a moment; your job is a quick, READ-ONLY look so that it starts informed. " +
	"Use read-only tools to find out what you can about the job below: what exists, where things are, what the obvious snags are, what is unclear or missing. " +
	"Do not change anything. Finish with a short brief (under 150 words): what you found, what looks risky or ambiguous, what you would check first.\n\nTHE JOB:\n"

// scout takes a quick read-only look at a job before the real run and returns a brief for the agent that will do it ("" when the
// look produced nothing useful).
func (e *Engine) scout(ctx context.Context, env *tools.Env, p *Profile, instruction, title string, depth int) string {
	t, err := e.childTask(ctx, env, p, "Scout: "+title, scoutBrief+instruction, depth)
	if err != nil {
		return ""
	}
	out := e.RunTask(ctx, t, TaskOpts{Scout: true, Leaf: true, Tainted: env.Tainted, ParentRun: env.RunID, Restricted: env.Restricted})
	if out.Status != tasks.Done || strings.TrimSpace(out.Result) == "" {
		return ""
	}
	if _, declined := notCapable(out.Result); declined {
		return ""
	}
	return strings.TrimSpace(out.Result)
}
