package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

// Bug: canRead allowed os.TempDir() (the Go runtime's own per-process scratch dir, /var/folders/…/T on
// macOS) but not plain /tmp itself — the single most common place a user drops a file and tells an agent
// "it's in /tmp". A task that said exactly that got rejected ("reading outside your home directory is not
// allowed: /private/tmp/…"), and the agent spiralled through increasingly convoluted workarounds (shell
// cat, re-reading the same file repeatedly) trying to get at it, burning well over 100k tokens of context
// in the process. Caught from a real task transcript and its matching llm_calls token counts.
func TestCanReadAllowsPlainTmp(t *testing.T) {
	_, deps, _, _ := setup(t)
	ctx := t.Context()

	f, err := os.CreateTemp("/tmp", "prism-canread-test-*")
	if err != nil {
		t.Skipf("can't create a file in /tmp on this machine: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	if err := deps.canRead(ctx, f.Name()); err != nil {
		t.Fatalf("canRead(%q) = %v, want nil — /tmp must be readable", f.Name(), err)
	}
	// the macOS symlink form must resolve to the same allowed root
	if real, everr := filepath.EvalSymlinks(f.Name()); everr == nil && real != f.Name() {
		if err := deps.canRead(ctx, real); err != nil {
			t.Fatalf("canRead(%q) = %v, want nil — the canonical form of an allowed /tmp path must also be allowed", real, err)
		}
	}
}
