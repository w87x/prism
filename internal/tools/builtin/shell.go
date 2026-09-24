package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"prism/internal/tools"
)

const maxShellOut = 20000

type capped struct {
	buf       bytes.Buffer
	truncated int
}

func (c *capped) Write(p []byte) (int, error) {
	room := maxShellOut*2 - c.buf.Len()
	if room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated += len(p) - room
		} else {
			c.buf.Write(p)
		}
	} else {
		c.truncated += len(p)
	}
	return len(p), nil
}

func (c *capped) String() string {
	s := c.buf.String()
	if len(s) > maxShellOut { // keep head and tail
		s = s[:maxShellOut/2] + "\n…[output truncated]…\n" + s[len(s)-maxShellOut/2:]
	} else if c.truncated > 0 {
		s += "\n…[output truncated]"
	}
	return s
}

func runCmd(ctx context.Context, timeout time.Duration, dir string, name string, args ...string) (string, error) {
	if timeout <= 0 || timeout > 30*time.Minute {
		timeout = 2 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	out := &capped{}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	res := out.String()
	switch {
	case ctx.Err() != nil: // the caller's own deadline or cancellation, not this command's timeout
		return res + "\n[stopped: the tool call was cancelled or hit its overall time limit]", nil
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		return res + fmt.Sprintf("\n[timed out after %s]", timeout), nil
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return res + fmt.Sprintf("\n[exit status %d]", ee.ExitCode()), nil
		}
		return res, err
	}
	if strings.TrimSpace(res) == "" {
		return "(no output, exit 0)", nil
	}
	return res, nil
}

func registerShell(reg *tools.Registry, d Deps) {
	work := filepath.Join(d.DataDir, "work")
	_ = os.MkdirAll(work, 0o755)
	reg.Register(
		&tools.Tool{
			Name: "shell", Category: "code", Risk: tools.RiskExec, Timeout: 31 * time.Minute,
			Description: "Run a quick shell command (zsh) and get its output — the right tool for ls, cat, head, grep, wc, small scripts. Default working dir is the PRISM workspace. Output is truncated to ~20 KB; commands time out (default 2 min) and block your turn while they run, so for anything long (downloads, builds, transfers) use process_start (or download_start for files and videos) instead of raising the timeout.",
			Params: tools.Obj("command", tools.Str("command", "the command line"), tools.Str("cwd", "working directory (default workspace)"),
				tools.Int("timeout_s", "timeout in seconds (max 1800)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Command  string `json:"command"`
					Cwd      string `json:"cwd"`
					TimeoutS int    `json:"timeout_s"`
				}](raw)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(a.Command) == "" {
					return "", errors.New("empty command")
				}
				dir := work
				if a.Cwd != "" {
					dir = expandHome(a.Cwd)
				}
				return runCmd(ctx, time.Duration(a.TimeoutS)*time.Second, dir, "/bin/zsh", "-c", a.Command)
			},
		},
		&tools.Tool{
			Name: "python", Category: "code", Risk: tools.RiskExec, Timeout: 31 * time.Minute,
			Description: "Run a Python 3 script (stdlib and whatever is installed). Use print() for output. Good for data crunching and quick calculations.",
			Params:      tools.Obj("code", tools.Str("code", "python source"), tools.Int("timeout_s", "timeout in seconds")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Code     string `json:"code"`
					TimeoutS int    `json:"timeout_s"`
				}](raw)
				if err != nil {
					return "", err
				}
				f, err := os.CreateTemp(work, "snippet-*.py")
				if err != nil {
					return "", err
				}
				defer os.Remove(f.Name())
				if _, err := f.WriteString(a.Code); err != nil {
					return "", err
				}
				f.Close()
				return runCmd(ctx, time.Duration(a.TimeoutS)*time.Second, work, "python3", f.Name())
			},
		},
	)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
