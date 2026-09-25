// Package scheduler runs PRISM's autonomy: cron jobs (a fresh session with a
// standing prompt), standing intents (prospective memory: "tell me when X
// ships"), watches (progress of long-running things) and dream briefings.
// Everything is table-driven and survives restarts.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/agent"
	"prism/internal/cron"
	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/textmatch"
)

type Cron struct {
	ID      int64      `json:"id"`
	Name    string     `json:"name"`
	Agent   string     `json:"agent"`
	Expr    string     `json:"expr"`
	Prompt  string     `json:"prompt"`
	Enabled bool       `json:"enabled"`
	System  bool       `json:"system"`
	LastRun *time.Time `json:"last_run,omitempty"`
	NextRun *time.Time `json:"next_run,omitempty"`
}

type Intent struct {
	ID          int64           `json:"id"`
	Type        string          `json:"type"` // intent | watch
	Owner       string          `json:"owner"`
	Description string          `json:"description"`
	Predicate   json.RawMessage `json:"predicate"`
	CadenceS    int             `json:"cadence_s"`
	Repeat      bool            `json:"repeat"`
	Status      string          `json:"status"`
	Progress    string          `json:"progress"`
	LastCheck   *time.Time      `json:"last_check,omitempty"`
	NextDue     time.Time       `json:"next_due"`
	LastError   string          `json:"last_error"`
	Notify      bool            `json:"notify"`
	CreatedAt   time.Time       `json:"created_at"`
	FiredAt     *time.Time      `json:"fired_at,omitempty"`
	// Monitor fields (watches with a time budget, see monitor.go)
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Announce   bool       `json:"announce"`
	Fraction   *float64   `json:"fraction,omitempty"`    // how far along the last progress text said it was
	ETASeconds *int       `json:"eta_seconds,omitempty"` // estimated seconds to completion, when it can be told
	samples    []sample
}

type Briefing struct {
	ID         int64      `json:"id"`
	Agent      string     `json:"agent"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	Importance int        `json:"importance"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	Reply      string     `json:"reply"`
	RepliedAt  *time.Time `json:"replied_at"`
}

type Service struct {
	DB       *pgxpool.Pool
	Engine   *agent.Engine
	Settings *settings.Store
	Env      Env
	Emit     func(typ string, data any)
	Logf     func(level, source, format string, args ...any)
	// Notify reports scheduler events to the notification centre (may be nil).
	Notify func(kind, level, title, text string)

	mu      sync.Mutex
	running bool
}

func (s *Service) emit(t string, d any) {
	if s.Emit != nil {
		s.Emit(t, d)
	}
}

func (s *Service) logf(level, format string, args ...any) {
	if s.Logf != nil {
		s.Logf(level, "scheduler", format, args...)
	} else {
		log.Printf("[%s] scheduler: "+format, append([]any{level}, args...)...)
	}
}

func (s *Service) loc(ctx context.Context) *time.Location {
	g := settings.Load(ctx, s.Settings, settings.KeyGeneral, settings.General{})
	if g.Timezone != "" {
		if l, err := time.LoadLocation(g.Timezone); err == nil {
			return l
		}
	}
	return time.Local
}

// Start runs the tick loop until ctx ends.
func (s *Service) Start(ctx context.Context) {
	go func() {
		s.tick(ctx)
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.tick(ctx)
			}
		}
	}()
}

func (s *Service) autonomy(ctx context.Context) settings.Autonomy {
	return settings.Load(ctx, s.Settings, settings.KeyAutonomy, settings.Autonomy{Enabled: true, DreamEnabled: true})
}

func (s *Service) tick(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()
	if !s.autonomy(ctx).Enabled {
		return
	}
	s.tickCrons(ctx)
	s.tickIntents(ctx)
}

// ── crons ───────────────────────────────────────────────────────────────────

