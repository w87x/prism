package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"prism/internal/cron"
	"prism/internal/memory"
	"prism/internal/settings"
	"prism/internal/textmatch"
	"prism/internal/tracker"
)

// Job hygiene. Agents create scheduled jobs one at a time, and nobody reads them together afterwards: "check Avito price for X", "…for
// Y" and "…for Z" run as three jobs; a job whose approach keeps failing keeps being run as written although memory has long said what
// works instead. This reviews them:
//
//	merge    near-duplicate jobs that are the same check over different items become one
//	rewrite  a job that keeps failing is rewritten from the evidence of its runs, its lessons and what memory knows
//
// Each review ends as a proposal. What happens to it follows the autonomy dial (Settings.Autonomy.JobsMode): safe — it waits in Today;
// guarded — a small rewrite is applied at once, merges wait; armed — applied at once. Every change keeps the old version
// (cron_history), so it can be undone.

type JobProposal struct {
	ID        int64    `json:"id"`
	Kind      string   `json:"kind"`
	CronIDs   []int64  `json:"cron_ids"`
	Names     []string `json:"names"`
	Title     string   `json:"title"`
	NewPrompt string   `json:"new_prompt"`
	NewExpr   string   `json:"new_expr"`
	OldPrompt string   `json:"old_prompt"`
	Rationale string   `json:"rationale"`
	Evidence  string   `json:"evidence"`
	Status    string   `json:"status"`
	Auto      bool     `json:"auto"`
	// Plan is the tracker a "tracker" proposal would create; NewAgent the agent that should run its refresh job.
	Plan        json.RawMessage `json:"plan,omitempty"`
	NewAgent    string          `json:"new_agent,omitempty"`
	TrackerName string          `json:"tracker_name,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	DecidedAt   *time.Time      `json:"decided_at,omitempty"`
}

func (s *Service) JobProposals(ctx context.Context, status string) ([]JobProposal, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, kind, cron_ids, title, new_prompt, new_expr, rationale, evidence, status, auto, plan, new_agent, tracker_name, created_at, decided_at FROM job_proposals
		WHERE ($1='' OR status=$1) ORDER BY id DESC LIMIT 100`, status)
	if err != nil {
		return nil, err
	}
	var out []JobProposal
	for rows.Next() {
		var p JobProposal
		if rows.Scan(&p.ID, &p.Kind, &p.CronIDs, &p.Title, &p.NewPrompt, &p.NewExpr, &p.Rationale, &p.Evidence, &p.Status, &p.Auto, &p.Plan, &p.NewAgent, &p.TrackerName, &p.CreatedAt, &p.DecidedAt) == nil {
			out = append(out, p)
		}
	}
	rows.Close()
	for i := range out {
		for j, id := range out[i].CronIDs {
			var name, prompt string
			if s.DB.QueryRow(ctx, `SELECT name, prompt FROM crons WHERE id=$1`, id).Scan(&name, &prompt) == nil {
				out[i].Names = append(out[i].Names, fmt.Sprintf("#%d %s", id, name))
				if j == 0 || out[i].Kind == "rewrite" {
					out[i].OldPrompt = prompt
				}
			}
		}
	}
	return out, nil
}

// jobsDistance is how different two prompts are, 0 (same words) to 1 (nothing in common).
func jobsDistance(a, b string) float64 {
	set := func(x string) map[string]bool {
		m := map[string]bool{}
		for _, t := range textmatch.Tokens(x) {
			if len(t) > 2 {
				m[t] = true
			}
		}
		return m
	}
	sa, sb := set(a), set(b)
	if len(sa) == 0 || len(sb) == 0 {
		return 1
	}
	inter := 0
	for t := range sa {
		if sb[t] {
			inter++
		}
	}
	return 1 - float64(inter)/float64(len(sa)+len(sb)-inter)
}

const mergePrompt = `You manage a user's scheduled jobs. Below are several jobs that look alike. Decide whether they are really ONE kind of check run over different items (or plain duplicates), which should be a single job.
If yes: write the single merged prompt. It must cover every item, keep every threshold, condition and instruction of each job, keep telling the agent to reply exactly NO_REPLY when there is nothing to report, and name each item explicitly. Choose the schedule (cron expression) that serves the most demanding of them.
If any job needs different logic, a different agent's tools or a different schedule for a real reason, answer same=false.
Answer JSON only: {"same":true|false,"prompt":"...","expr":"...","why":"one or two sentences"}`

