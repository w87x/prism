// Package codexapp drives the Codex CLI's app-server, the interface Codex itself uses to power rich clients (its IDE extensions and
// desktop app). PRISM uses it to run models on the user's ChatGPT subscription: Codex keeps the login and refreshes the token,
// PRISM never sees or stores a credential. The protocol is JSON-RPC over the child's stdin/stdout, one JSON message per line.
//
// One long-lived `codex app-server` process serves every request; each request is its own short, ephemeral thread with a single turn.
package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// FindBinary locates the Codex CLI: an explicit path or name, else PRISM_CODEX_BIN, else the PATH and the usual install folders.
func FindBinary(custom string) (string, error) {
	bin := custom
	if bin == "" {
		bin = os.Getenv("PRISM_CODEX_BIN")
	}
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

// Error is a failure reported by Codex, classified so callers can treat a used-up plan differently from a bad request.
type Error struct {
	Message string
	Limit   bool // the plan's usage or rate limit
	Auth    bool // not signed in, or the sign-in expired
}

func (e *Error) Error() string { return "codex: " + e.Message }

func classify(msg string) *Error {
	l := strings.ToLower(msg)
	e := &Error{Message: msg}
	switch {
	case strings.Contains(l, "usage limit"), strings.Contains(l, "rate limit"), strings.Contains(l, "too many requests"), strings.Contains(l, "quota"):
		e.Limit = true
	case strings.Contains(l, "log in"), strings.Contains(l, "login"), strings.Contains(l, "sign in"), strings.Contains(l, "unauthorized"), strings.Contains(l, "401"), strings.Contains(l, "not authenticated"):
		e.Auth = true
	}
	return e
}

type rpcMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	wmu    sync.Mutex
	nextID atomic.Int64
	mu     sync.Mutex
	wait   map[int64]chan rpcMsg
	subs   map[string]chan rpcMsg // thread id → its notifications
	dead   chan struct{}
	tail   *tailBuf
	rate   json.RawMessage // the last rate-limit snapshot Codex reported
}

type tailBuf struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 4000 {
		t.b = t.b[len(t.b)-4000:]
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}

// Manager owns the app-server process: started on first use, restarted if it dies.
type Manager struct {
	Bin     string // optional explicit path
	Version string // reported to Codex as the client version

	mu  sync.Mutex
	p   *proc
	cwd string
}

// Default is the process-wide manager.
var Default = &Manager{}

func (m *Manager) scratch() (string, error) {
	if m.cwd != "" {
		return m.cwd, nil
	}
	d, err := os.MkdirTemp("", "prism-codex-*")
	if err != nil {
		return "", err
	}
	m.cwd = d
	return d, nil
}

func (m *Manager) ensure(ctx context.Context) (*proc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.p != nil {
		select {
		case <-m.p.dead:
			m.p = nil
		default:
			return m.p, nil
		}
	}
	bin, err := FindBinary(m.Bin)
	if err != nil {
		return nil, err
	}
	dir, err := m.scratch()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "app-server")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &proc{cmd: cmd, stdin: in, wait: map[int64]chan rpcMsg{}, subs: map[string]chan rpcMsg{}, dead: make(chan struct{}), tail: &tailBuf{}}
	cmd.Stderr = p.tail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting codex app-server: %w", err)
	}
	go p.read(out)
	go func() { _ = cmd.Wait(); p.fail() }()
	ver := m.Version
	if ver == "" {
		ver = "0"
	}
	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := p.call(ictx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "prism", "title": "PRISM", "version": ver}}); err != nil {
		p.kill()
		return nil, fmt.Errorf("codex app-server did not start: %w", err)
	}
	p.notify("initialized", nil)
	m.p = p
	return p, nil
}

// Close stops the app-server (it also exits by itself when PRISM does, because its stdin closes).
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.p != nil {
		m.p.kill()
		m.p = nil
	}
	if m.cwd != "" {
		_ = os.RemoveAll(m.cwd)
		m.cwd = ""
	}
}

func (p *proc) kill() {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// fail ends every waiting call and subscriber: the process is gone.
func (p *proc) fail() {
	select {
	case <-p.dead:
	default:
		close(p.dead)
	}
}

func (p *proc) read(r io.Reader) {
	defer p.fail()
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var m rpcMsg
			if json.Unmarshal(line, &m) == nil {
				p.dispatch(m)
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *proc) dispatch(m rpcMsg) {
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method == "" && hasID: // the answer to one of our requests
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			return
		}
		p.mu.Lock()
		ch := p.wait[id]
		delete(p.wait, id)
		p.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	case m.Method != "" && hasID: // Codex asking us something (an approval, a tool): there is nobody to ask, so it is declined
		p.write(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "not supported by this client"}})
	case m.Method != "":
		if m.Method == "account/rateLimits/updated" {
			p.mu.Lock()
			p.rate = m.Params
			p.mu.Unlock()
		}
		var th struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(m.Params, &th)
		if th.ThreadID == "" {
			return
		}
		p.mu.Lock()
		ch := p.subs[th.ThreadID]
		p.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			case <-p.dead:
			}
		}
	}
}

