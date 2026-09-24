package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"prism/internal/tools"
)

// Process is the durable record of a background command: shell/python block until exit and cap output at
// ~20KB, which is fine for a quick command but not for a build, an index pass, or a long transfer — those
// need to be started, checked on later, fed input, and cancelled without tying up the agent's turn.
type Process struct {
	ID         int64      `json:"id"`
	TaskID     int64      `json:"task_id,omitempty"`
	Agent      string     `json:"agent"`
	Command    string     `json:"command"`
	Cwd        string     `json:"cwd"`
	Status     string     `json:"status"` // running | exited | killed | failed | unknown
	ExitCode   *int       `json:"exit_code,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

const procCols = `id,task_id,agent,command,cwd,status,exit_code,created_at,finished_at`

func scanProcess(row interface {
	Scan(dest ...any) error
}) (Process, error) {
	var p Process
	var taskID *int64
	err := row.Scan(&p.ID, &taskID, &p.Agent, &p.Command, &p.Cwd, &p.Status, &p.ExitCode, &p.CreatedAt, &p.FinishedAt)
	if taskID != nil {
		p.TaskID = *taskID
	}
	return p, err
}

// maxProcOut caps retained output per process — generous relative to shell's ~20KB single-call cap, since a
// background job is expected to run far longer and produce far more.
const maxProcOut = 200000

// outBuf is a concurrency-safe growing buffer: one goroutine streams the process's combined stdout/stderr
// into it while process_log calls read a consistent snapshot from any other goroutine.
type outBuf struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	trunc int
}

func (b *outBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := maxProcOut*2 - b.buf.Len()
	if room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
			b.trunc += len(p) - room
		} else {
			b.buf.Write(p)
		}
	} else {
		b.trunc += len(p)
	}
	return len(p), nil
}

func (b *outBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if len(s) > maxProcOut {
		s = s[:maxProcOut/2] + "\n…[output truncated]…\n" + s[len(s)-maxProcOut/2:]
	} else if b.trunc > 0 {
		s += "\n…[output truncated]"
	}
	return s
}

// running is the live handle for a process still tracked in this process's lifetime: its OS process, the
// pipe used to send it input, and its output buffer. Lost across a restart — see Reconcile.
type running struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *outBuf
	cancel context.CancelFunc
}

// ProcessManager starts and tracks background commands, mirroring Downloader's shape: DB-backed status
// survives a restart even though the OS process itself cannot be reattached to (see Reconcile).
type ProcessManager struct {
	d    Deps
	mu   sync.Mutex
	live map[int64]*running
}

func newProcessManager(d Deps) *ProcessManager {
	return &ProcessManager{d: d, live: map[int64]*running{}}
}

// Reconcile runs once at startup. Any row still 'running' from a previous process life has no live handle
// any more — the OS process was started with its own process group (Setpgid), so it may still be running as
// an orphan, or it may be long gone; PRISM lost track either way and must say so rather than claim either.
// Mirrors the same bug class already fixed for tasks (see pending_asks / ReconcilePendingAsks).
func (pm *ProcessManager) Reconcile(ctx context.Context) (int, error) {
	tag, err := pm.d.DB.Exec(ctx, `UPDATE bg_processes SET status='unknown', finished_at=now() WHERE status='running'`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (pm *ProcessManager) List(ctx context.Context, limit int) ([]Process, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := pm.d.DB.Query(ctx, `SELECT `+procCols+` FROM bg_processes ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Process
	for rows.Next() {
		p, err := scanProcess(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (pm *ProcessManager) Get(ctx context.Context, id int64) (Process, error) {
	return scanProcess(pm.d.DB.QueryRow(ctx, `SELECT `+procCols+` FROM bg_processes WHERE id=$1`, id))
}

// Log returns a character window of the process's combined output — the live buffer while running, the
// stored final output once it has finished — windowed the same way web_fetch/artifact_read page long text.
func (pm *ProcessManager) Log(ctx context.Context, id int64, offset, maxChars int) (string, int, error) {
	pm.mu.Lock()
	r, isLive := pm.live[id]
	pm.mu.Unlock()
	var full string
	if isLive {
		full = r.out.String()
	} else {
		if err := pm.d.DB.QueryRow(ctx, `SELECT output FROM bg_processes WHERE id=$1`, id).Scan(&full); err != nil {
			return "", 0, err
		}
	}
	runes := []rune(full)
	total := len(runes)
	if maxChars <= 0 {
		maxChars = 8000
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return "", total, nil
	}
	end := min(offset+maxChars, total)
	return string(runes[offset:end]), total, nil
}

// Input writes text (plus a newline) to a running process's stdin. Returns an error if the process has
// already finished or never accepted input (e.g. it closed stdin itself).
func (pm *ProcessManager) Input(ctx context.Context, id int64, text string) error {
	pm.mu.Lock()
	r, ok := pm.live[id]
	pm.mu.Unlock()
	if !ok || r.stdin == nil {
		return fmt.Errorf("process #%d is not running (or accepts no input)", id)
	}
	_, err := io.WriteString(r.stdin, strings.TrimRight(text, "\n")+"\n")
	return err
}

// Cancel stops a running process: SIGTERM first, then SIGKILL after a short grace period if it hasn't exited.
func (pm *ProcessManager) Cancel(ctx context.Context, id int64) error {
	pm.mu.Lock()
	r, ok := pm.live[id]
	pm.mu.Unlock()
	if !ok {
		tag, err := pm.d.DB.Exec(ctx, `UPDATE bg_processes SET status='unknown', finished_at=now() WHERE id=$1 AND status='running'`, id)
		if err == nil && tag.RowsAffected() > 0 {
			pm.d.Emit("process.update", map[string]any{"id": id, "status": "unknown"})
		}
		return err
	}
	if r.cmd.Process != nil {
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGTERM)
	}
	go func() {
		time.Sleep(5 * time.Second)
		pm.mu.Lock()
		still, ok := pm.live[id]
		pm.mu.Unlock()
		if ok && still.cmd.Process != nil {
			_ = syscall.Kill(-still.cmd.Process.Pid, syscall.SIGKILL)
		}
	}()
	r.cancel()
	return nil
}

// StartReq describes a command to launch in the background.
type StartReq struct {
	Command string
	Cwd     string
	TaskID  int64
	Agent   string
}

// Start launches the command and returns immediately with its id; the command keeps running after this
// call returns, unlike shell/python which block for the whole call.
func (pm *ProcessManager) Start(ctx context.Context, req StartReq) (Process, error) {
	if strings.TrimSpace(req.Command) == "" {
		return Process{}, errors.New("empty command")
	}
	work := filepath.Join(pm.d.DataDir, "work")
	_ = os.MkdirAll(work, 0o755)
	dir := work
	if req.Cwd != "" {
		dir = expandHome(req.Cwd)
	}
	p := Process{TaskID: req.TaskID, Agent: req.Agent, Command: req.Command, Cwd: dir, Status: "running"}
	var taskID any
	if req.TaskID != 0 {
		taskID = req.TaskID
	}
	if err := pm.d.DB.QueryRow(ctx, `INSERT INTO bg_processes(task_id,agent,command,cwd) VALUES($1,$2,$3,$4) RETURNING id,created_at`,
		taskID, req.Agent, req.Command, dir).Scan(&p.ID, &p.CreatedAt); err != nil {
		return p, err
	}

	// Deliberately NOT the calling tool call's ctx: the whole point is the command outlives this one agent
	// turn. Its own lifetime is controlled only by Cancel (or the process exiting on its own).
	rctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(rctx, "/bin/zsh", "-c", req.Command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &outBuf{}
	cmd.Stdout, cmd.Stderr = out, out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		pm.finish(context.WithoutCancel(ctx), p.ID, "failed", nil, out.String())
		return p, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		pm.finish(context.WithoutCancel(ctx), p.ID, "failed", nil, err.Error())
		return p, err
	}
	r := &running{cmd: cmd, stdin: stdin, out: out, cancel: cancel}
	pm.mu.Lock()
	pm.live[p.ID] = r
	pm.mu.Unlock()
	pm.d.Emit("process.update", map[string]any{"id": p.ID, "status": "running"})

	go pm.watch(p, r)
	return p, nil
}

// watch waits for the process to exit, periodically flushing its output so a restart mid-run loses at most
// a couple of seconds of transcript (see Reconcile for what a restart itself means for the process).
func (pm *ProcessManager) watch(p Process, r *running) {
	// Only this function closes stop; the ticker goroutine only ever reads it. (Closing from both ends —
	// the ticker's own exit path as well as here — double-closes it and panics.)
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				pm.flush(p.ID, r.out.String())
			}
		}
	}()
	werr := r.cmd.Wait()
	close(stop)

	pm.mu.Lock()
	delete(pm.live, p.ID)
	pm.mu.Unlock()

	status, code := "exited", 0
	if r.cmd.ProcessState != nil {
		code = r.cmd.ProcessState.ExitCode()
	}
	switch {
	case werr != nil && r.cmd.ProcessState != nil && !r.cmd.ProcessState.Exited():
		status = "killed"
	case code != 0:
		status = "exited" // still "exited": a nonzero exit is a normal, reportable outcome, not a PRISM-side failure
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pm.finishCode(ctx, p.ID, status, &code, r.out.String())
	pm.d.Emit("process.update", map[string]any{"id": p.ID, "status": status, "exit_code": code})
	// a command that finished while the agent was still waiting on it (or within seconds) needs no notice: only
	// long-running ones are worth interrupting the user for — otherwise every `ls` and `cat` became a message
	if pm.d.Notify != nil && time.Since(p.CreatedAt) > notifyAfter {
		text := fmt.Sprintf("Process #%d finished (%s): %s", p.ID, status, trim(p.Command, 80))
		if status == "exited" && code != 0 {
			text = fmt.Sprintf("Process #%d exited with code %d: %s", p.ID, code, trim(p.Command, 80))
		}
		pm.d.Notify(context.Background(), firstNonEmpty(p.Agent, "Atlas"), text)
	}
}

