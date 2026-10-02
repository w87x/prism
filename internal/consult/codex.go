package consult

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"prism/internal/settings"
)

// codexProvider shells out to `codex exec`, which runs one non-interactive turn on the user's ChatGPT login.
// It runs in an empty scratch directory with a read-only sandbox: the consultant sees only the prompt, never
// the user's files.
type codexProvider struct{}

func (codexProvider) ask(ctx context.Context, s *Service, cfg settings.Consult, prompt string) (string, error) {
	bin, err := findCodex(cfg)
	if err != nil {
		return "", err
	}
	root := filepath.Join(s.DataDir, "work", "consult")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, "codex-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	answer := filepath.Join(dir, "answer.md")

	args := []string{"exec", "--skip-git-repo-check", "--sandbox", "read-only", "--color", "never", "--cd", dir, "-o", answer}
	if cfg.CodexModel != "" {
		args = append(args, "-m", cfg.CodexModel)
	}
	if e := strings.ToLower(cfg.CodexEffort); efforts[e] {
		args = append(args, "-c", "model_reasoning_effort="+e)
	}
	args = append(args, "-") // prompt on stdin: no argv length limit, nothing in `ps`

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) } // node wrappers spawn children
	cmd.WaitDelay = 5 * time.Second
	var out tail
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("codex exec failed: %v\n%s", err, out.String())
	}
	b, _ := os.ReadFile(answer)
	if a := strings.TrimSpace(string(b)); a != "" {
		return a, nil
	}
	if a := strings.TrimSpace(out.String()); a != "" { // older CLIs without -o print the answer
		return a, nil
	}
	return "", fmt.Errorf("codex returned no answer")
}

// tail keeps the last ~8KB written to it.
type tail struct{ b bytes.Buffer }

func (t *tail) Write(p []byte) (int, error) {
	t.b.Write(p)
	if t.b.Len() > 16384 {
		rest := append([]byte(nil), t.b.Bytes()[t.b.Len()-8192:]...)
		t.b.Reset()
		t.b.Write(rest)
	}
	return len(p), nil
}

func (t *tail) String() string { return t.b.String() }