func (p *proc) write(v any) {
	b, _ := json.Marshal(v)
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, _ = p.stdin.Write(append(b, '\n'))
}

func (p *proc) notify(method string, params any) {
	m := map[string]any{"method": method}
	if params != nil {
		m["params"] = params
	}
	p.write(m)
}

func (p *proc) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := p.nextID.Add(1)
	ch := make(chan rpcMsg, 1)
	p.mu.Lock()
	p.wait[id] = ch
	p.mu.Unlock()
	if params == nil {
		params = map[string]any{}
	}
	p.write(map[string]any{"id": id, "method": method, "params": params})
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, classify(m.Error.Message)
		}
		return m.Result, nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.wait, id)
		p.mu.Unlock()
		return nil, ctx.Err()
	case <-p.dead:
		return nil, p.exited()
	}
}

func (p *proc) exited() error {
	msg := "the Codex app-server exited"
	if t := p.tail.String(); t != "" {
		msg += ": " + t
	}
	return classify(msg)
}

func (p *proc) subscribe(thread string) chan rpcMsg {
	ch := make(chan rpcMsg, 512)
	p.mu.Lock()
	p.subs[thread] = ch
	p.mu.Unlock()
	return ch
}

func (p *proc) unsubscribe(thread string) {
	p.mu.Lock()
	delete(p.subs, thread)
	p.mu.Unlock()
}

// ── what callers see ──

// Model is one model the signed-in plan can use.
type Model struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Images  bool     `json:"images"`
	Efforts []string `json:"efforts"`
	Default bool     `json:"default"`
}

// Status says whether Codex is installed and signed in, and with which plan. It carries no credential.
type Status struct {
	Installed bool            `json:"installed"`
	LoggedIn  bool            `json:"logged_in"`
	Kind      string          `json:"kind,omitempty"` // chatgpt | apikey
	Plan      string          `json:"plan,omitempty"`
	Error     string          `json:"error,omitempty"`
	Limits    json.RawMessage `json:"limits,omitempty"` // the plan's usage windows, as Codex reports them
}