func (s *Service) Crons(ctx context.Context) ([]Cron, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,name,agent,expr,prompt,enabled,system,last_run,next_run FROM crons ORDER BY system DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cron
	for rows.Next() {
		var c Cron
		if err := rows.Scan(&c.ID, &c.Name, &c.Agent, &c.Expr, &c.Prompt, &c.Enabled, &c.System, &c.LastRun, &c.NextRun); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) SaveCron(ctx context.Context, c Cron) (int64, error) {
	sched, err := cron.Parse(c.Expr)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(c.Prompt) == "" || strings.TrimSpace(c.Agent) == "" || strings.TrimSpace(c.Name) == "" {
		return 0, errors.New("name, agent and prompt are required")
	}
	next := sched.Next(time.Now().In(s.loc(ctx)))
	var id int64
	if c.ID == 0 {
		err = s.DB.QueryRow(ctx, `INSERT INTO crons(name,agent,expr,prompt,enabled,system,next_run) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			c.Name, c.Agent, c.Expr, c.Prompt, c.Enabled, c.System, next).Scan(&id)
	} else {
		id = c.ID
		_, err = s.DB.Exec(ctx, `UPDATE crons SET name=$2,agent=$3,expr=$4,prompt=$5,enabled=$6,next_run=$7 WHERE id=$1`, c.ID, c.Name, c.Agent, c.Expr, c.Prompt, c.Enabled, next)
	}
	return id, err
}

func (s *Service) DeleteCron(ctx context.Context, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM crons WHERE id=$1 AND NOT system`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("built-in schedules cannot be deleted (disable them instead)")
	}
	return err
}

func (s *Service) fireCron(ctx context.Context, c Cron) error {
	priority := 0
	if c.System {
		// built-in maintenance (memory consolidation, dreaming, evolution review) shouldn't sit behind a
		// backlog of ad-hoc chat delegation — the queue is priority-ordered (tasks.ClaimNext), so this only
		// changes turn order when there's a backlog, never preempts something already running.
		priority = systemCronPriority
	}
	input := c.Prompt
	if c.System && c.Agent == "Oneiros" {
		input += s.dismissedNote(ctx)
	}
	_, err := s.Engine.Enqueue(ctx, tasks.Task{FromKind: "cron", FromName: c.Name, ToAgent: c.Agent, Title: c.Name, Input: input, Priority: priority})
	return err
}

const systemCronPriority = 5

func (s *Service) RunCronNow(ctx context.Context, id int64) error {
	cs, err := s.Crons(ctx)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if c.ID == id {
			return s.fireCron(ctx, c)
		}
	}
	return errors.New("cron not found")
}

func (s *Service) tickCrons(ctx context.Context) {
	cs, err := s.Crons(ctx)
	if err != nil {
		return
	}
	au := s.autonomy(ctx)
	now := time.Now()
	loc := s.loc(ctx)
	for _, c := range cs {
		if !c.Enabled {
			continue
		}
		sched, err := cron.Parse(c.Expr)
		if err != nil {
			continue
		}
		if c.NextRun == nil {
			n := sched.Next(now.In(loc))
			_, _ = s.DB.Exec(ctx, `UPDATE crons SET next_run=$2 WHERE id=$1`, c.ID, n)
			continue
		}
		if c.NextRun.After(now) {
			continue
		}
		next := sched.Next(now.In(loc))
		_, _ = s.DB.Exec(ctx, `UPDATE crons SET last_run=now(), next_run=$2 WHERE id=$1`, c.ID, next)
		if c.System && c.Agent == "Oneiros" && !au.DreamEnabled {
			continue
		}
		// a schedule missed by more than its own period (machine asleep) fires once, not repeatedly
		if err := s.fireCron(ctx, c); err != nil {
			s.logf("warn", "cron %q failed to enqueue: %v", c.Name, err)
		}
		s.emit("cron.fired", map[string]any{"id": c.ID, "name": c.Name})
		if s.Notify != nil {
			s.Notify("cron", "info", "Schedule fired", fmt.Sprintf("“%s” started for %s", c.Name, c.Agent))
		}
	}
}

// ── intents & watches ───────────────────────────────────────────────────────

const intentCols = `id,type,owner,description,predicate,cadence_s,repeat,status,progress,last_check,next_due,last_error,notify,created_at,fired_at,expires_at,announce,samples`

func scanIntent(r pgx.Row) (Intent, error) {
	var i Intent
	var sm []byte
	err := r.Scan(&i.ID, &i.Type, &i.Owner, &i.Description, &i.Predicate, &i.CadenceS, &i.Repeat, &i.Status, &i.Progress, &i.LastCheck, &i.NextDue, &i.LastError, &i.Notify, &i.CreatedAt, &i.FiredAt,
		&i.ExpiresAt, &i.Announce, &sm)
	i.samples = parseSamples(sm)
	if n := len(i.samples); n > 0 && i.Status == "active" {
		f := i.samples[n-1].F
		i.Fraction = &f
		if e, ok := etaSeconds(i.samples); ok {
			i.ETASeconds = &e
		}
	}
	return i, err
}

func (s *Service) Intents(ctx context.Context, status string) ([]Intent, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+intentCols+` FROM intents WHERE ($1='' OR status=$1) ORDER BY (status='active') DESC, id DESC LIMIT 300`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Intent
	for rows.Next() {
		i, err := scanIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Service) CreateIntent(ctx context.Context, i Intent, p Predicate) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(i.Description) == "" || strings.TrimSpace(i.Owner) == "" {
		return 0, errors.New("description and owner are required")
	}
	if i.Type == "" {
		i.Type = "intent"
	}
	if i.CadenceS <= 0 {
		i.CadenceS = 300
	}
	min := 10
	if i.Type == "intent" {
		min = 30
	}
	if i.CadenceS < min {
		i.CadenceS = min
	}
	pj, _ := json.Marshal(p)
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO intents(type,owner,description,predicate,cadence_s,repeat,notify,next_due,expires_at,announce) VALUES($1,$2,$3,$4,$5,$6,$7,now(),$8,$9) RETURNING id`,
		i.Type, i.Owner, i.Description, pj, i.CadenceS, i.Repeat, i.Notify || i.ID == 0, i.ExpiresAt, i.Announce).Scan(&id)
	if err == nil {
		s.emit("intent.update", map[string]any{"id": id})
	}
	return id, err
}

func (s *Service) UpdateIntent(ctx context.Context, id int64, status string, cadence *int) error {
	if status != "" {
		if status != "active" && status != "cancelled" {
			return errors.New("status must be active or cancelled")
		}
		if _, err := s.DB.Exec(ctx, `UPDATE intents SET status=$2, next_due=now(), last_error='' WHERE id=$1`, id, status); err != nil {
			return err
		}
	}
	if cadence != nil && *cadence >= 10 && *cadence <= 86400 {
		if _, err := s.DB.Exec(ctx, `UPDATE intents SET cadence_s=$2 WHERE id=$1`, id, *cadence); err != nil {
			return err
		}
	}
	s.emit("intent.update", map[string]any{"id": id})
	return nil
}

func (s *Service) DeleteIntent(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM intents WHERE id=$1`, id)
	return err
}