const trackerPrompt = `Several scheduled jobs of a user's AI assistant check the same kind of thing for different items. Decide whether they should become ONE TRACKER (a table with a row per item, refreshed by a single job) instead of one merged prompt.
Say fit=true only if there are 3 or more items and each has values that change over time and are worth comparing or keeping a history of (prices, counts, status, availability). Say fit=false for a plain "check and tell me" job without values to keep.
If fit: columns — the first is "item" (string); then only the typed values the jobs really look at (type string, number, boolean or time; unit for a number, like USD). items — one per item across all the jobs, key = the item's name exactly, data = {"item": name} plus any fixed attribute that has a column. prompt — the standing prompt of the single refresh job: for each listed item look up its current values by the method the original jobs describe; then ONE tracker_snapshot call (this tracker, run_id = the current date and time, complete=true, a row per item keyed by the item name exactly as listed; leave out what could not be observed, never invent); then look at tracker_changes and tell the user only what matters under the original jobs' conditions and thresholds (keep every one), otherwise reply exactly NO_REPLY. Name the tracker and list the items inside the prompt. expr — the schedule that serves the most demanding job.
Answer JSON only: {"fit":true|false,"name":"short tracker name","description":"one line","columns":[{"name":"item","type":"string"},{"name":"price","type":"number","unit":"USD"}],"items":[{"key":"…","data":{"item":"…"}}],"prompt":"…","expr":"…","why":"one or two sentences"}`

// trackerPlan is what a "tracker" proposal would create.
type trackerPlan struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Columns     []tracker.Column `json:"columns"`
	Items       []struct {
		Key  string         `json:"key"`
		Data map[string]any `json:"data"`
	} `json:"items"`
}

const rewritePrompt = `You review one scheduled job of a user's AI assistant. You get its prompt, how its recent runs went, lessons already learned for the agent that runs it, and facts memory holds that relate to it.
Decide whether the prompt should change so the job stops failing or uses what was learned — for example an approach known to fail replaced by the one that worked, a missing detail added, a wrong assumption fixed. Change only what the evidence supports. Keep the job's purpose, its conditions and thresholds, and the instruction to reply exactly NO_REPLY when there is nothing to report.
If the evidence does not point to a prompt problem (the site was down, the model failed, nothing is learned yet), answer revise=false.
Answer JSON only: {"revise":true|false,"prompt":"the complete new prompt","why":"one or two sentences citing the evidence"}`

type jobRow struct {
	ID      int64
	Name    string
	Agent   string
	Expr    string
	Prompt  string
	Enabled bool
}

func (s *Service) activeJobs(ctx context.Context) []jobRow {
	rows, err := s.DB.Query(ctx, `SELECT id, name, agent, expr, prompt, enabled FROM crons WHERE NOT system AND enabled AND finished_at IS NULL ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []jobRow
	for rows.Next() {
		var j jobRow
		if rows.Scan(&j.ID, &j.Name, &j.Agent, &j.Expr, &j.Prompt, &j.Enabled) == nil {
			out = append(out, j)
		}
	}
	return out
}

func (s *Service) hasOpenProposal(ctx context.Context, id int64) bool {
	var n int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM job_proposals WHERE $1=ANY(cron_ids) AND (status='pending' OR (status='dismissed' AND decided_at > now()-interval '14 days'))`, id).Scan(&n)
	return n > 0
}