// notifyAfter is how long a background process must have run before its completion is announced to the user.
const notifyAfter = 30 * time.Second

func (pm *ProcessManager) flush(id int64, output string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = pm.d.DB.Exec(ctx, `UPDATE bg_processes SET output=$2 WHERE id=$1 AND status='running'`, id, output)
}

func (pm *ProcessManager) finish(ctx context.Context, id int64, status string, code *int, output string) {
	pm.finishCode(ctx, id, status, code, output)
}

func (pm *ProcessManager) finishCode(ctx context.Context, id int64, status string, code *int, output string) {
	_, _ = pm.d.DB.Exec(ctx, `UPDATE bg_processes SET status=$2, exit_code=$3, output=$4, finished_at=now() WHERE id=$1`, id, status, code, output)
}

func trim(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func registerProcess(reg *tools.Registry, pm *ProcessManager) {
	reg.Register(
		&tools.Tool{
			Name: "process_start", Category: "code", Risk: tools.RiskExec,
			Description: "Run a shell command as a background process. It waits up to wait_s seconds (default 15, max 120) for the command to finish: a quick command comes back with its exit code and output right in this call, a long one (a build, an index pass, a big transfer, yt-dlp, a dev server) keeps running and you get its id, progress and latest output instead. " +
				"For plain quick commands (ls, cat, grep, head) just use the shell tool. Follow a long one with process_status (wait_s makes it wait for news instead of polling), process_log, process_input or process_cancel; the user is notified when a long process ends. Do NOT start one process per tiny step — chain steps in one command.",
			Params: tools.Obj("command", tools.Str("command", "the command line (zsh)"), tools.Str("cwd", "working directory (default workspace)"),
				tools.Int("wait_s", "seconds to wait for it to finish before returning (default 15, max 120)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Command, Cwd string
					WaitS        int `json:"wait_s"`
				}](raw)
				if err != nil {
					return "", err
				}
				p, err := pm.Start(ctx, StartReq{Command: a.Command, Cwd: a.Cwd, TaskID: env.TaskID, Agent: env.Agent})
				if err != nil {
					return "", err
				}
				wait := a.WaitS
				if wait <= 0 {
					wait = 15
				}
				if wait > 120 {
					wait = 120
				}
				fp, out, err := pm.Wait(ctx, p.ID, time.Duration(wait)*time.Second)
				if err != nil {
					return "", err
				}
				if fp.Status == "running" {
					return fmt.Sprintf("Still running after %ds — it continues in the background and the user is notified when it ends. Follow it with process_status.\n%s", wait, describeProcess(fp, out)), nil
				}
				return describeProcess(fp, out), nil
			},
		},
		&tools.Tool{
			Name: "process_status", Category: "code", Risk: tools.RiskRead,
			Description: "Status of a background process by id — with its progress (percentage, speed, ETA when the output shows one) and latest output while running, its output once finished — or the list of recent ones (omit id).",
			Params:      tools.Obj("", tools.Int("id", "process id (omit to list recent)"), tools.Int("wait_s", "with an id: wait up to this many seconds (max 60) for it to finish before answering — use it instead of polling in a loop")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID    int64
					WaitS int `json:"wait_s"`
				}](raw)
				if err != nil {
					return "", err
				}
				if a.ID != 0 {
					w := min(max(a.WaitS, 0), 60)
					p, out, err := pm.Wait(ctx, a.ID, time.Duration(w)*time.Second)
					if err != nil {
						return "", err
					}
					return describeProcess(p, out), nil
				}
				ps, err := pm.List(ctx, 20)
				if err != nil {
					return "", err
				}
				if len(ps) == 0 {
					return "No background processes.", nil
				}
				var sb strings.Builder
				for _, p := range ps {
					sb.WriteString(formatProcess(p) + "\n")
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "process_log", Category: "code", Risk: tools.RiskRead,
			Description: "Output of a background process (combined stdout/stderr), paged by character offset — like a running or finished process's transcript.",
			Params:      tools.Obj("id", tools.Int("id", "process id"), tools.Int("offset", "character offset (default 0)"), tools.Int("max_chars", "max characters (default 8000)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID       int64
					Offset   int
					MaxChars int `json:"max_chars"`
				}](raw)
				if err != nil {
					return "", err
				}
				text, total, err := pm.Log(ctx, a.ID, a.Offset, a.MaxChars)
				if err != nil {
					return "", err
				}
				if a.Offset+len([]rune(text)) < total {
					text += fmt.Sprintf("\n…[%d more chars; continue with offset=%d]", total-a.Offset-len([]rune(text)), a.Offset+len([]rune(text)))
				}
				if text == "" {
					return "(no output yet)", nil
				}
				return text, nil
			},
		},
		&tools.Tool{
			Name: "process_input", Category: "code", Risk: tools.RiskExec,
			Description: "Send a line of text to a running background process's stdin (for a REPL, a prompt it's waiting on, etc.).",
			Params:      tools.Obj("id,text", tools.Int("id", "process id"), tools.Str("text", "text to send (a trailing newline is added)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID   int64
					Text string
				}](raw)
				if err != nil {
					return "", err
				}
				if err := pm.Input(ctx, a.ID, a.Text); err != nil {
					return "", err
				}
				return "sent", nil
			},
		},
		&tools.Tool{
			Name: "process_cancel", Category: "code", Risk: tools.RiskWrite,
			Description: "Stop a background process (SIGTERM, then SIGKILL if it doesn't exit within a few seconds).",
			Params:      tools.Obj("id", tools.Int("id", "process id")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ ID int64 }](raw)
				if err != nil {
					return "", err
				}
				return "stopping", pm.Cancel(ctx, a.ID)
			},
		},
	)
}

