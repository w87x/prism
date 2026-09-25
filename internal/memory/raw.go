package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"prism/internal/llm"
)

type RawMsg struct {
	From    string
	To      string
	Channel string
	Topic   string
	Text    string
	TaskID  int64
	// Agent is the agent whose work this message records; profile lessons drawn from it go to that agent's
	// bank. Empty: guessed from the participants.
	Agent string
	// Tainted marks a message that involved untrusted content (a web page, mail, an MCP result…) — set it
	// whenever the run that produced this text touched such content, even indirectly. Facts later distilled
	// from a tainted message get their confidence capped regardless of what the extraction model estimates:
	// source trust is a ceiling on confidence, not a number the model can talk itself past.
	Tainted bool
}

// AddRaw appends a message to the raw bank. Every user/agent message lands here
// first, preserving from/to and time; Process later distils facts and cleans up.
func (s *Service) AddRaw(ctx context.Context, m RawMsg) error {
	if strings.TrimSpace(m.Text) == "" {
		return nil
	}
	var tid *int64
	if m.TaskID != 0 {
		tid = &m.TaskID
	}
	_, err := s.db.Exec(ctx, `INSERT INTO memory_raw(from_name,to_name,channel,topic,text,task_id,agent,tainted) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		m.From, m.To, m.Channel, m.Topic, m.Text, tid, m.Agent, m.Tainted)
	if err == nil {
		s.nudge()
	}
	return err
}

func (s *Service) RawCount(ctx context.Context) (n int) {
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM memory_raw WHERE NOT processed`).Scan(&n)
	return
}

type rawRow struct {
	id       int64
	from, to string
	text     string
	agent    string
	at       time.Time
	tainted  bool
	attempts int
	taskID   int64 // 0: not tied to a task (e.g. an ask_colleague exchange)
	channel  string
	topic    string
}

const extractPrompt = `You are the memory clerk of a personal AI assistant. From the conversation excerpt, extract durable facts worth remembering for future tasks.

Route each fact to a bank — pick the narrowest one that fits; "user" is not a catch-all for the whole conversation:
- "user": ONLY facts that describe the user themselves — their own preferences, identity, people in their life, routines, constraints (e.g. their hardware), goals, opinions. A fact about a product, model, price, place, or anything else the user asked about is NOT a "user" fact, even when it mattered for their task.
- "profile": lessons for the agent named in the first line — how the user wants things done, tool/workflow tips, mistakes to avoid that this agent should remember.
- "project:<Name>": facts belonging to a named ongoing project or task (things found, options compared, decisions, status). Give facts from the same task the same short name, so they land together.
- "domain:<Name>": general knowledge on a topic that would still be true in a different task (e.g. "domain:Cooking", "domain:LLM models").

Example — researching local LLM models for the user: "User has a Mac M3 with 96GB RAM" is a "user" fact (a constraint of the user). "Llama-4 Scout fits in 53GB at Q3" is NOT a "user" fact — that describes the model, not the user, so it goes in "domain:LLM models" or "project:<the task's name>".

Reuse an existing project/domain bank (listed below, if any) whenever a fact clearly belongs with it — do NOT invent a new, more specific name for the same ongoing topic (e.g. facts about different versions of one model family — GLM, GLM-4.6, GLM-5.3 — all belong in ONE bank such as "domain:GLM" or "project:<the task>", never one bank per version).

Rules: one self-contained sentence per fact, third person ("User prefers tea over coffee"), include dates for time-sensitive facts. Skip small talk, transient states and trivia already obvious. Never extract today's date or the current time as a fact by itself (e.g. "Today is March 3rd") — it is available live from the clock tool and would be wrong the very next day; only mention a date when it matters for what's being remembered (a deadline, an event, when something changed). Prefer few high-value facts; return an empty list when nothing qualifies. When unsure between "user" and another bank, prefer the other bank — the user bank is pulled into every agent's context on every task, so keeping it to facts about the user keeps it useful.
Answer JSON only: {"facts":[{"text":"...","bank":"user","tags":["..."],"confidence":0.0-1.0}]}`