func (m *Manager) Status(ctx context.Context) Status {
	if _, err := FindBinary(m.Bin); err != nil {
		return Status{Error: err.Error()}
	}
	st := Status{Installed: true}
	p, err := m.ensure(ctx)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	res, err := p.call(ctx, "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		st.Error = err.Error()
		return st
	}
	var a struct {
		Account *struct {
			Type     string `json:"type"`
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	_ = json.Unmarshal(res, &a)
	if a.Account != nil {
		st.LoggedIn, st.Kind, st.Plan = true, a.Account.Type, a.Account.PlanType
	} else {
		st.Error = "Codex is installed but not signed in — run `codex login` and choose Sign in with ChatGPT"
	}
	p.mu.Lock()
	st.Limits = p.rate
	p.mu.Unlock()
	return st
}

// Models lists what the plan offers.
func (m *Manager) Models(ctx context.Context) ([]Model, error) {
	p, err := m.ensure(ctx)
	if err != nil {
		return nil, err
	}
	var out []Model
	cursor := ""
	for i := 0; i < 5; i++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := p.call(ctx, "model/list", params)
		if err != nil {
			return nil, err
		}
		var r struct {
			Data []struct {
				ID                        string   `json:"id"`
				Model                     string   `json:"model"`
				DisplayName               string   `json:"displayName"`
				Hidden                    bool     `json:"hidden"`
				IsDefault                 bool     `json:"isDefault"`
				InputModalities           []string `json:"inputModalities"`
				SupportedReasoningEfforts []struct {
					ReasoningEffort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &r); err != nil {
			return nil, err
		}
		for _, d := range r.Data {
			if d.Hidden {
				continue
			}
			md := Model{ID: firstNonEmpty(d.Model, d.ID), Name: firstNonEmpty(d.DisplayName, d.Model, d.ID), Default: d.IsDefault}
			for _, im := range d.InputModalities {
				md.Images = md.Images || im == "image"
			}
			for _, e := range d.SupportedReasoningEfforts {
				md.Efforts = append(md.Efforts, e.ReasoningEffort)
			}
			out = append(out, md)
		}
		if r.NextCursor == nil || *r.NextCursor == "" {
			break
		}
		cursor = *r.NextCursor
	}
	return out, nil
}

func firstNonEmpty(a ...string) string {
	for _, x := range a {
		if x != "" {
			return x
		}
	}
	return ""
}

// slimConfig switches off everything of Codex's own that PRISM does not use. PRISM wants the model, not the agent around it: with
// Codex's defaults a one-word question carried ~12.4k input tokens (its skills list, plugin and app catalogues, multi-agent and browser
// tools, shell, web search, patch and plan tools); with these overrides ~4.2k (measured on the real service). They are ordinary config
// keys, the same ones `codex -c key=value` takes.
var slimConfig = map[string]any{
	"skills.include_instructions": false, "skills.bundled.enabled": false,
	"features.plugins": false, "features.apps": false, "features.multi_agent": false, "features.multi_agent_v2": false,
	"features.goals": false, "features.browser_use": false, "features.computer_use": false, "features.in_app_browser": false,
	"features.image_generation": false, "features.hooks": false, "features.skill_search": false, "features.sleep_tool": false,
	"features.tool_suggest": false, "features.shell_tool": false, "features.unified_exec": false,
	"web_search": "disabled", "include_apply_patch_tool": false, "include_plan_tool": false, "tools.view_image": false,
}

// Request is one model call.
type Request struct {
	Model        string
	Instructions string          // the system prompt: it replaces Codex's own
	Input        string          // the conversation, as text
	Images       []string        // picture files to show the model
	Schema       json.RawMessage // optional JSON schema the answer must follow
	Effort       string          // reasoning effort, if the model has it
	OnDelta      func(string)    // streamed text, when there is no schema
}

// Result is the model's answer and what it cost.
type Result struct {
	Text    string
	In, Out int
	Window  int
}

// Complete runs one turn on a fresh ephemeral thread.
func (m *Manager) Complete(ctx context.Context, rq Request) (*Result, error) {
	p, err := m.ensure(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	cwd, _ := m.scratch()
	m.mu.Unlock()
	ts := map[string]any{"ephemeral": true, "baseInstructions": rq.Instructions, "sandbox": "read-only", "approvalPolicy": "never", "cwd": cwd}
	if rq.Model != "" {
		ts["model"] = rq.Model
	}
	ts["config"] = slimConfig
	res, err := p.call(ctx, "thread/start", ts)
	var ce *Error
	if err != nil && errors.As(err, &ce) && !ce.Limit && !ce.Auth {
		delete(ts, "config") // a Codex version that does not take these keys: work with its defaults rather than not at all
		res, err = p.call(ctx, "thread/start", ts)
	}
	if err != nil {
		return nil, err
	}
	var th struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(res, &th); err != nil || th.Thread.ID == "" {
		return nil, errors.New("codex: no thread was started")
	}
	tid := th.Thread.ID
	sub := p.subscribe(tid)
	defer p.unsubscribe(tid)

	input := []map[string]any{{"type": "text", "text": rq.Input}}
	for _, f := range rq.Images {
		input = append(input, map[string]any{"type": "localImage", "path": f})
	}
	ps := map[string]any{"threadId": tid, "input": input}
	if len(rq.Schema) > 0 {
		ps["outputSchema"] = json.RawMessage(rq.Schema)
	}
	if rq.Effort != "" {
		ps["effort"] = rq.Effort
	}
	var turnID string
	if tr, err := p.call(ctx, "turn/start", ps); err != nil {
		return nil, err
	} else {
		var t struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(tr, &t)
		turnID = t.Turn.ID
	}
	out := &Result{}
	var deltas strings.Builder
	var lastErr string
	for {
		select {
		case <-ctx.Done():
			ic, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = p.call(ic, "turn/interrupt", map[string]any{"threadId": tid, "turnId": turnID})
			cancel()
			return nil, ctx.Err()
		case <-p.dead:
			return nil, p.exited()
		case n := <-sub:
			switch n.Method {
			case "item/agentMessage/delta":
				var d struct {
					Delta string `json:"delta"`
				}
				_ = json.Unmarshal(n.Params, &d)
				deltas.WriteString(d.Delta)
				if rq.OnDelta != nil && len(rq.Schema) == 0 {
					rq.OnDelta(d.Delta)
				}
			case "thread/tokenUsage/updated":
				var u struct {
					TokenUsage struct {
						Last struct {
							In  int `json:"inputTokens"`
							Out int `json:"outputTokens"`
						} `json:"last"`
						Window int `json:"modelContextWindow"`
					} `json:"tokenUsage"`
				}
				if json.Unmarshal(n.Params, &u) == nil {
					out.In, out.Out, out.Window = u.TokenUsage.Last.In, u.TokenUsage.Last.Out, u.TokenUsage.Window
				}
			case "error":
				var e struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
					WillRetry bool `json:"willRetry"`
				}
				if json.Unmarshal(n.Params, &e) == nil && !e.WillRetry && e.Error.Message != "" {
					lastErr = e.Error.Message
				}
			case "turn/completed":
				var c struct {
					Turn struct {
						Status string `json:"status"`
						Error  *struct {
							Message string `json:"message"`
						} `json:"error"`
						Items []struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"items"`
					} `json:"turn"`
				}
				_ = json.Unmarshal(n.Params, &c)
				if c.Turn.Status == "failed" || c.Turn.Error != nil {
					msg := lastErr
					if c.Turn.Error != nil && c.Turn.Error.Message != "" {
						msg = c.Turn.Error.Message
					}
					return nil, classify(firstNonEmpty(msg, "the turn failed"))
				}
				if c.Turn.Status == "interrupted" {
					return nil, errors.New("codex: the turn was interrupted")
				}
				for _, it := range c.Turn.Items {
					if it.Type == "agentMessage" && it.Text != "" {
						out.Text = it.Text
					}
				}
				if out.Text == "" {
					out.Text = deltas.String()
				}
				return out, nil
			}
		}
	}
}