func (s *Service) tickIntents(ctx context.Context) {
	rows, err := s.DB.Query(ctx, `SELECT `+intentCols+` FROM intents WHERE status='active' AND (next_due <= now() OR expires_at <= now()) ORDER BY next_due LIMIT 20`)
	if err != nil {
		return
	}
	var due []Intent
	for rows.Next() {
		if i, err := scanIntent(rows); err == nil {
			due = append(due, i)
		}
	}
	rows.Close()
	for _, i := range due {
		if i.ExpiresAt != nil && !i.ExpiresAt.After(time.Now()) {
			s.expire(ctx, i)
			continue
		}
		s.check(ctx, i)
	}
}

func (s *Service) check(ctx context.Context, i Intent) {
	var p Predicate
	if err := json.Unmarshal(i.Predicate, &p); err != nil {
		_, _ = s.DB.Exec(ctx, `UPDATE intents SET status='error', last_error=$2 WHERE id=$1`, i.ID, "bad predicate: "+err.Error())
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	res, err := p.Eval(cctx, s.Env)
	cancel()
	next := time.Now().Add(time.Duration(i.CadenceS) * time.Second)
	if err != nil {
		errs, _ := p.state()["errs"].(float64)
		errs++
		p.state()["errs"] = errs
		pj, _ := json.Marshal(p)
		status := "active"
		if errs >= 6 {
			status = "error"
		}
		_, _ = s.DB.Exec(ctx, `UPDATE intents SET predicate=$2, last_check=now(), next_due=$3, last_error=$4, status=$5 WHERE id=$1`, i.ID, pj, next, err.Error(), status)
		if status == "error" {
			if s.Notify != nil {
				s.Notify("error", "warning", "Watch stopped", fmt.Sprintf("“%s” failed %d times in a row: %v", i.Description, int(errs), err))
			}
			s.Engine.Notify(ctx, agent.Notice{Agent: i.Owner, Level: "warning", Text: fmt.Sprintf("I stopped watching “%s”: it failed %d times in a row (%v).", i.Description, int(errs), err)})
		}
		s.emit("intent.update", map[string]any{"id": i.ID})
		return
	}
	p.state()["errs"] = 0.0
	ss := s.recordSample(ctx, i.ID, i.samples, res.Progress)
	if i.Announce && !res.Fired && res.Progress != "" && res.Progress != i.Progress {
		// a monitor that reports while it runs: at most one update per 5 minutes, with the estimate when there is one
		last, _ := p.state()["ann"].(float64)
		if time.Since(time.Unix(int64(last), 0)) >= 5*time.Minute {
			p.state()["ann"] = float64(time.Now().Unix())
			txt := fmt.Sprintf("%s — %s", i.Description, res.Progress)
			if e, ok := etaSeconds(ss); ok {
				txt += fmt.Sprintf(" (about %s left)", humanDuration(e))
			}
			s.Engine.Notify(ctx, agent.Notice{Agent: i.Owner, Level: "info", Text: txt})
		}
	}
	pj, _ := json.Marshal(p)
	if !res.Fired {
		_, _ = s.DB.Exec(ctx, `UPDATE intents SET predicate=$2, last_check=now(), next_due=$3, progress=$4, last_error='' WHERE id=$1`, i.ID, pj, next, res.Progress)
		s.emit("intent.update", map[string]any{"id": i.ID, "progress": res.Progress})
		return
	}
	status := "fired"
	if i.Repeat {
		status = "active"
	}
	_, _ = s.DB.Exec(ctx, `UPDATE intents SET predicate=$2, last_check=now(), next_due=$3, progress=$4, status=$5, fired_at=now(), last_error='' WHERE id=$1`, i.ID, pj, next, res.Progress, status)
	s.emit("intent.update", map[string]any{"id": i.ID, "fired": true})
	if s.Notify != nil {
		s.Notify("intent", "attention", "Intent triggered", i.Description)
	}
	if !i.Notify {
		return
	}
	if i.Type == "watch" { // watches report directly: no model call needed
		s.Engine.Notify(ctx, agent.Notice{Agent: i.Owner, Level: "info", Text: fmt.Sprintf("%s — %s", i.Description, res.Evidence)})
		return
	}
	// intents wake their owner, who decides what the user should hear
	_, err = s.Engine.Enqueue(ctx, tasks.Task{FromKind: "intent", FromName: fmt.Sprintf("intent #%d", i.ID), ToAgent: i.Owner, Title: "Intent: " + i.Description,
		Input: fmt.Sprintf("A standing intent you registered has triggered.\nIntent: %s\nEvidence: %s\n\nDecide what the user should be told and reply with that message (concise, useful, no preamble). If acting is within your remit, do it first. If it is not worth interrupting the user, reply NO_REPLY.", i.Description, res.Evidence)})
	if err != nil {
		s.logf("warn", "could not wake %s for intent %d: %v", i.Owner, i.ID, err)
	}
}

// ── briefings ───────────────────────────────────────────────────────────────

func (s *Service) Briefings(ctx context.Context, status string) ([]Briefing, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,agent,title,body,importance,status,created_at,reply,replied_at FROM briefings WHERE ($1='' OR status=$1) ORDER BY id DESC LIMIT 200`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Briefing
	for rows.Next() {
		var b Briefing
		if err := rows.Scan(&b.ID, &b.Agent, &b.Title, &b.Body, &b.Importance, &b.Status, &b.CreatedAt, &b.Reply, &b.RepliedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Briefing fetches a single briefing by id (for the "save to Obsidian" / "read" actions).
func (s *Service) Briefing(ctx context.Context, id int64) (Briefing, error) {
	var b Briefing
	err := s.DB.QueryRow(ctx, `SELECT id,agent,title,body,importance,status,created_at,reply,replied_at FROM briefings WHERE id=$1`, id).
		Scan(&b.ID, &b.Agent, &b.Title, &b.Body, &b.Importance, &b.Status, &b.CreatedAt, &b.Reply, &b.RepliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, fmt.Errorf("briefing #%d does not exist", id)
	}
	return b, err
}

func (s *Service) AddBriefing(ctx context.Context, agentName, title, body string, importance int) (int64, error) {
	if importance < 1 {
		importance = 1
	}
	if importance > 5 {
		importance = 5
	}
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO briefings(agent,title,body,importance) VALUES($1,$2,$3,$4) RETURNING id`, agentName, title, body, importance).Scan(&id)
	if err != nil {
		return 0, err
	}
	s.emit("briefing.new", map[string]any{"id": id, "title": title})
	if importance >= 4 { // deliver immediately
		s.Engine.Notify(ctx, agent.Notice{Agent: agentName, Level: "attention", Text: "**" + title + "**\n" + body})
		_, _ = s.DB.Exec(ctx, `UPDATE briefings SET status='delivered' WHERE id=$1`, id)
	}
	return id, nil
}

