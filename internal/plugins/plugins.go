// Package plugins lets an agent write a small Python tool at runtime. Nothing an agent writes runs until
// the user approves it: the code is shown in full, pinned by hash, and executed in a macOS sandbox with no
// network (unless the plugin declared it and the user approved that too), no writes outside its own folder
// and no reading of protected locations. Where no sandbox exists, plugins refuse to run rather than run open.
package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/tools"
)

type Plugin struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Params      json.RawMessage `json:"params"`
	Code        string          `json:"code,omitempty"`
	Hash        string          `json:"hash"`
	Status      string          `json:"status"`
	Network     bool            `json:"network"`
	TimeoutS    int             `json:"timeout_s"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	ApprovedAt  *time.Time      `json:"approved_at,omitempty"`
}

const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDisabled = "disabled"

	maxCode   = 64 << 10
	maxOutput = 20000
)

// runner is the tiny host program: it loads the plugin module, feeds it the JSON arguments from stdin and
// prints what run(args) returns.
const runner = `import sys, json, importlib.util
spec = importlib.util.spec_from_file_location("plugin", sys.argv[1])
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
args = json.load(sys.stdin)
out = mod.run(args)
sys.stdout.write(out if isinstance(out, str) else json.dumps(out, ensure_ascii=False, default=str))
`

type Manager struct {
	DB      *pgxpool.Pool
	Reg     *tools.Registry
	DataDir string
	// DenyReads lists locations a plugin may never read (the agents' protected paths).
	DenyReads func(ctx context.Context) []string
	// Emit reports plugin events (e.g. "plugin.pending") to the app; may be nil.
	Emit func(typ string, data any)
	// Python is the interpreter (default python3).
	Python string

	mu sync.Mutex
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,30}$`)

func hashOf(code string) string { h := sha256.Sum256([]byte(code)); return hex.EncodeToString(h[:]) }

func (m *Manager) py() string {
	if m.Python != "" {
		return m.Python
	}
	return "python3"
}

// Sandboxed reports whether plugins can run safely here.
func (m *Manager) Sandboxed() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

// ── storage ─────────────────────────────────────────────────────────────────

const cols = `id,name,description,params,code,hash,status,network,timeout_s,created_by,created_at,approved_at`

func scan(row interface{ Scan(...any) error }) (Plugin, error) {
	var p Plugin
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Params, &p.Code, &p.Hash, &p.Status, &p.Network, &p.TimeoutS, &p.CreatedBy, &p.CreatedAt, &p.ApprovedAt)
	return p, err
}