// maxRawAttempts/maxPendingAttempts cap how many times a batch (or a single extracted fact) is retried
// before it is given up on rather than blocking the queue forever behind a permanently failing item.
const maxRawAttempts = 5
const maxPendingAttempts = 5

// Process distils unprocessed raw messages into facts. force ignores the minimum batch size. It returns
// the number of facts stored (including duplicates merged and previously-pending facts flushed).
//
// A Store() failure for one extracted fact must not lose that fact, and a retry must not re-ask the model
// (which could phrase things differently and produce a near-duplicate instead of the exact same fact): see
// flushPendingFacts and memory_pending_facts. A raw batch that fails outright (the model call, or its JSON)
// is retried on later cycles up to maxRawAttempts before being marked processed anyway, with the error kept
// on the row so it is not silently invisible.
func (s *Service) Process(ctx context.Context, batch int, force bool) (int, error) {
	return s.ProcessMin(ctx, batch, 0, force)
}

// DefaultProcessMin is how many raw messages make a digest pass worthwhile on their own.
const DefaultProcessMin = 6

// ProcessMin is Process with the minimum backlog (0 = DefaultProcessMin) that triggers a non-forced pass.
func (s *Service) ProcessMin(ctx context.Context, batch, minRaw int, force bool) (int, error) {
	if minRaw <= 0 {
		minRaw = DefaultProcessMin
	}
	if batch <= 0 {
		batch = 40
	}
	if s.llm.RoleRef(ctx, "fast") == "" {
		return 0, nil
	}
	n, firstErr := s.flushPendingFacts(ctx)

	// raw messages already tracked by an active (not given-up) pending fact are excluded: they are being
	// retried by flushPendingFacts above, not re-extracted from scratch.
	rows, err := s.db.Query(ctx, `SELECT r.id,r.from_name,r.to_name,r.text,r.agent,r.created_at,r.tainted,r.attempts,r.task_id,r.channel,r.topic FROM memory_raw r
		WHERE NOT processed AND NOT EXISTS (SELECT 1 FROM memory_pending_facts p WHERE r.id = ANY(p.raw_ids) AND NOT p.given_up)
		ORDER BY id LIMIT $1`, batch)
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
		return n, firstErr
	}
	var raws []rawRow
	for rows.Next() {
		var r rawRow
		var taskID *int64
		if err := rows.Scan(&r.id, &r.from, &r.to, &r.text, &r.agent, &r.at, &r.tainted, &r.attempts, &taskID, &r.channel, &r.topic); err != nil {
			rows.Close()
			if firstErr == nil {
				firstErr = err
			}
			return n, firstErr
		}
		if taskID != nil {
			r.taskID = *taskID
		}
		raws = append(raws, r)
	}
	rows.Close()
	if len(raws) == 0 || (!force && len(raws) < minRaw && time.Since(raws[0].at) < 20*time.Minute) {
		return n, firstErr
	}
	// Each message teaches the agent whose work it records, so lessons for "profile" land in that agent's
	// own bank instead of all going to whoever spoke most in the batch.
	groups := map[string][]rawRow{}
	var owners []string
	for _, r := range raws {
		o := r.agent
		if o == "" {
			o = guessOwner(r)
		}
		key := o + "\x00" + r.channel + "\x00" + r.topic // chats are digested separately: each may focus on its own project
		if _, ok := groups[key]; !ok {
			owners = append(owners, key)
		}
		groups[key] = append(groups[key], r)
	}
	for _, key := range owners {
		grp := groups[key]
		o := strings.SplitN(key, "\x00", 2)[0]
		ids := make([]int64, len(grp))
		for i, r := range grp {
			ids[i] = r.id
		}
		project := ""
		if s.ChatProject != nil {
			project = s.ChatProject(ctx, grp[0].channel, grp[0].topic)
		}
		stored, pending, err := s.distil(ctx, o, grp, project)
		if err != nil { // the whole batch failed (model call or unparsable JSON): retry later, up to the cap
			s.bumpRawAttempts(ctx, ids, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n += stored
		if pending == 0 { // every fact this batch produced was stored (or there were none): done with it
			if _, derr := s.db.Exec(ctx, `DELETE FROM memory_raw WHERE id=ANY($1)`, ids); derr != nil && firstErr == nil {
				firstErr = derr
			}
		}
		// pending > 0: the raw rows stay (memory_pending_facts now owns retrying those specific facts; the
		// exclusion clause above keeps this batch from being re-sent to the model next cycle)
	}
	return n, firstErr
}

func (s *Service) bumpRawAttempts(ctx context.Context, ids []int64, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, _ = s.db.Exec(ctx, `UPDATE memory_raw SET attempts=attempts+1, last_error=$2 WHERE id=ANY($1)`, ids, msg)
	// give up rather than block the queue forever behind a batch that can never succeed; last_error stays
	// on the row (it is not deleted) so the failure is still visible, not silently lost.
	_, _ = s.db.Exec(ctx, `UPDATE memory_raw SET processed=true WHERE id=ANY($1) AND attempts>=$2`, ids, maxRawAttempts)
}

// queuePendingFact durably records a fact the model already extracted but that failed to store, so the next
// Process cycle can retry the exact same Store call instead of re-extracting (and re-duplicating).
func (s *Service) queuePendingFact(ctx context.Context, rawIDs []int64, bank, agent, text string, tags []string, confidence float64, tainted bool, cause error) {
	if tags == nil {
		tags = []string{}
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO memory_pending_facts(raw_ids,bank,agent,text,tags,confidence,tainted,attempts,last_error)
		VALUES($1,$2,$3,$4,$5,$6,$7,1,$8)`, rawIDs, bank, agent, text, tags, float32(confidence), tainted, msg)
}

// flushPendingFacts retries facts left over from an earlier partial failure — a deterministic Store() call,
// never a fresh model extraction, so a transient failure (the embedding service was briefly down, a network
// hiccup) can never turn into a duplicate. Once every pending fact tied to a raw batch is resolved (stored,
// or given up on after maxPendingAttempts), that batch's raw messages are finally deleted.
func (s *Service) flushPendingFacts(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT id,raw_ids,bank,agent,text,tags,confidence,tainted,attempts FROM memory_pending_facts WHERE NOT given_up ORDER BY id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type prow struct {
		id                int64
		rawIDs            []int64
		bank, agent, text string
		tags              []string
		conf              float64
		tainted           bool
		attempts          int
	}
	var pend []prow
	for rows.Next() {
		var p prow
		var conf float32
		if err := rows.Scan(&p.id, &p.rawIDs, &p.bank, &p.agent, &p.text, &p.tags, &conf, &p.tainted, &p.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		p.conf = float64(conf)
		pend = append(pend, p)
	}
	rows.Close()
	n := 0
	touched := map[int64]bool{}
	var firstErr error
	for _, p := range pend {
		src := "raw"
		if p.tainted {
			src = "raw (tainted)"
		}
		if _, err := s.Store(ctx, StoreReq{Bank: p.bank, Agent: p.agent, Text: p.text, Tags: p.tags, Source: src, Confidence: p.conf}); err != nil {
			attempts := p.attempts + 1
			_, _ = s.db.Exec(ctx, `UPDATE memory_pending_facts SET attempts=$2, last_error=$3, given_up=$4 WHERE id=$1`,
				p.id, attempts, err.Error(), attempts >= maxPendingAttempts)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		_, _ = s.db.Exec(ctx, `DELETE FROM memory_pending_facts WHERE id=$1`, p.id)
		n++
		for _, rid := range p.rawIDs {
			touched[rid] = true
		}
	}
	if len(touched) > 0 {
		ids := make([]int64, 0, len(touched))
		for id := range touched {
			ids = append(ids, id)
		}
		_, _ = s.db.Exec(ctx, `DELETE FROM memory_raw r WHERE r.id=ANY($1)
			AND NOT EXISTS (SELECT 1 FROM memory_pending_facts p WHERE r.id = ANY(p.raw_ids) AND NOT p.given_up)`, ids)
	}
	return n, firstErr
}

// PendingFacts lists facts still being retried or given up on, for visibility (Stats / the Memory page).
type PendingFact struct {
	ID        int64     `json:"id"`
	Bank      string    `json:"bank"`
	Text      string    `json:"text"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error"`
	GivenUp   bool      `json:"given_up"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Service) PendingFacts(ctx context.Context) ([]PendingFact, error) {
	rows, err := s.db.Query(ctx, `SELECT id,bank,text,attempts,last_error,given_up,created_at FROM memory_pending_facts ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingFact{}
	for rows.Next() {
		var p PendingFact
		if err := rows.Scan(&p.ID, &p.Bank, &p.Text, &p.Attempts, &p.LastError, &p.GivenUp, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// rawFactPolicy applies the source-trust ceiling: a fact distilled from tainted content (a web page, mail,
// an MCP result reached this conversation) is capped at 0.4 confidence and flagged unverified, whatever the
// extraction model itself estimated. Trust is a ceiling on confidence, not a number the model can talk its
// way past — this mirrors the same rule the live memory_store tool already applies (internal/memory/tools.go).
func rawFactPolicy(tainted bool, conf float64, tags []string, source string) (float64, []string, string) {
	if !tainted {
		return conf, tags, source
	}
	if conf > 0.4 || conf == 0 {
		conf = 0.4
	}
	return conf, append(append([]string{}, tags...), "unverified"), source + " (tainted)"
}

// namedBanks lists active project/domain banks for the extraction prompt, so the model reuses one instead of
// inventing a new, over-specific name for a topic that already has a bank (capped so the prompt stays small).
func (s *Service) namedBanks(ctx context.Context) string {
	bs, err := s.Banks(ctx)
	if err != nil {
		return ""
	}
	var sb strings.Builder
	n := 0
	for _, b := range bs {
		if (b.Kind != KindProject && b.Kind != KindDomain) || b.Status != "active" || n >= 40 {
			continue
		}
		fmt.Fprintf(&sb, "- %s", b.Label())
		if b.Description != "" {
			fmt.Fprintf(&sb, " — %s", b.Description)
		}
		sb.WriteByte('\n')
		n++
	}
	return sb.String()
}

// guessOwner names the agent a legacy raw message (recorded without one) is about: the non-user side.
func guessOwner(r rawRow) string {
	for _, n := range []string{r.to, r.from} {
		if n != "" && n != "user" {
			return n
		}
	}
	return "Atlas"
}

// distil extracts facts from one agent's raw messages and stores them; "profile" facts go to owner's bank.
// It returns (stored, pending, err): err is set only for a whole-batch failure (the model call or its JSON);
// a fact that failed to store individually is durably queued (see queuePendingFact) and counted in pending,
// never silently dropped.
func (s *Service) distil(ctx context.Context, owner string, raws []rawRow, project string) (stored, pending int, err error) {
	tainted := false
	var taskID int64 // best-effort provenance: the first task any raw message in this batch belonged to
	for _, r := range raws {
		if r.tainted {
			tainted = true
		}
		if taskID == 0 {
			taskID = r.taskID
		}
	}
	var sb strings.Builder
	sb.WriteString(s.guidance(ctx))
	fmt.Fprintf(&sb, "Agent whose profile bank is \"profile\": %s\n\n", owner)
	if project != "" {
		fmt.Fprintf(&sb, "This conversation is focused on the project bank %q: file facts about the work being discussed there (unless they clearly belong to another project or domain); facts about the user personally still go to \"user\".\n\n", project)
	}
	if names := s.namedBanks(ctx); names != "" {
		fmt.Fprintf(&sb, "Existing project/domain banks (reuse one of these when a fact fits):\n%s\n\n", names)
	}
	for _, r := range raws {
		t := r.text
		if len(t) > 1500 {
			t = t[:1500] + "…"
		}
		fmt.Fprintf(&sb, "[%s] %s → %s: %s\n", r.at.Format("2006-01-02 15:04"), r.from, r.to, t)
	}
	var parsed struct {
		Facts []struct {
			Text       string   `json:"text"`
			Bank       string   `json:"bank"`
			Tags       []string `json:"tags"`
			Confidence float64  `json:"confidence"`
		} `json:"facts"`
	}
	if err := s.llm.CompleteJSON(ctx, "role:fast", extractPrompt, sb.String(), &parsed); err != nil {
		return 0, 0, fmt.Errorf("memory extraction: %w", err)
	}
	ids := make([]int64, len(raws))
	for i, r := range raws {
		ids[i] = r.id
	}
	for _, f := range parsed.Facts {
		if strings.TrimSpace(f.Text) == "" {
			continue
		}
		bank := strings.TrimSpace(f.Bank)
		if bank == "" {
			bank = "user"
			if project != "" {
				bank = project
			}
		}
		if _, _, _, err := ParseSpec(bank, owner); err != nil {
			bank = "domain:General"
		}
		conf, tags, src := rawFactPolicy(tainted, f.Confidence, f.Tags, "raw")
		if _, err := s.Store(ctx, StoreReq{Bank: bank, Agent: owner, Text: f.Text, Tags: tags, Source: src, Confidence: conf, TaskID: taskID}); err != nil {
			s.queuePendingFact(ctx, ids, bank, owner, f.Text, tags, conf, tainted, err)
			pending++
			continue
		}
		stored++
	}
	return stored, pending, nil
}

// ── consolidation ──────────────────────────────────────────────────────────

// Prune archives long-unused, low-rank facts into history (valid_to set) and
// removes stale superseded facts older than keepHistory. Returns (archived, purged).
func (s *Service) Prune(ctx context.Context, keepHistory time.Duration) (int, int, error) {
	t1, err := s.db.Exec(ctx, `UPDATE memory_facts SET valid_to=now()
		WHERE valid_to IS NULL AND NOT pinned AND rank<0.25 AND COALESCE(last_used,created_at) < now()-interval '90 days'`)
	if err != nil {
		return 0, 0, err
	}
	t2, err := s.db.Exec(ctx, `DELETE FROM memory_facts WHERE valid_to IS NOT NULL AND valid_to < $1`, time.Now().Add(-keepHistory))
	if err != nil {
		return int(t1.RowsAffected()), 0, err
	}
	return int(t1.RowsAffected()), int(t2.RowsAffected()), nil
}

// Stats summarises the memory system for the status bar / UI.
type Stats struct {
	Banks int  `json:"banks"`
	Facts int  `json:"facts"`
	Raw   int  `json:"raw"`
	Embed bool `json:"embedding"`
	// Pending counts facts still being retried after a Store failure; GivenUp counts ones that hit the retry
	// cap and stopped — both are otherwise invisible (see memory_pending_facts / PendingFacts).
	Pending int `json:"pending"`
	GivenUp int `json:"given_up"`
}

func (s *Service) Stats(ctx context.Context) Stats {
	var st Stats
	_ = s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM memory_banks), (SELECT count(*) FROM memory_facts WHERE valid_to IS NULL),
		(SELECT count(*) FROM memory_raw WHERE NOT processed), (SELECT count(*) FROM memory_pending_facts WHERE NOT given_up),
		(SELECT count(*) FROM memory_pending_facts WHERE given_up)`).
		Scan(&st.Banks, &st.Facts, &st.Raw, &st.Pending, &st.GivenUp)
	st.Embed = s.llm.HasEmbedding(ctx)
	return st
}

var _ = llm.ErrNoModel
