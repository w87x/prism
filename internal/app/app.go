// Package app wires PRISM's services together and owns their lifecycle. Before a
// database is configured the app runs in "setup mode" and exposes only the setup RPCs.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/agent"
	"prism/internal/config"
	"prism/internal/db"
	"prism/internal/harvest"
	"prism/internal/hub"
	"prism/internal/llm"
	"prism/internal/memory"
	"prism/internal/metrics"
	"prism/internal/notify"
	"prism/internal/search"
	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/tasksum"
	"prism/internal/tools"
	"prism/internal/tools/builtin"
)

const Version = "0.1.0"

type App struct {
	Cfg *config.Config
	Hub *hub.Hub

	mu     sync.RWMutex
	ready  bool
	cancel context.CancelFunc
	ctx    context.Context

	DB        *db.DB
	Settings  *settings.Store
	LLM       *llm.Router
	Tools     *tools.Registry
	Memory    *memory.Service
	Profiles  *agent.ProfileStore
	Sessions  *agent.SessionStore
	Tasks     *tasks.Store
	TaskSum   *tasksum.Store
	Engine    *agent.Engine
	Downloads *builtin.Downloader
	Processes *builtin.ProcessManager
	Notifs    *notify.Store
	Metrics   *metrics.Store
	Search    *search.Service

	Ext Extensions // optional subsystems (web, mcp, skills, scheduler, integrations)

	memStepV    atomic.Value // string: what the memory loop is doing right now ("" = idle)
	memPassAt   atomic.Int64 // unix seconds the last pass finished
	memNextAt   atomic.Int64 // unix seconds the next timer-driven pass is due
	memJobsMu   sync.Mutex
	memJobs     map[string]*MemoryJob
	memJobOrder []string
	memNudge    chan struct{} // wakes memoryLoop when facts/raw messages arrive (see Memory.OnNew)
	lastStatus  string
	logSeq      atomic.Int64
}

func New(cfg *config.Config, h *hub.Hub) *App {
	return &App{Cfg: cfg, Hub: h, memNudge: make(chan struct{}, 1)}
}

func (a *App) Ready() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ready
}

// Emit broadcasts an event to all UI clients.
func (a *App) Emit(typ string, data any) {
	a.Hub.Broadcast(typ, data)
	a.observe(typ, data)
}

// Logf records an entry in the logs table (when available) and stdout.
func (a *App) Logf(level, source, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[%s] %s: %s", level, source, msg)
	if a.DB == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = a.DB.Exec(ctx, `INSERT INTO logs(level,source,message) VALUES($1,$2,$3)`, level, source, msg)
		if a.logSeq.Add(1)%500 == 0 { // trim the table now and then, not on every line
			_, _ = a.DB.Exec(ctx, `DELETE FROM logs WHERE id < (SELECT COALESCE(max(id),0)-20000 FROM logs)`)
		}
	}()
	if level == "warn" || level == "error" {
		a.Emit("log", map[string]any{"level": level, "source": source, "message": msg})
	}
}

// Connect opens the database (running migrations) and starts every service.
func (a *App) Connect(ctx context.Context, dsn string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ready {
		return errors.New("already connected")
	}
	d, err := db.Open(ctx, dsn)
	if err != nil {
		return err
	}
	a.DB = d
	a.ctx, a.cancel = context.WithCancel(context.Background())
	if err := a.build(a.ctx); err != nil {
		a.cancel()
		d.Close()
		a.DB = nil
		return err
	}
	a.ready = true
	return nil
}