// List returns every plugin; withCode includes the source (the UI asks for it when showing one).
func (m *Manager) List(ctx context.Context, withCode bool) ([]Plugin, error) {
	rows, err := m.DB.Query(ctx, `SELECT `+cols+` FROM plugins ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plugin{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if !withCode {
			p.Code = ""
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (m *Manager) Get(ctx context.Context, id int64) (Plugin, error) {
	return scan(m.DB.QueryRow(ctx, `SELECT `+cols+` FROM plugins WHERE id=$1`, id))
}

// Validate checks a proposed plugin without storing it.
func Validate(p Plugin) error {
	if !nameRe.MatchString(p.Name) {
		return errors.New("the name must be lowercase letters, digits and _ (2–31 characters, starting with a letter)")
	}
	if strings.TrimSpace(p.Description) == "" {
		return errors.New("describe what the plugin does")
	}
	if len(p.Code) == 0 || len(p.Code) > maxCode {
		return fmt.Errorf("the code must be 1 byte to %d KB", maxCode>>10)
	}
	if !regexp.MustCompile(`(?m)^def run\(\s*args\s*\)\s*:`).MatchString(p.Code) {
		return errors.New("the code must define `def run(args):` — args is a dict, and the return value (a string or JSON-serialisable data) is the result")
	}
	if len(p.Params) > 0 && string(p.Params) != "null" {
		var s map[string]any
		if err := json.Unmarshal(p.Params, &s); err != nil {
			return fmt.Errorf("params must be a JSON schema object: %w", err)
		}
	}
	if p.TimeoutS < 0 || p.TimeoutS > 300 {
		return errors.New("timeout_s must be between 1 and 300")
	}
	return nil
}

// syntaxCheck compiles the code so mistakes surface at creation, not at first use.
func (m *Manager) syntaxCheck(ctx context.Context, code string) error {
	dir, err := os.MkdirTemp("", "prism-plugin-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, "p.py")
	if err := os.WriteFile(f, []byte(code), 0o600); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, m.py(), "-I", "-c", "import sys; compile(open(sys.argv[1]).read(), 'plugin.py', 'exec')", f).CombinedOutput()
	if err != nil {
		return fmt.Errorf("the code does not compile: %s", tailStr(string(out), 400))
	}
	return nil
}

func tailStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}

// Create stores a new plugin as pending. It never runs until Approve.
func (m *Manager) Create(ctx context.Context, p Plugin) (Plugin, error) {
	p.Name = strings.ToLower(strings.TrimSpace(p.Name))
	if p.TimeoutS == 0 {
		p.TimeoutS = 30
	}
	if len(p.Params) == 0 || string(p.Params) == "null" {
		p.Params = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	if err := Validate(p); err != nil {
		return p, err
	}
	if _, ok := m.Reg.Get("plugin_" + p.Name); ok {
		return p, fmt.Errorf("a plugin called %s already exists", p.Name)
	}
	if err := m.syntaxCheck(ctx, p.Code); err != nil {
		return p, err
	}
	p.Hash, p.Status = hashOf(p.Code), StatusPending
	err := m.DB.QueryRow(ctx, `INSERT INTO plugins(name,description,params,code,hash,status,network,timeout_s,created_by) VALUES($1,$2,$3,$4,$5,'pending',$6,$7,$8) RETURNING id,created_at`,
		p.Name, p.Description, p.Params, p.Code, p.Hash, p.Network, p.TimeoutS, p.CreatedBy).Scan(&p.ID, &p.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "plugins_name_key") {
			return p, fmt.Errorf("a plugin called %s already exists", p.Name)
		}
		return p, err
	}
	if m.Emit != nil {
		m.Emit("plugin.pending", map[string]any{"id": p.ID, "name": p.Name, "by": p.CreatedBy, "description": p.Description})
	}
	return p, nil
}

// SetStatus approves, disables or re-enables a plugin and re-registers the tools.
func (m *Manager) SetStatus(ctx context.Context, id int64, status string) error {
	if status != StatusApproved && status != StatusDisabled && status != StatusPending {
		return errors.New("unknown status")
	}
	var at any
	if status == StatusApproved {
		at = time.Now()
	}
	t, err := m.DB.Exec(ctx, `UPDATE plugins SET status=$2, approved_at=COALESCE($3, approved_at) WHERE id=$1`, id, status, at)
	if err != nil {
		return err
	}
	if t.RowsAffected() == 0 {
		return errors.New("no such plugin")
	}
	return m.Sync(ctx)
}

func (m *Manager) Delete(ctx context.Context, id int64) error {
	if _, err := m.DB.Exec(ctx, `DELETE FROM plugins WHERE id=$1`, id); err != nil {
		return err
	}
	return m.Sync(ctx)
}

// Sync makes the registry match the database: every approved plugin whose code still matches its pinned
// hash is a tool; everything else is not.
func (m *Manager) Sync(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.Reg.All() {
		if strings.HasPrefix(t.Source, "plugin:") {
			m.Reg.UnregisterSource(t.Source)
		}
	}
	ps, err := m.List(ctx, true)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.Status != StatusApproved || hashOf(p.Code) != p.Hash {
			continue // never approved, switched off, or the row was changed after approval
		}
		p := p
		desc := "[plugin — code approved by the user] " + p.Description
		if !p.Network {
			desc += " (runs offline in a sandbox)"
		}
		m.Reg.Register(&tools.Tool{
			Name: "plugin_" + p.Name, Category: "plugins", Risk: tools.RiskExec, Deferred: true, Untrusted: p.Network,
			Source: "plugin:" + p.Name, Description: desc, Params: p.Params,
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				return m.run(ctx, p, raw)
			},
		})
	}
	return nil
}

// ── running ─────────────────────────────────────────────────────────────────

func sbQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// profile builds the sandbox policy for one run.
func (m *Manager) profile(ctx context.Context, dir string, network bool) string {
	var sb strings.Builder
	sb.WriteString("(version 1)\n(allow default)\n")
	if !network {
		sb.WriteString("(deny network*)\n")
	}
	sb.WriteString("(deny file-write* (require-all (require-not (subpath " + sbQuote(dir) + ")) (require-not (subpath \"/private/tmp\")) (require-not (subpath \"/private/var/folders\")) (require-not (literal \"/dev/null\")) (require-not (literal \"/dev/dtracehelper\")) (require-not (subpath \"/dev/fd\"))))\n")
	if m.DenyReads != nil {
		for _, r := range m.DenyReads(ctx) {
			if c, err := filepath.EvalSymlinks(r); err == nil { // the sandbox matches real paths (/var is really /private/var)
				r = c
			}
			if r != "" && !strings.ContainsAny(r, "\"\n") {
				sb.WriteString("(deny file-read* (subpath " + sbQuote(r) + "))\n")
			}
		}
	}
	return sb.String()
}

func (m *Manager) run(ctx context.Context, p Plugin, args json.RawMessage) (string, error) {
	if !m.Sandboxed() {
		return "", errors.New("plugins run only inside the macOS sandbox, which is not available here")
	}
	if hashOf(p.Code) != p.Hash {
		return "", errors.New("the plugin's code no longer matches what was approved")
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	dir := filepath.Join(m.DataDir, "plugins", p.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dir, _ = filepath.EvalSymlinks(dir)
	// the files are rewritten from the pinned code on every run, so nothing on disk can change what runs
	mod, host := filepath.Join(dir, "plugin.py"), filepath.Join(dir, "runner.py")
	_ = os.Remove(mod)
	if err := os.WriteFile(mod, []byte(p.Code), 0o444); err != nil {
		return "", err
	}
	_ = os.Remove(host)
	if err := os.WriteFile(host, []byte(runner), 0o444); err != nil {
		return "", err
	}
	prof := filepath.Join(dir, ".sandbox.sb")
	_ = os.Remove(prof)
	if err := os.WriteFile(prof, []byte(m.profile(ctx, dir, p.Network)), 0o444); err != nil {
		return "", err
	}
	limit := time.Duration(p.TimeoutS) * time.Second
	if limit <= 0 || limit > 5*time.Minute {
		limit = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sandbox-exec", "-f", prof, m.py(), "-I", "-B", host, mod)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", "HOME=" + dir, "TMPDIR=" + dir, "PYTHONDONTWRITEBYTECODE=1", "LANG=en_US.UTF-8", "PYTHONIOENCODING=utf-8"}
	cmd.Stdin = bytes.NewReader(args)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr limited
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if cctx.Err() != nil && ctx.Err() == nil {
		return stdout.String(), fmt.Errorf("the plugin did not finish within %s", limit)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("the plugin failed: %s", tailStr(msg, 800))
	}
	out := stdout.String()
	if strings.TrimSpace(out) == "" {
		return "(the plugin returned nothing)", nil
	}
	return out, nil
}

// limited is a buffer that keeps only the first maxOutput bytes and notes the truncation.
type limited struct {
	b     bytes.Buffer
	extra int
}

func (l *limited) Write(p []byte) (int, error) {
	room := maxOutput - l.b.Len()
	if room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
			l.extra += len(p) - room
		} else {
			l.b.Write(p)
		}
	} else {
		l.extra += len(p)
	}
	return len(p), nil
}

func (l *limited) String() string {
	if l.extra > 0 {
		return l.b.String() + fmt.Sprintf("\n…[output truncated, %d more bytes]", l.extra)
	}
	return l.b.String()
}