// ReplyBriefing records the user's answer to a briefing and hands it to the agent that wrote it as a task, so
// the answer is acted on (remembered, used in the next dream) rather than left in a text box.
func (s *Service) ReplyBriefing(ctx context.Context, id int64, text string) (*tasks.Task, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("write a reply first")
	}
	b, err := s.Briefing(ctx, id)
	if err != nil {
		return nil, err
	}
	t, err := s.Engine.Enqueue(ctx, tasks.Task{FromKind: "user", FromName: "user", ToAgent: b.Agent, Title: "Reply to briefing: " + b.Title,
		Input: fmt.Sprintf("You wrote this briefing for the user:\n\n%s\n%s\n\nThe user has answered:\n%s\n\nWork out what the answer means. Store anything durable with memory_store (a preference, a decision, a correction — and if the answer contradicts something you believed, say so in the fact). Then, if it changes what should happen next, do it or note it. Finish with one short sentence confirming what you took from the answer.", b.Title, b.Body, text)})
	if err != nil {
		return nil, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE briefings SET reply=$2, replied_at=now(), status=CASE WHEN status='new' THEN 'delivered' ELSE status END WHERE id=$1`, id, text); err != nil {
		return nil, err
	}
	s.emit("briefing.new", map[string]any{"id": id, "title": b.Title})
	return &t, nil
}

func (s *Service) SetBriefingStatus(ctx context.Context, id int64, status string) error {
	_, err := s.DB.Exec(ctx, `UPDATE briefings SET status=$2 WHERE id=$1`, id, status)
	return err
}

// ── defaults ────────────────────────────────────────────────────────────────

// SeedDefaults installs the built-in maintenance schedules (never overwriting user edits).
func (s *Service) SeedDefaults(ctx context.Context) error {
	defs := []Cron{
		{Name: "Memory consolidation", Agent: "Mnemosyne", Expr: "30 3 * * *", Enabled: true, System: true,
			Prompt: "Nightly memory consolidation. Run memory_consolidate, then review the user bank and the most active project/domain banks: merge duplicates, retire trivia or wrong facts, and keep facts self-contained. Finish with a 3-line report. If nothing needed changing reply NO_REPLY."},
		{Name: "Dream (briefings)", Agent: "Oneiros", Expr: "0 9 * * *", Enabled: true, System: true,
			Prompt: "Dream now. Reflect on everything known about the user (memory_find / memory_list) and prepare up to three specific, grounded briefings with briefing_add. If nothing deserves their attention reply NO_REPLY."},
		{Name: "Agent evolution review", Agent: "Metis", Expr: "0 4 * * 0", Enabled: false, System: true,
			Prompt: "Weekly evolution review. For each non-system agent with at least 5 tasks in the last 30 days (agent_performance), check its profile bank and the user bank for lessons; propose a soul revision with evolve_propose only where the evidence is clear. Report which agents you reviewed. If nothing changed reply NO_REPLY."},
		{Name: "Evolution audit", Agent: "Metis", Expr: "0 5 * * 0", Enabled: false, System: true,
			Prompt: "Weekly audit of the evolvers. Call evolution_audit (load it with tool_search if needed) and judge last week's proposals — yours, Daedalus's skills and Forge's hires: were the rationales backed by evidence, did applied changes bloat or contradict a soul, did the agent's success rate improve, hold or drop after the change? For a change that looks harmful, propose the fix with evolve_propose (a revert or a trimmed soul). Then note one or two lessons about how YOU should propose better (store them with memory tools in your own profile bank). Report in a few lines; if there were no proposals reply NO_REPLY."},
	}
	for _, d := range defs {
		var n int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM crons WHERE system AND name=$1`, d.Name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := s.SaveCron(ctx, d); err != nil {
				return err
			}
		}
	}
	return nil
}

