// Package swiftbin builds and runs small Swift helper programs that PRISM embeds as source: they reach macOS
// frameworks Go cannot (EventKit, PDFKit, Vision). The source is compiled on first use into the data
// directory (a few seconds, once per source version) and talked to with one JSON argument in, JSON out.
package swiftbin

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
	"runtime"
	"strings"
	"sync"
	"time"
)

// Tool is one helper program.
type Tool struct {
	Name   string // binary name prefix, e.g. "prism-eventkit"
	Source []byte // Swift source
	Plist  []byte // optional Info.plist embedded in the binary (usage descriptions for privacy prompts)
	Dir    string // where the compiled binary lives
	// Bin overrides the binary entirely (tests).
	Bin string
	// Timeout bounds one call (default 100 s: long enough to answer a macOS permission prompt).
	Timeout time.Duration

	mu   sync.Mutex
	path string
}

func (t *Tool) binaryName() string {
	sum := sha256.Sum256(append(append([]byte{}, t.Source...), t.Plist...))
	return t.Name + "-" + hex.EncodeToString(sum[:4])
}

// Cached returns the compiled binary if it exists.
func (t *Tool) Cached() (string, bool) {
	p := filepath.Join(t.Dir, t.binaryName())
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, true
	}
	return "", false
}

// CanBuild reports whether the helper can run here: macOS with a Swift compiler, or an already built binary.
func (t *Tool) CanBuild() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if t.Bin != "" {
		return true
	}
	if _, ok := t.Cached(); ok {
		return true
	}
	_, err := exec.LookPath("swiftc")
	return err == nil
}

// Ensure returns the path of an up-to-date binary, compiling it when needed.
func (t *Tool) Ensure(ctx context.Context) (string, error) {
	if t.Bin != "" {
		return t.Bin, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.path != "" {
		if _, err := os.Stat(t.path); err == nil {
			return t.path, nil
		}
	}
	if p, ok := t.Cached(); ok {
		t.path = p
		return p, nil
	}
	if runtime.GOOS != "darwin" {
		return "", errors.New("this feature is only available on macOS")
	}
	swiftc, err := exec.LookPath("swiftc")
	if err != nil {
		return "", errors.New("this needs the Swift compiler: run `xcode-select --install`, then try again")
	}
	if err := os.MkdirAll(t.Dir, 0o755); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp("", t.Name+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	src, plist, tmp := filepath.Join(work, "main.swift"), filepath.Join(work, "Info.plist"), filepath.Join(work, "out")
	if err := os.WriteFile(src, t.Source, 0o644); err != nil {
		return "", err
	}
	args := []string{"-O", "-swift-version", "5", "-o", tmp, src}
	if len(t.Plist) > 0 {
		if err := os.WriteFile(plist, t.Plist, 0o644); err != nil {
			return "", err
		}
		args = append(args, "-Xlinker", "-sectcreate", "-Xlinker", "__TEXT", "-Xlinker", "__info_plist", "-Xlinker", plist)
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(cctx, swiftc, args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("compiling %s failed: %v\n%s", t.Name, err, tail(string(out), 600))
	}
	final := filepath.Join(t.Dir, t.binaryName())
	if err := os.Rename(tmp, final); err != nil { // other filesystem: copy
		b, rerr := os.ReadFile(tmp)
		if rerr != nil {
			return "", err
		}
		if err := os.WriteFile(final, b, 0o755); err != nil {
			return "", err
		}
	}
	_ = os.Chmod(final, 0o755)
	if old, _ := filepath.Glob(filepath.Join(t.Dir, t.Name+"-*")); len(old) > 1 { // drop builds of earlier versions
		for _, o := range old {
			if o != final {
				_ = os.Remove(o)
			}
		}
	}
	t.path = final
	return final, nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}

// Run executes one command with a JSON argument and decodes the JSON answer into out. The argument is a single
// argv element: nothing is ever interpolated into a script. A {"error": "..."} answer becomes a Go error.
func (t *Tool) Run(ctx context.Context, command string, args any, out any) error {
	bin, err := t.Ensure(ctx)
	if err != nil {
		return err
	}
	a := []byte("{}")
	if args != nil {
		if a, err = json.Marshal(args); err != nil {
			return err
		}
	}
	limit := t.Timeout
	if limit <= 0 {
		limit = 100 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, command, string(a))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var reply struct {
		Error string `json:"error"`
	}
	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) > 0 && json.Unmarshal(raw, &reply) == nil && reply.Error != "" {
		return errors.New(reply.Error)
	}
	if runErr != nil {
		if cctx.Err() != nil {
			return fmt.Errorf("%s timed out after %s (is a macOS permission prompt waiting for an answer?)", t.Name, limit)
		}
		return fmt.Errorf("%s: %v %s", t.Name, runErr, tail(stderr.String(), 300))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("unexpected answer from %s: %w", t.Name, err)
		}
	}
	return nil
}
