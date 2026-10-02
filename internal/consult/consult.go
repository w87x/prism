// Package consult lets agents ask an outside model for a second opinion — typically "write me a detailed
// plan for X" — using subscriptions rather than API keys: the Codex CLI (logged in with ChatGPT) or the
// ChatGPT web UI driven through PRISM's persistent browser profile. Asking can take many minutes, so every
// question is a job: consult waits for it inline, but one that outlives the call keeps running and is
// collected later with consult_result.
package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"prism/internal/browser"
	"prism/internal/settings"
)

const (
	maxPrompt = 120000 // chars sent out
	maxAnswer = 100000 // chars kept
	keepJobs  = 50
)

// Job is one question to one outside model.
type Job struct {
	ID       int64      `json:"id"`
	Provider string     `json:"provider"`
	Agent    string     `json:"agent"`
	Prompt   string     `json:"prompt"` // first 300 chars, for listings
	Status   string     `json:"status"` // running | done | failed | cancelled
	Answer   string     `json:"answer,omitempty"`
	Error    string     `json:"error,omitempty"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`

	full   string
	cancel context.CancelFunc
	done   chan struct{}
}

type provider interface {
	ask(ctx context.Context, s *Service, cfg settings.Consult, prompt string) (string, error)
}

type Service struct {
	Settings *settings.Store
	Browser  *browser.Manager
	LLM      Completer // used by Tune to read page structure; may be nil
	DataDir  string
	Emit     func(kind string, data any)
	// Poll is how often the ChatGPT page is checked for a finished answer (default 2s).
	Poll time.Duration
	// Quiet is how long a page without explicit "done" selectors must stay unchanged to count as finished
	// (default 8s).
	Quiet time.Duration

	mu    sync.Mutex
	jobs  map[int64]*Job
	next  int64
	locks map[string]*sync.Mutex // one question at a time per provider (one chat tab, one Codex login)
}

// provider resolves a name to codex or a configured chat site.
func (s *Service) provider(cfg settings.Consult, name string) (provider, error) {
	if name == "codex" {
		return codexProvider{}, nil
	}
	site, err := findSite(cfg, name)
	if err != nil {
		return nil, err
	}
	return webProvider{site}, nil
}

func (s *Service) cfg(ctx context.Context) settings.Consult {
	return settings.Load(ctx, s.Settings, settings.KeyConsult, settings.Consult{})
}

func (s *Service) emit() {
	if s.Emit != nil {
		s.Emit("consult.update", nil)
	}
}

// DefaultProvider is Codex when its CLI is installed (no scraping, officially supported), else ChatGPT, else
// the first enabled site.
func (s *Service) DefaultProvider(ctx context.Context) string {
	cfg := s.cfg(ctx)
	if _, err := findCodex(cfg); err == nil {
		return "codex"
	}
	for _, st := range Sites(cfg) {
		if !st.Disabled && st.Name == "chatgpt" {
			return "chatgpt"
		}
	}
	for _, st := range Sites(cfg) {
		if !st.Disabled {
			return st.Name
		}
	}
	return "chatgpt"
}

// Options override the configured defaults for one question. Only Codex honours them.
type Options struct {
	Model  string // e.g. gpt-5-codex
	Effort string // minimal | low | medium | high | xhigh
}

var efforts = map[string]bool{"minimal": true, "low": true, "medium": true, "high": true, "xhigh": true}

// Start queues a question and returns immediately; the job runs in the background.
func (s *Service) Start(ctx context.Context, providerName, agent, prompt string, limit time.Duration, opt Options) (*Job, error) {
	cfg := s.cfg(ctx)
	p, err := s.provider(cfg, providerName)
	if err != nil {
		return nil, err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, errors.New("empty prompt")
	}
	if len(prompt) > maxPrompt {
		return nil, fmt.Errorf("prompt is %d chars; the limit is %d — attach less context", len(prompt), maxPrompt)
	}
	if limit <= 0 {
		limit = 30 * time.Minute
	}
	opt.Model, opt.Effort = strings.TrimSpace(opt.Model), strings.ToLower(strings.TrimSpace(opt.Effort))
	if (opt.Model != "" || opt.Effort != "") && providerName != "codex" {
		return nil, errors.New("model and effort can only be chosen with provider codex (a chat website uses whatever model is selected in your account there)")
	}
	if opt.Effort != "" && !efforts[opt.Effort] {
		return nil, fmt.Errorf("unknown effort %q (use minimal, low, medium, high or xhigh)", opt.Effort)
	}
	if strings.HasPrefix(opt.Model, "-") || strings.ContainsAny(opt.Model, " \t\n") {
		return nil, fmt.Errorf("invalid model name %q", opt.Model)
	}
	if opt.Model != "" {
		cfg.CodexModel = opt.Model
	}
	if opt.Effort != "" {
		cfg.CodexEffort = opt.Effort
	}

	jctx, cancel := context.WithTimeout(context.Background(), limit)
	s.mu.Lock()
	if s.jobs == nil {
		s.jobs, s.locks = map[int64]*Job{}, map[string]*sync.Mutex{}
	}
	s.next++
	short := prompt
	if len(short) > 300 {
		short = short[:300] + "…"
	}
	j := &Job{ID: s.next, Provider: providerName, Agent: agent, Prompt: short, Status: "running", Started: time.Now(),
		full: prompt, cancel: cancel, done: make(chan struct{})}
	s.jobs[j.ID] = j
	lock := s.locks[providerName]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[providerName] = lock
	}
	s.prune()
	s.mu.Unlock()
	s.emit()

	go func() {
		defer cancel()
		lock.Lock() // queued jobs wait here; the limit still counts down, which is the honest behaviour
		defer lock.Unlock()
		var ans string
		var err error
		if jctx.Err() == nil {
			ans, err = p.ask(jctx, s, cfg, j.full)
		} else {
			err = jctx.Err()
		}
		s.mu.Lock()
		now := time.Now()
		j.Finished = &now
		switch {
		case errors.Is(jctx.Err(), context.Canceled) && j.Status == "cancelled":
		case err != nil:
			j.Status, j.Error = "failed", err.Error()
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(jctx.Err(), context.DeadlineExceeded) {
				j.Error = "timed out after " + limit.String()
			}
		default:
			j.Status = "done"
			j.Answer = clip(strings.TrimSpace(ans), maxAnswer)
		}
		j.full = ""
		s.mu.Unlock()
		close(j.done)
		s.emit()
	}()
	return j, nil
}