// ── dismissed briefings ─────────────────────────────────────────────────────

const dismissedWindow = 60 * 24 * time.Hour

// RecentDismissed lists the briefings the user waved away lately, newest first.
func (s *Service) RecentDismissed(ctx context.Context, limit int) ([]Briefing, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,agent,title,body,importance,status,created_at FROM briefings
		WHERE status='dismissed' AND created_at > $1 ORDER BY id DESC LIMIT $2`, time.Now().Add(-dismissedWindow), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Briefing
	for rows.Next() {
		var b Briefing
		if err := rows.Scan(&b.ID, &b.Agent, &b.Title, &b.Body, &b.Importance, &b.Status, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// dismissedNote is appended to the dream prompt so Oneiros knows what the user already said "no thanks" to.
func (s *Service) dismissedNote(ctx context.Context) string {
	ds, err := s.RecentDismissed(ctx, 25)
	if err != nil || len(ds) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nBriefings the user DISMISSED recently — they did not want these. Do not repeat these topics unless something materially new has happened since (and then say what is new):\n")
	for _, d := range ds {
		fmt.Fprintf(&sb, "- %s\n", d.Title)
	}
	return sb.String()
}

// SimilarDismissed finds a dismissed briefing that covers the same ground as a new one (token overlap of
// title+body), so briefing_add can refuse to nag about something the user already rejected.
func (s *Service) SimilarDismissed(ctx context.Context, title, body string) *Briefing {
	ds, err := s.RecentDismissed(ctx, 100)
	if err != nil {
		return nil
	}
	want := tokenSet(title + " " + body)
	for i := range ds {
		if jaccardSets(want, tokenSet(ds[i].Title+" "+ds[i].Body)) >= 0.5 {
			return &ds[i]
		}
	}
	return nil
}

func tokenSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, t := range textmatch.Tokens(s) {
		if len(t) > 2 {
			m[t] = true
		}
	}
	return m
}

func jaccardSets(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// maxActiveWatches is how many watches one agent may have running at once. A task with several downloads needs
// one check that covers them all (a list call), not one polling loop per item.
const maxActiveWatches = 3

// watchGuard refuses a fourth active watch of an agent and reports an identical one that already exists.
func (s *Service) watchGuard(ctx context.Context, owner string, p Predicate) (existing int64, err error) {
	rows, err := s.DB.Query(ctx, `SELECT id, predicate FROM intents WHERE owner=$1 AND type='watch' AND status='active' AND (expires_at IS NULL OR expires_at>now()) ORDER BY id`, owner)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	p.State = nil
	want, _ := json.Marshal(p)
	var ids []int64
	for rows.Next() {
		var id int64
		var raw []byte
		if rows.Scan(&id, &raw) != nil {
			continue
		}
		var q Predicate
		if json.Unmarshal(raw, &q) == nil {
			q.State = nil
			if got, _ := json.Marshal(q); string(got) == string(want) {
				return id, nil
			}
		}
		ids = append(ids, id)
	}
	if len(ids) >= maxActiveWatches {
		var l []string
		for _, id := range ids {
			l = append(l, fmt.Sprintf("#%d", id))
		}
		return 0, fmt.Errorf("you already have %d active watches (%s). Do not start one watch per item: cancel or adjust an existing one (intent_cancel, monitor_adjust), or cover several items with a single check — one list call whose output shows all of them", len(ids), strings.Join(l, " "))
	}
	return 0, nil
}