func formatProcess(p Process) string {
	code := ""
	if p.ExitCode != nil {
		code = fmt.Sprintf(" exit=%d", *p.ExitCode)
	}
	return fmt.Sprintf("#%d %s%s — %s (%s)", p.ID, p.Status, code, trim(p.Command, 80), p.Cwd)
}

// snapshot returns a process record together with the tail of its output (live buffer while running, the
// stored transcript afterwards).
func (pm *ProcessManager) snapshot(ctx context.Context, id int64) (Process, string, error) {
	p, err := pm.Get(ctx, id)
	if err != nil {
		return p, "", err
	}
	pm.mu.Lock()
	r, live := pm.live[id]
	pm.mu.Unlock()
	var out string
	if live {
		out = r.out.String()
	} else {
		_ = pm.d.DB.QueryRow(ctx, `SELECT output FROM bg_processes WHERE id=$1`, id).Scan(&out)
	}
	return p, out, nil
}

// Wait blocks until the process is no longer running or d has passed (cut short if ctx ends), and returns
// its state and output so far. This is what lets one tool call cover a quick command end to end while a long
// one still returns control after a bounded wait.
func (pm *ProcessManager) Wait(ctx context.Context, id int64, d time.Duration) (Process, string, error) {
	deadline := time.Now().Add(d)
	for {
		p, out, err := pm.snapshot(ctx, id)
		if err != nil || p.Status != "running" || !time.Now().Before(deadline) {
			return p, out, err
		}
		select {
		case <-ctx.Done():
			return p, out, nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

var (
	progPctRe  = regexp.MustCompile(`(\d+(?:\.\d+)?)%`)
	progETARe  = regexp.MustCompile(`ETA\s+([0-9:]+)`)
	progRateRe = regexp.MustCompile(`(?i)(?:at|@)\s+([\d.]+\s?[KMGT]?i?B/s)`)
)

// progressOf reads a one-line progress summary ("45.3% · 2.5MiB/s · ETA 02:10") from the end of a command's
// output — yt-dlp, aria2, rsync, curl and most build tools print something of the kind.
func progressOf(out string) string {
	tail := out
	if len(tail) > 1200 {
		tail = tail[len(tail)-1200:]
	}
	m := progPctRe.FindAllStringSubmatch(tail, -1)
	if len(m) == 0 {
		return ""
	}
	// the numbers must come from the same (last) progress line
	line := tail
	if i := strings.LastIndex(tail[:strings.LastIndex(tail, m[len(m)-1][0])+1], "\n"); i >= 0 {
		line = tail[i+1:]
	}
	parts := []string{m[len(m)-1][1] + "%"}
	if r := progRateRe.FindStringSubmatch(line); r != nil {
		parts = append(parts, r[1])
	}
	if e := progETARe.FindStringSubmatch(line); e != nil {
		parts = append(parts, "ETA "+e[1])
	}
	return strings.Join(parts, " · ")
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\r\n "), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		// progress bars redraw with \r: keep the freshest segment
		seg := strings.Split(lines[i], "\r")
		if l := strings.TrimSpace(seg[len(seg)-1]); l != "" {
			return trim(l, 200)
		}
	}
	return ""
}

// describeProcess is what an agent reads back: the status line, then either the progress and latest output
// of a running process or the (tail of the) output of a finished one.
func describeProcess(p Process, out string) string {
	head := formatProcess(p)
	if p.Status == "running" {
		var sb strings.Builder
		sb.WriteString(head)
		if pr := progressOf(out); pr != "" {
			sb.WriteString("\nprogress: " + pr)
		}
		if l := lastLine(out); l != "" {
			sb.WriteString("\nlatest output: " + l)
		}
		return sb.String()
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return head + "\n(no output)"
	}
	if r := []rune(out); len(r) > 4000 {
		out = "…" + string(r[len(r)-4000:])
	}
	return head + "\noutput:\n" + out
}