// Wait blocks until the job ends or d elapses (or ctx is done) and returns a snapshot.
func (s *Service) Wait(ctx context.Context, id int64, d time.Duration) (Job, error) {
	s.mu.Lock()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		return Job{}, fmt.Errorf("no consult job %d (jobs are forgotten when PRISM restarts)", id)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-j.done:
	case <-t.C:
	case <-ctx.Done():
	}
	return s.Get(id)
}

func (s *Service) Get(id int64) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("no consult job %d", id)
	}
	return *j, nil
}

// List returns jobs newest first.
func (s *Service) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		c := *j
		c.Answer = "" // listings stay small; consult_result has the text
		out = append(out, c)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
	return out
}

func (s *Service) Cancel(id int64) error {
	s.mu.Lock()
	j, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("no consult job %d", id)
	}
	if j.Status != "running" {
		s.mu.Unlock()
		return fmt.Errorf("job %d already %s", id, j.Status)
	}
	j.Status = "cancelled"
	cancel := j.cancel
	s.mu.Unlock()
	cancel()
	return nil
}

// prune drops the oldest finished jobs beyond keepJobs. Caller holds mu.
func (s *Service) prune() {
	if len(s.jobs) <= keepJobs {
		return
	}
	ids := make([]int64, 0, len(s.jobs))
	for id, j := range s.jobs {
		if j.Status != "running" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	for _, id := range ids {
		if len(s.jobs) <= keepJobs {
			return
		}
		delete(s.jobs, id)
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[truncated]"
}

// Where a GUI-launched PRISM may not have the shell's PATH, look in the usual install spots too.
func findCodex(cfg settings.Consult) (string, error) {
	bin := cfg.CodexBin
	if bin == "" {
		bin = "codex"
	}
	if strings.ContainsRune(bin, filepath.Separator) {
		if _, err := os.Stat(bin); err != nil {
			return "", fmt.Errorf("codex binary %s: %w", bin, err)
		}
		return bin, nil
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local/bin"),
		filepath.Join(home, ".npm-global/bin"), filepath.Join(home, ".bun/bin"), filepath.Join(home, ".volta/bin")} {
		p := filepath.Join(d, bin)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("the Codex CLI was not found — install it (brew install codex, or npm i -g @openai/codex), run `codex login` and choose Sign in with ChatGPT, or set its path in Settings")
}

// Status is what the Settings panel shows: whether Codex is installed, the effective chat sites, recent jobs.
func (s *Service) Status(ctx context.Context) map[string]any {
	cfg := s.cfg(ctx)
	codex := map[string]any{"found": true}
	if p, err := findCodex(cfg); err != nil {
		codex = map[string]any{"found": false, "detail": err.Error()}
	} else {
		codex["path"] = p
	}
	return map[string]any{"codex": codex, "sites": Sites(cfg), "jobs": s.List(), "default": s.DefaultProvider(ctx)}
}

// Test asks a provider a trivial question and reports whether a sensible answer came back.
func (s *Service) Test(ctx context.Context, name string) (map[string]any, error) {
	start := time.Now()
	j, err := s.Start(ctx, name, "settings", "Reply with exactly one word: PONG", 4*time.Minute, Options{})
	if err != nil {
		return nil, err
	}
	snap, err := s.Wait(ctx, j.ID, 4*time.Minute)
	if err != nil {
		return nil, err
	}
	if snap.Status == "running" {
		_ = s.Cancel(j.ID)
		snap.Status, snap.Error = "failed", "no answer in time"
	}
	return map[string]any{"ok": snap.Status == "done", "status": snap.Status, "answer": clip(snap.Answer, 400),
		"error": snap.Error, "ms": time.Since(start).Milliseconds(), "pong": strings.Contains(strings.ToUpper(snap.Answer), "PONG")}, nil
}