func (a *App) build(ctx context.Context) error {
	a.Settings = settings.New(a.DB.Pool)
	a.LLM = llm.NewRouter(a.DB.Pool, a.Settings)
	a.Tools = tools.NewRegistry(a.DB.Pool)
	a.Memory = memory.New(a.DB.Pool, a.LLM, a.Settings)
	a.Memory.VectorOn = a.DB.VectorOn
	a.Memory.OnChange = func() { a.Emit("memory.update", nil) }
	a.Memory.OnNew = func() {
		select {
		case a.memNudge <- struct{}{}:
		default:
		}
	}
	a.Search = search.New(a.DB.Pool)
	a.Profiles = agent.NewProfileStore(a.DB.Pool)
	a.Profiles.OnChange = func() { a.Emit("agents.update", nil) }
	a.Sessions = agent.NewSessionStore(a.DB.Pool)
	a.Tasks = tasks.NewStore(a.DB.Pool)
	a.TaskSum = &tasksum.Store{DB: a.DB.Pool}
	a.Notifs = &notify.Store{DB: a.DB.Pool}
	// asks live in memory: after a restart none can still be waiting, so their notifications are stale
	_, _ = a.DB.Exec(ctx, `UPDATE notifications SET read=true WHERE kind='ask' AND NOT read`)
	a.Tasks.OnChange = func(t tasks.Task) { a.Emit("task.update", t) }

	a.Engine = agent.NewEngine(agent.Deps{DB: a.DB.Pool, LLM: a.LLM, Tools: a.Tools, Profiles: a.Profiles, Sessions: a.Sessions,
		Tasks: a.Tasks, Memory: a.Memory, TaskSum: a.TaskSum, Settings: a.Settings, Emit: a.Emit, DefaultBanks: a.defaultBanks})
	a.Engine.OnTaskDone = a.reviewWorkspaces
	a.Memory.ChatProject = a.Engine.ChatProject

	// tools
	memory.RegisterTools(a.Tools, a.Memory, a.defaultBanks)
	tasksum.RegisterTools(a.Tools, a.TaskSum)
	a.Downloads, a.Processes = builtin.Register(a.Tools, builtin.Deps{DB: a.DB.Pool, Settings: a.Settings, LLM: a.LLM, DataDir: a.Cfg.DataDir, Emit: a.Emit,
		Notify: func(ctx context.Context, ag, text string) { a.Engine.Notify(ctx, agent.Notice{Agent: ag, Text: text}) }})
	a.Engine.SaveImage = func(ctx context.Context, name, mime string, data []byte) (int64, error) {
		art, err := builtin.SaveArtifact(ctx, builtin.Deps{DB: a.DB.Pool, DataDir: a.Cfg.DataDir, Emit: a.Emit}, name, mime, data, "user")
		if err != nil {
			return 0, err
		}
		return art.ID, nil
	}
	a.Engine.LoadImage = func(ctx context.Context, id int64) (string, []byte, error) {
		var mime, path string
		if err := a.DB.QueryRow(ctx, `SELECT mime,path FROM artifacts WHERE id=$1`, id).Scan(&mime, &path); err != nil {
			return "", nil, err
		}
		b, err := os.ReadFile(path)
		return mime, b, err
	}
	a.Engine.Export = func(ctx context.Context, name, content string) (string, error) {
		art, err := builtin.SaveArtifact(ctx, builtin.Deps{DB: a.DB.Pool, DataDir: a.Cfg.DataDir, Emit: a.Emit}, name, "text/markdown", []byte(content), "user")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Saved as artifact #%d: %s (Library → Artifacts)", art.ID, art.Name), nil
	}
	a.Engine.RegisterTools(a.Tools)
	a.Profiles.OnCreate = func(id int64, icon string) {
		if icon == "" {
			go a.Engine.AssignIcon(context.Background(), id)
		}
	}
	if err := a.buildExtensions(ctx); err != nil {
		return err
	}
	if err := a.Tools.Load(ctx); err != nil {
		return err
	}
	if err := a.Profiles.Seed(ctx); err != nil {
		return err
	}
	master := settings.Load(ctx, a.Settings, "tools_master", struct {
		Armed       bool `json:"armed"`
		IgnoreTaint bool `json:"ignore_taint"`
	}{})
	a.Tools.SetMaster(master.Armed)
	a.Tools.SetIgnoreTaint(master.IgnoreTaint)
	go a.Engine.AssignMissingIcons(a.ctx)
	a.LLM.OnUsage(func(model string, u llm.Usage) {
		a.Emit("llm.usage", map[string]any{"model": model, "in": u.Prompt, "out": u.Completion})
	})

	a.Metrics = &metrics.Store{DB: a.DB.Pool}
	a.LLM.OnCall(a.Metrics.RecordLLM)
	a.Engine.OnTool = a.Metrics.RecordTool
	a.LLM.OnBusy(func(n int, model string) { a.Emit("llm.busy", map[string]any{"active": n, "model": model}) })

	// workers
	if n, err := a.Processes.Reconcile(ctx); err == nil && n > 0 {
		a.Notify("error", "attention", "Background processes lost on restart", fmt.Sprintf("%d process(es) were still running when PRISM stopped; their status is now unknown.", n))
	}
	a.Engine.SetLLMConcurrency(settings.Load(ctx, a.Settings, settings.KeyRuntime, settings.DefaultRuntime()).LLMConcurrency)
	a.Engine.StartDispatcher(ctx, 6)
	go a.memoryLoop(ctx)
	go a.cleanupLoop(ctx)
	go a.statusLoop(ctx)
	a.startExtensions(ctx)
	return nil
}