// ReviewJobs looks for jobs to merge or rewrite and files proposals (applying them per the dial). It returns how many it made.
func (s *Service) ReviewJobs(ctx context.Context) (int, error) {
	if s.Engine == nil || s.Engine.LLM == nil || s.Engine.LLM.RoleRef(ctx, "chat") == "" {
		return 0, errors.New("no model configured")
	}
	made := 0
	jobs := s.activeJobs(ctx)

	// merge: cluster by prompt similarity
	parent := map[int64]int64{}
	var find func(int64) int64
	find = func(x int64) int64 {
		if parent[x] == 0 || parent[x] == x {
			parent[x] = x
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	for i := range jobs {
		for k := i + 1; k < len(jobs); k++ {
			if jobsDistance(jobs[i].Prompt, jobs[k].Prompt) <= 0.5 {
				parent[find(jobs[k].ID)] = find(jobs[i].ID)
			}
		}
	}
	clusters := map[int64][]jobRow{}
	for _, j := range jobs {
		clusters[find(j.ID)] = append(clusters[find(j.ID)], j)
	}
	var roots []int64
	for r, c := range clusters {
		if len(c) >= 2 {
			roots = append(roots, r)
		}
	}
	sort.Slice(roots, func(a, b int) bool { return roots[a] < roots[b] })
	for _, r := range roots {
		c := clusters[r]
		if len(c) > 6 {
			c = c[:6]
		}
		skip := false
		for _, j := range c {
			skip = skip || s.hasOpenProposal(ctx, j.ID)
		}
		if skip {
			continue
		}
		var sb strings.Builder
		var ids []int64
		var names []string
		for _, j := range c {
			fmt.Fprintf(&sb, "JOB #%d %q — agent %s, schedule %s\nPROMPT: %s\n\n", j.ID, j.Name, j.Agent, j.Expr, j.Prompt)
			ids = append(ids, j.ID)
			names = append(names, j.Name)
		}
		var v struct {
			Same   *bool  `json:"same"`
			Prompt string `json:"prompt"`
			Expr   string `json:"expr"`
			Why    string `json:"why"`
		}
		cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		err := s.Engine.LLM.CompleteJSON(cctx, s.Engine.LLM.ActionRef(cctx, "jobs_review", "chat"), mergePrompt, sb.String(), &v)
		cancel()
		if err != nil || v.Same == nil || !*v.Same || strings.TrimSpace(v.Prompt) == "" {
			continue
		}
		if e := strings.TrimSpace(v.Expr); e != "" {
			if _, perr := cron.Parse(e); perr != nil {
				v.Expr = ""
			}
		}
		if len(c) >= 3 {
			if id, ok := s.proposeTracker(ctx, c, sb.String(), ids, names); ok {
				made++
				s.settle(ctx, id)
				continue
			}
		}
		id, err := s.addJobProposal(ctx, "merge", ids, "Merge "+strings.Join(names, ", "), v.Prompt, v.Expr, v.Why, "")
		if err == nil {
			made++
			s.settle(ctx, id)
		}
	}

	// rewrite: jobs whose runs keep going wrong
	for _, j := range s.activeJobs(ctx) {
		if s.hasOpenProposal(ctx, j.ID) {
			continue
		}
		evidence, troubled := s.jobEvidence(ctx, j)
		if !troubled {
			continue
		}
		var v struct {
			Revise *bool  `json:"revise"`
			Prompt string `json:"prompt"`
			Why    string `json:"why"`
		}
		cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		err := s.Engine.LLM.CompleteJSON(cctx, s.Engine.LLM.ActionRef(cctx, "jobs_review", "chat"), rewritePrompt, "THE JOB #"+fmt.Sprint(j.ID)+" "+j.Name+" (agent "+j.Agent+", "+j.Expr+")\nPROMPT: "+j.Prompt+"\n\n"+evidence, &v)
		cancel()
		if err != nil || v.Revise == nil || !*v.Revise || strings.TrimSpace(v.Prompt) == "" || strings.TrimSpace(v.Prompt) == strings.TrimSpace(j.Prompt) {
			continue
		}
		id, err := s.addJobProposal(ctx, "rewrite", []int64{j.ID}, "Rewrite "+j.Name, v.Prompt, "", v.Why, evidence)
		if err == nil {
			made++
			s.settle(ctx, id)
		}
	}
	return made, nil
}

// trackerHolder picks the agent that will run a tracker's refresh job: the job's own agent if it holds the snapshot tool, else any
// enabled agent that does.
func (s *Service) trackerHolder(ctx context.Context, preferred string) string {
	has := func(tools []string) bool {
		a, b := false, false
		for _, t := range tools {
			a = a || t == "tracker_snapshot"
			b = b || t == "tracker_changes"
		}
		return a && b
	}
	if p, err := s.Engine.Profiles.Get(ctx, preferred); err == nil && p.Enabled && has(p.Tools) {
		return p.Name
	}
	if ps, err := s.Engine.Profiles.List(ctx); err == nil {
		for _, p := range ps {
			if p.Enabled && has(p.Tools) {
				return p.Name
			}
		}
	}
	return ""
}

// proposeTracker asks whether a cluster that is really one check should rather be a tracker with a row per item, and files that
// proposal if so.
func (s *Service) proposeTracker(ctx context.Context, c []jobRow, jobsText string, ids []int64, names []string) (int64, bool) {
	if s.Trackers == nil {
		return 0, false
	}
	holder := s.trackerHolder(ctx, c[0].Agent)
	if holder == "" {
		return 0, false
	}
	var v struct {
		Fit         *bool            `json:"fit"`
		Name        string           `json:"name"`
		Description string           `json:"description"`
		Columns     []tracker.Column `json:"columns"`
		Items       []struct {
			Key  string         `json:"key"`
			Data map[string]any `json:"data"`
		} `json:"items"`
		Prompt string `json:"prompt"`
		Expr   string `json:"expr"`
		Why    string `json:"why"`
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	err := s.Engine.LLM.CompleteJSON(cctx, s.Engine.LLM.ActionRef(cctx, "jobs_review", "chat"), trackerPrompt, jobsText, &v)
	cancel()
	if err != nil || v.Fit == nil || !*v.Fit || strings.TrimSpace(v.Name) == "" || strings.TrimSpace(v.Prompt) == "" || len(v.Items) < 2 || len(v.Columns) < 1 {
		return 0, false
	}
	if !strings.EqualFold(v.Columns[0].Name, "item") {
		v.Columns = append([]tracker.Column{{Name: "item", Type: "string"}}, v.Columns...)
	}
	if len(v.Columns) < 2 {
		return 0, false // a table of names only keeps nothing worth keeping
	}
	if e := strings.TrimSpace(v.Expr); e != "" {
		if _, perr := cron.Parse(e); perr != nil {
			v.Expr = ""
		}
	}
	plan := trackerPlan{Name: strings.TrimSpace(v.Name), Description: strings.TrimSpace(v.Description), Columns: v.Columns, Items: v.Items}
	pb, _ := json.Marshal(plan)
	var id int64
	err = s.DB.QueryRow(ctx, `INSERT INTO job_proposals (kind, cron_ids, title, new_prompt, new_expr, rationale, plan, new_agent) VALUES ('tracker',$1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		ids, "Track as a table: "+plan.Name+" ("+strings.Join(names, ", ")+")", strings.TrimSpace(v.Prompt), strings.TrimSpace(v.Expr), strings.TrimSpace(v.Why), pb, holder).Scan(&id)
	if err != nil {
		return 0, false
	}
	s.emit("jobs.proposal", map[string]any{"id": id, "title": plan.Name})
	return id, true
}

// jobEvidence gathers what bears on a job's prompt and whether there is reason to look at it at all.
func (s *Service) jobEvidence(ctx context.Context, j jobRow) (string, bool) {
	// only runs since the job was last changed count: failures of a prompt that has since been rewritten are not evidence against the new one
	var same int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM crons WHERE lower(name)=lower($1)`, j.Name).Scan(&same)
	if same > 1 {
		return "", false // runs are told apart by job name; with two jobs of one name they cannot be attributed
	}
	var since time.Time
	_ = s.DB.QueryRow(ctx, `SELECT COALESCE(max(at), 'epoch') FROM cron_history WHERE cron_id=$1`, j.ID).Scan(&since)
	rows, err := s.DB.Query(ctx, `SELECT status, left(COALESCE(NULLIF(error,''), result), 140), created_at FROM tasks WHERE from_kind='cron' AND from_name=$1 AND created_at > $2 ORDER BY id DESC LIMIT 8`, j.Name, since)
	if err != nil {
		return "", false
	}
	var sb strings.Builder
	bad, total := 0, 0
	sb.WriteString("RECENT RUNS (newest first):\n")
	for rows.Next() {
		var st, note string
		var at time.Time
		if rows.Scan(&st, &note, &at) == nil {
			total++
			if st == "failed" || st == "partial" {
				bad++
			}
			fmt.Fprintf(&sb, "- %s %s: %s\n", at.Format("2 Jan"), st, strings.Join(strings.Fields(note), " "))
		}
	}
	rows.Close()
	troubled := bad >= 2
	var rbNote, rbStatus string
	if s.DB.QueryRow(ctx, `SELECT status, last_note FROM runbooks WHERE key=$1`, "cron:"+j.Name).Scan(&rbStatus, &rbNote) == nil && rbStatus == "stale" {
		troubled = true
		fmt.Fprintf(&sb, "\nITS RUNBOOK WENT STALE: %s\n", rbNote)
	}
	if !troubled {
		return "", false
	}
	if abs, err := s.Engine.Antibodies(ctx); err == nil {
		first := true
		for _, a := range abs {
			if a.Agent == "*" || strings.EqualFold(a.Agent, j.Agent) {
				if first {
					sb.WriteString("\nLESSONS LEARNED FOR THIS AGENT:\n")
					first = false
				}
				sb.WriteString("- " + a.Rule + "\n")
			}
		}
	}
	if s.Engine.Memory != nil {
		if fs, err := s.Engine.Memory.Find(ctx, memory.FindReq{Query: j.Name + " " + j.Prompt, Banks: []string{"user", "profile"}, Agent: j.Agent, K: 8, NoReinforce: true, NoLinks: true}); err == nil && len(fs) > 0 {
			sb.WriteString("\nFACTS MEMORY HOLDS THAT MAY RELATE:\n")
			for _, f := range fs {
				sb.WriteString("- " + strings.Join(strings.Fields(f.Text), " ") + "\n")
			}
		}
	}
	return sb.String(), total > 0
}

func (s *Service) addJobProposal(ctx context.Context, kind string, ids []int64, title, prompt, expr, why, evidence string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO job_proposals (kind, cron_ids, title, new_prompt, new_expr, rationale, evidence) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		kind, ids, title, strings.TrimSpace(prompt), strings.TrimSpace(expr), strings.TrimSpace(why), evidence).Scan(&id)
	if err == nil {
		s.emit("jobs.proposal", map[string]any{"id": id, "title": title})
	}
	return id, err
}

// settle applies a fresh proposal if the dial says so.
func (s *Service) settle(ctx context.Context, id int64) {
	level := settings.Load(ctx, s.Settings, settings.KeyAutonomy, settings.DefaultAutonomy()).JobsLevel()
	ps, err := s.JobProposals(ctx, "pending")
	if err != nil {
		return
	}
	for _, p := range ps {
		if p.ID != id {
			continue
		}
		apply := level == "armed" || (level == "guarded" && p.Kind == "rewrite" && jobsDistance(p.OldPrompt, p.NewPrompt) <= 0.35)
		if apply {
			if err := s.ApplyJobProposal(ctx, id, true); err == nil && s.Notify != nil {
				s.Notify("autonomy", "info", "Scheduled job changed by itself", p.Title+" — "+p.Rationale+" (undo it in Autonomy → Team → Jobs)")
			}
		} else if s.Notify != nil {
			s.Notify("autonomy", "info", "A scheduled job could be improved", p.Title+" — "+p.Rationale)
		}
	}
}

// ApplyJobProposal carries a proposal out: a merge keeps the first job (with the merged prompt) and finishes the others; a rewrite
// changes the one job's prompt. What it changed is recorded so it can be undone.
func (s *Service) ApplyJobProposal(ctx context.Context, id int64, auto bool) error {
	var kind, prompt, expr string
	var ids []int64
	var status string
	if err := s.DB.QueryRow(ctx, `SELECT kind, cron_ids, new_prompt, new_expr, status FROM job_proposals WHERE id=$1`, id).Scan(&kind, &ids, &prompt, &expr, &status); err != nil {
		return errors.New("no such proposal")
	}
	if status != "pending" {
		return fmt.Errorf("proposal already %s", status)
	}
	if len(ids) == 0 {
		return errors.New("the proposal has no jobs")
	}
	keeper := ids[0]
	why := fmt.Sprintf("job proposal #%d (%s)", id, kind)
	patch := CronPatch{Prompt: &prompt}
	if expr != "" {
		patch.Expr = &expr
	}
	trackerName := ""
	if kind == "tracker" {
		var planRaw []byte
		var newAgent string
		_ = s.DB.QueryRow(ctx, `SELECT plan, new_agent FROM job_proposals WHERE id=$1`, id).Scan(&planRaw, &newAgent)
		var plan trackerPlan
		if err := json.Unmarshal(planRaw, &plan); err != nil || s.Trackers == nil || plan.Name == "" {
			return errors.New("this proposal has no usable tracker plan")
		}
		t, err := s.Trackers.Create(ctx, plan.Name, plan.Description, plan.Columns, "job review")
		if err != nil {
			return err
		}
		trackerName = t.Name
		for _, it := range plan.Items {
			if strings.TrimSpace(it.Key) == "" {
				continue
			}
			if it.Data == nil {
				it.Data = map[string]any{"item": it.Key}
			}
			if _, _, err := s.Trackers.UpsertRow(ctx, t.ID, strings.TrimSpace(it.Key), it.Data, ""); err != nil {
				_ = s.Trackers.Delete(ctx, t.Name)
				return fmt.Errorf("starting rows for the tracker: %w", err)
			}
		}
		if newAgent != "" {
			patch.Agent = &newAgent
		}
	}
	hist := []int64{}
	if h, err := s.recordHistory(ctx, keeper, why); err == nil {
		hist = append(hist, h)
	}
	if _, err := s.UpdateCron(ctx, keeper, patch); err != nil {
		if trackerName != "" {
			_ = s.Trackers.Delete(ctx, trackerName)
		}
		return err
	}
	if kind == "merge" || kind == "tracker" {
		for _, other := range ids[1:] {
			if h, err := s.recordHistory(ctx, other, why); err == nil {
				hist = append(hist, h)
			}
			_ = s.FinishCron(ctx, other, fmt.Sprintf("merged into #%d", keeper))
		}
	}
	_, err := s.DB.Exec(ctx, `UPDATE job_proposals SET status='applied', auto=$2, decided_at=now(), undo_ids=$3, tracker_name=$4 WHERE id=$1`, id, auto, hist, trackerName)
	return err
}

func (s *Service) DismissJobProposal(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE job_proposals SET status='dismissed', decided_at=now() WHERE id=$1 AND status='pending'`, id)
	return err
}

// UndoJobProposal puts every job a proposal changed back as it was.
func (s *Service) UndoJobProposal(ctx context.Context, id int64) error {
	var undo []int64
	var status string
	if err := s.DB.QueryRow(ctx, `SELECT undo_ids, status FROM job_proposals WHERE id=$1`, id).Scan(&undo, &status); err != nil || status != "applied" {
		return errors.New("nothing to undo")
	}
	for i := len(undo) - 1; i >= 0; i-- {
		var cid int64
		var c Cron
		if s.DB.QueryRow(ctx, `SELECT cron_id, name, agent, expr, prompt, enabled FROM cron_history WHERE id=$1`, undo[i]).Scan(&cid, &c.Name, &c.Agent, &c.Expr, &c.Prompt, &c.Enabled) != nil {
			continue
		}
		c.ID = cid
		c.Enabled = true // a job that was merged away comes back switched on
		if _, err := s.SaveCronWhy(ctx, c, fmt.Sprintf("undo of job proposal #%d", id)); err != nil {
			return err
		}
	}
	_, err := s.DB.Exec(ctx, `UPDATE job_proposals SET status='undone', decided_at=now() WHERE id=$1`, id)
	return err
}

func (s *Service) recordHistory(ctx context.Context, cronID int64, reason string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `INSERT INTO cron_history (cron_id, name, agent, expr, prompt, enabled, reason) SELECT id, name, agent, expr, prompt, enabled, $2 FROM crons WHERE id=$1 RETURNING id`, cronID, reason).Scan(&id)
	return id, err
}

// maybeReviewJobs runs the review about once a week.
func (s *Service) maybeReviewJobs(ctx context.Context) {
	if s.Engine == nil || s.Engine.LLMDown() {
		return
	}
	var lastRun time.Time
	if ok, _ := s.Settings.Get(ctx, "jobs_review_at", &lastRun); ok && time.Since(lastRun) < 7*24*time.Hour {
		return
	}
	_ = s.Settings.Set(ctx, "jobs_review_at", time.Now())
	_, _ = s.ReviewJobs(ctx)
}