// defaultBanks are the banks an agent searches when memory_find gets no explicit list:
// the user bank, its own profile bank, and the banks configured on its profile.
func (a *App) defaultBanks(ctx context.Context, name string) []string {
	banks := []string{"user", "profile:" + name}
	if p, err := a.Profiles.Get(ctx, name); err == nil {
		banks = append(banks, p.Banks...)
	}
	// a chat focused on a project searches that project's bank instead of every project's
	if ch, topic, ok := agent.ChatFrom(ctx); ok && a.Engine != nil {
		if label := a.Engine.ChatProject(ctx, ch, topic); label != "" {
			return append(banks, label)
		}
	}
	// active project banks are cheap to include and often what a follow-up refers to
	if bs, err := a.Memory.Banks(ctx); err == nil {
		for _, b := range bs {
			if b.Kind == memory.KindProject && b.Status == "active" {
				banks = append(banks, b.Label())
			}
		}
	}
	return banks
}

// VerifyMemory sends an agent to check unverified facts and hypotheses against the web (see memory.VerifyBrief).
// ids limits it to specific facts (a manual request); otherwise it takes what is due, at most every three days and
// only when at least three items wait. It returns how many items were sent.
func (a *App) VerifyMemory(ctx context.Context, ids []int64, auto bool) (int, error) {
	type st struct {
		LastAt time.Time `json:"last_at"`
	}
	var items []memory.VerifyItem
	var err error
	if len(ids) > 0 {
		items, err = a.Memory.VerifyItems(ctx, ids)
	} else {
		if auto {
			if last := settings.Load(ctx, a.Settings, "memory.verify", st{}); time.Since(last.LastAt) < 3*24*time.Hour {
				return 0, nil
			}
		}
		items, err = a.Memory.VerifyQueue(ctx, 6)
		if auto && len(items) < 3 {
			return 0, err
		}
	}
	if err != nil || len(items) == 0 {
		return 0, err
	}
	title, input := a.Memory.VerifyBrief(items)
	from := "user"
	if auto {
		from = "system"
		input += "\nReply NO_REPLY after the last line if everything went as expected."
	}
	if _, err := a.Engine.Enqueue(ctx, tasks.Task{FromKind: from, FromName: "memory verification", ToAgent: "Atlas", Title: title, Input: input}); err != nil {
		return 0, err
	}
	var sent []int64
	for _, it := range items {
		sent = append(sent, it.ID)
	}
	a.Memory.MarkVerifyTried(ctx, sent)
	if auto {
		_ = a.Settings.Set(ctx, "memory.verify", st{LastAt: time.Now()})
	}
	return len(items), nil
}

// MemoryDigest writes the memory digest briefing; force ignores the schedule and the "nothing happened" check.
func (a *App) MemoryDigest(ctx context.Context, cfg settings.Memory, force bool) (bool, error) {
	type st struct {
		LastAt time.Time `json:"last_at"`
	}
	days := cfg.DigestDays
	if days <= 0 {
		days = 7
	}
	last := settings.Load(ctx, a.Settings, "memory.digest", st{})
	if !force {
		if cfg.DigestOff || (!last.LastAt.IsZero() && time.Since(last.LastAt) < time.Duration(days)*24*time.Hour) {
			return false, nil
		}
		if last.LastAt.IsZero() { // first run: start the clock, report after a full period
			return false, a.Settings.Set(ctx, "memory.digest", st{LastAt: time.Now()})
		}
	}
	since := last.LastAt
	if since.IsZero() || force {
		since = time.Now().AddDate(0, 0, -days)
	}
	d, err := a.Memory.Digest(ctx, since)
	if err != nil {
		return false, err
	}
	if !force {
		_ = a.Settings.Set(ctx, "memory.digest", st{LastAt: time.Now()})
	}
	if d.Empty() && !force {
		return false, nil
	}
	if a.Ext.Sched == nil {
		return false, nil
	}
	_, err = a.Ext.Sched.AddBriefing(ctx, "Mnemosyne", "Memory digest — "+time.Now().Format("2 Jan"), d.Markdown(), 2)
	return err == nil, err
}

// HarvestBookmarks bookmarks pages the agents visited (see harvest.Bookmarks); pages a plain fetch cannot read are
// handed to an agent that can use the browser and FlareSolverr fallbacks.
func (a *App) HarvestBookmarks(ctx context.Context) (int, error) {
	return harvest.Bookmarks(ctx, a.DB.Pool, a.LLM, a.Settings, func(ctx context.Context, title, input string) error {
		_, err := a.Engine.Enqueue(ctx, tasks.Task{FromKind: "system", FromName: "bookmark harvest", ToAgent: "Atlas", Title: title, Input: input})
		return err
	})
}

// reviewWorkspaces runs when a task finished: for each coding workspace it opened that has changes, the checks are
// run (if the agent did not) and the Reviewer is asked to judge the diff. The verdict shows up next to the workspace
// in Library → Code. It runs in the background so the finishing task is not held up.
func (a *App) reviewWorkspaces(ctx context.Context, t tasks.Task) {
	if t.ToAgent == "Reviewer" || settings.Load(ctx, a.Settings, settings.KeyGuardrails, settings.DefaultGuardrails()).CodeReviewOff {
		return
	}
	ws, err := builtin.OpenWorkspacesOfTask(ctx, a.DB.Pool, t.ID)
	if err != nil || len(ws) == 0 {
		return
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		for _, w := range ws {
			diff, err := builtin.WorkspaceDiff(bg, a.DB.Pool, w.ID, true)
			if err != nil || strings.HasPrefix(diff, "(no changes") {
				continue
			}
			if w.VerifyStatus == "" {
				if _, _, err := builtin.VerifyWorkspace(bg, a.DB.Pool, w.ID); err != nil {
					a.Logf("warn", "code", "checks of workspace #%d failed to run: %v", w.ID, err)
				}
			}
			cur, _ := builtin.Workspaces(bg, a.DB.Pool)
			checks := "not run"
			for _, c := range cur {
				if c.ID == w.ID && c.VerifyStatus != "" {
					checks = c.VerifyStatus + "\n" + c.VerifyOutput
				}
			}
			input := fmt.Sprintf("Review workspace #%d (%s, branch %s), work done by %s for this request:\n%s\n\nStart with workspace_diff(id=%d). The project checks reported: %s\n\nFollow your review method and finish with the single line VERDICT: approve, VERDICT: approve with fixes, or VERDICT: reject.", w.ID, filepath.Base(w.Repo), w.Branch, t.ToAgent, trimText(t.Input, 600), w.ID, checks)
			nt, err := a.Engine.Enqueue(bg, tasks.Task{FromKind: "user", FromName: "auto review", ToAgent: "Reviewer", Title: fmt.Sprintf("Review workspace #%d", w.ID), Input: input})
			if err != nil {
				a.Logf("warn", "code", "could not queue the review of workspace #%d: %v", w.ID, err)
				continue
			}
			_ = builtin.SetWorkspaceReview(bg, a.DB.Pool, w.ID, nt.ID)
			a.Emit("workspaces.update", map[string]any{"id": w.ID})
		}
	}()
}

// MemoryStatus tells what the memory maintenance loop is doing and when it last and next runs.
type MemoryStatus struct {
	Step   string      `json:"step"`
	LastAt int64       `json:"last_at"`
	NextAt int64       `json:"next_at"`
	Jobs   []MemoryJob `json:"jobs"`
}

// MemoryJob is the last outcome of one of the memory loop's jobs, so a job that never does anything (or keeps
// failing) is visible instead of silent.
type MemoryJob struct {
	Name   string `json:"name"`
	LastAt int64  `json:"last_at"`
	Result string `json:"result"`
	Error  string `json:"error"`
	Runs   int    `json:"runs"`
	Acted  int64  `json:"acted_at"` // last time it actually changed something
}

func (a *App) MemoryStatus() MemoryStatus {
	st, _ := a.memStepV.Load().(string)
	a.memJobsMu.Lock()
	jobs := make([]MemoryJob, 0, len(a.memJobs))
	for _, name := range a.memJobOrder {
		jobs = append(jobs, *a.memJobs[name])
	}
	a.memJobsMu.Unlock()
	return MemoryStatus{Step: st, LastAt: a.memPassAt.Load(), NextAt: a.memNextAt.Load(), Jobs: jobs}
}

// memJob records the outcome of one job of the loop; acted says it changed something.
func (a *App) memJob(name, result string, acted bool, err error) {
	a.memJobsMu.Lock()
	defer a.memJobsMu.Unlock()
	if a.memJobs == nil {
		a.memJobs = map[string]*MemoryJob{}
	}
	j := a.memJobs[name]
	if j == nil {
		j = &MemoryJob{Name: name}
		a.memJobs[name] = j
		a.memJobOrder = append(a.memJobOrder, name)
	}
	j.LastAt, j.Result, j.Error = time.Now().Unix(), result, ""
	j.Runs++
	if err != nil {
		j.Error = err.Error()
	}
	if acted {
		j.Acted = j.LastAt
	}
}

func (a *App) memStage(step string) {
	a.memStepV.Store(step)
	a.Emit("memory.step", map[string]any{"step": step})
}

func (a *App) memoryLoop(ctx context.Context) {
	for {
		cfg := settings.Load(ctx, a.Settings, settings.KeyMemory, settings.Memory{ProcessEvery: 300, RawBatch: 40})
		if cfg.ProcessEvery < 30 {
			cfg.ProcessEvery = 300
		}
		a.memNextAt.Store(time.Now().Add(time.Duration(cfg.ProcessEvery) * time.Second).Unix())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(cfg.ProcessEvery) * time.Second):
		case <-a.memNudge:
			// something new arrived: run soon (each job checks its own "after N" threshold), but let a burst of
			// facts/messages settle first so one pass covers it
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Second):
			}
		}
		select { // a nudge that arrived during the wait is already covered by this pass
		case <-a.memNudge:
		default:
		}
		off := "switched off"
		a.memStage("digesting raw messages")
		n, err := a.Memory.ProcessMin(ctx, cfg.RawBatch, cfg.ProcessMin, false)
		if err != nil {
			a.Logf("warn", "memory", "raw processing failed: %v", err)
		} else if n > 0 {
			a.Logf("info", "memory", "distilled %d facts from raw messages", n)
		}
		a.memJob("digest raw messages", fmt.Sprintf("%d facts", n), n > 0, err)

		a.memStage("summarizing finished tasks")
		sn, serr := a.Engine.SummarizeDue(ctx, 5)
		if serr != nil {
			a.Logf("warn", "memory", "task summarization failed: %v", serr)
		} else if sn > 0 {
			a.Logf("info", "memory", "summarized %d finished task(s)", sn)
		}
		a.memJob("summarize tasks", fmt.Sprintf("%d tasks", sn), sn > 0, serr)

		a.memStage("merging banks")
		if cfg.AutoMergeOff {
			a.memJob("merge banks", off, false, nil)
		} else {
			merged := 0
			var merr error
			if n > 0 { // only worth asking the model right after new facts may have created new banks
				ms, err := a.Memory.AutoMergeBanks(ctx)
				if err != nil {
					a.Logf("warn", "memory", "bank auto-merge failed: %v", err)
					merr = err
				}
				for _, r := range ms {
					merged++
					a.Logf("info", "memory", "auto-merged into %s: %d facts, %d duplicates collapsed", r.Bank.Label(), r.Moved, r.Dropped)
				}
			}
			ms, err := a.Memory.DedupeBanks(ctx) // spelling variants of one bank name need no model and no new facts
			if err != nil {
				a.Logf("warn", "memory", "bank de-duplication failed: %v", err)
				merr = err
			}
			for _, r := range ms {
				merged++
				a.Logf("info", "memory", "merged same-name banks into %s: %d facts, %d duplicates collapsed", r.Bank.Label(), r.Moved, r.Dropped)
			}
			a.memJob("merge banks", fmt.Sprintf("%d merges", merged), merged > 0, merr)
		}

		a.memStage("reflecting")
		if cfg.ReflectOff {
			a.memJob("reflection", off, false, nil)
		} else {
			rs, err := a.Memory.ReflectDue(ctx, cfg.ReflectAfter, 2)
			if err != nil {
				a.Logf("warn", "memory", "reflection failed: %v", err)
			}
			ch := 0
			for _, r := range rs {
				ch += r.Changes()
				if r.Changes() > 0 {
					a.Logf("info", "memory", "reflection — %s", r)
				}
			}
			a.memJob("reflection", fmt.Sprintf("%d banks due, %d changes", len(rs), ch), ch > 0, err)
		}

		a.memStage("deep analysis")
		if cfg.AnalyzeOff {
			a.memJob("deep analysis", off, false, nil)
			a.memJob("mental models", off, false, nil)
		} else {
			rs, err := a.Memory.AnalyzeDue(ctx, cfg.AnalyzeAfter, 1)
			if err != nil {
				a.Logf("warn", "memory", "deep analysis failed: %v", err)
			}
			ch := 0
			for _, r := range rs {
				ch += r.Changes()
				if r.Changes() > 0 {
					a.Logf("info", "memory", "analysis — %s", r)
				}
			}
			a.memJob("deep analysis", fmt.Sprintf("%d banks due, %d changes", len(rs), ch), ch > 0, err)
			if cfg.SynthOff {
				a.memJob("synthesis", off, false, nil)
			} else {
				a.memStage("synthesising")
				rs, err := a.Memory.SynthesizeDue(ctx, cfg.SynthAfter)
				if err != nil {
					a.Logf("warn", "memory", "synthesis failed: %v", err)
				}
				ch := 0
				for _, r := range rs {
					ch += r.Changes()
					if r.Changes() > 0 {
						a.Logf("info", "memory", "synthesis — %s", r)
					}
				}
				a.memJob("synthesis", fmt.Sprintf("%d changes", ch), ch > 0, err)
			}
			a.memStage("refreshing mental models") // models follow the analysis: they read its insights too
			ms, err := a.Memory.ModelsDue(ctx, 0, 2)
			if err != nil {
				a.Logf("warn", "memory", "mental model refresh failed: %v", err)
			}
			for _, m := range ms {
				a.Logf("info", "memory", "mental model refreshed — %s", m.Name)
			}
			a.memJob("mental models", fmt.Sprintf("%d refreshed", len(ms)), len(ms) > 0, err)
		}

		a.memStage("extracting entities")
		if cfg.EntitiesOff {
			a.memJob("entities", off, false, nil)
		} else {
			rs, err := a.Memory.EntitiesDue(ctx, cfg.EntitiesAfter, 2)
			if err != nil {
				a.Logf("warn", "memory", "entity extraction failed: %v", err)
			}
			cnt := 0
			for _, r := range rs {
				cnt += r.Entities + r.Relations
				if r.Entities+r.Relations > 0 {
					a.Logf("info", "memory", "entities — %s", r)
				}
			}
			a.memJob("entities", fmt.Sprintf("%d banks due, %d entities/relations", len(rs), cnt), cnt > 0, err)
		}

		if cfg.BookmarksOff {
			a.memJob("bookmarks", off, false, nil)
		} else {
			a.memStage("bookmarking visited pages")
			n, err := a.HarvestBookmarks(ctx)
			if err != nil {
				a.Logf("warn", "memory", "bookmark harvest failed: %v", err)
			} else if n > 0 {
				a.Logf("info", "memory", "bookmarked %d page(s) the agents visited", n)
			}
			a.memJob("bookmarks", fmt.Sprintf("%d added", n), n > 0, err)
		}

		if cfg.VerifyOff {
			a.memJob("verification", off, false, nil)
		} else {
			n, err := a.VerifyMemory(ctx, nil, true)
			if err != nil {
				a.Logf("warn", "memory", "verification request failed: %v", err)
			} else if n > 0 {
				a.Logf("info", "memory", "asked an agent to verify %d unverified claim(s)", n)
			}
			a.memJob("verification", fmt.Sprintf("%d claims sent", n), n > 0, err)
		}

		if a.Ext.Ingest != nil {
			n, err := a.Ext.Ingest.Scan(ctx, !cfg.AutoIngestOff)
			if err != nil {
				a.Logf("warn", "memory", "inbox scan failed: %v", err)
			} else if n > 0 {
				a.Logf("info", "memory", "started learning from %d new document(s) in the inbox", n)
			}
			a.memJob("inbox documents", fmt.Sprintf("%d started, %d waiting for you", n, len(a.Ext.Ingest.Waiting())), n > 0, err)
		}
		made, derr := a.MemoryDigest(ctx, cfg, false)
		if derr != nil {
			a.Logf("warn", "memory", "memory digest failed: %v", derr)
		} else if made {
			a.Logf("info", "memory", "memory digest briefing written")
		}
		if cfg.DigestOff {
			a.memJob("digest briefing", off, false, nil)
		} else {
			a.memJob("digest briefing", map[bool]string{true: "written", false: "not due"}[made], made, derr)
		}
		a.memPassAt.Store(time.Now().Unix())
		a.memStage("")
	}
}

// Close stops workers and closes the database.
func (a *App) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	a.stopExtensions()
	if a.DB != nil {
		a.DB.Close()
	}
	a.ready = false
}

func trimText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
