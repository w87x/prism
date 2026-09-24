package obsidian

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"prism/internal/settings"
	"prism/internal/testutil"
	"prism/internal/tools"
)

func TestVaultToolsAndConfinement(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	vault := t.TempDir()
	_ = os.MkdirAll(filepath.Join(vault, ".obsidian"), 0o755)
	_ = os.WriteFile(filepath.Join(vault, ".obsidian", "daily-notes.json"), []byte(`{"folder":"Daily","format":"YYYY-MM-DD"}`), 0o644)
	_ = os.WriteFile(filepath.Join(vault, "Trip plan.md"), []byte("# Trip\nBook flights to Lisbon in October. Hotel near Alfama."), 0o644)
	_ = os.WriteFile(filepath.Join(vault, "Recipes.md"), []byte("Pasta with tomatoes and basil."), 0o644)
	secret := filepath.Join(t.TempDir(), "secret.md")
	_ = os.WriteFile(secret, []byte("top secret"), 0o644)
	_ = os.Symlink(secret, filepath.Join(vault, "link.md"))
	_ = st.Set(context.Background(), settings.KeyObsidian, settings.Obsidian{VaultPath: vault})

	v := &Vault{Settings: st}
	reg := tools.NewRegistry(d.Pool)
	RegisterTools(reg, v)
	ctx := context.Background()
	call := func(name string, args any) (string, error) {
		tool, _ := reg.Get(name)
		b, _ := json.Marshal(args)
		return tool.Run(ctx, &tools.Env{Agent: "t"}, b)
	}
	out, err := call("obsidian_search", map[string]any{"query": "lisbon hotel"})
	if err != nil || !strings.HasPrefix(out, "Trip plan") {
		t.Fatalf("search: %q %v", out, err)
	}
	if out, err = call("obsidian_read", map[string]any{"path": "Trip plan"}); err != nil || !strings.Contains(out, "Alfama") {
		t.Fatalf("read: %q %v", out, err)
	}
	// write refuses overwrite, append works, daily note lands in the configured folder
	if _, err = call("obsidian_write", map[string]any{"path": "Trip plan", "content": "x"}); err == nil {
		t.Fatal("overwrite should be refused")
	}
	if _, err = call("obsidian_append", map[string]any{"path": "Trip plan", "text": "- pack passport"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(vault, "Trip plan.md"))
	if !strings.Contains(string(b), "Alfama") || !strings.Contains(string(b), "- pack passport") {
		t.Fatalf("append lost data: %s", b)
	}
	if _, err = call("obsidian_daily", map[string]any{"text": "- called mom"}); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(vault, "Daily", "*.md"))
	if len(matches) != 1 {
		t.Fatalf("daily note not created in Daily/: %v", matches)
	}
	// confinement
	for _, bad := range []string{"../outside", "../../etc/passwd", ".obsidian/app", "link"} {
		if out, err := call("obsidian_read", map[string]any{"path": bad}); err == nil {
			t.Fatalf("path %q must be rejected, got %q", bad, out)
		}
	}
	if _, err = call("obsidian_write", map[string]any{"path": "../evil", "content": "x"}); err == nil {
		if _, serr := os.Stat(filepath.Join(filepath.Dir(vault), "evil.md")); serr == nil {
			t.Fatal("wrote outside the vault")
		}
	}
}

// WriteNote is the exported path the UI uses directly (e.g. "save briefing to Obsidian"), without going
// through the agent tool registry.
func TestWriteNoteExportedForDirectUse(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	vault := t.TempDir()
	ctx := context.Background()
	_ = st.Set(ctx, settings.KeyObsidian, settings.Obsidian{VaultPath: vault})
	v := &Vault{Settings: st}

	rel, err := v.WriteNote(ctx, "Briefings/2026-09-22 Weekly digest", "# Weekly digest\nhello", false)
	if err != nil || rel != "Briefings/2026-09-22 Weekly digest.md" {
		t.Fatalf("rel=%q err=%v", rel, err)
	}
	b, err := os.ReadFile(filepath.Join(vault, "Briefings", "2026-09-22 Weekly digest.md"))
	if err != nil || !strings.Contains(string(b), "hello") {
		t.Fatalf("content: %q err=%v", b, err)
	}
	if _, err := v.WriteNote(ctx, "Briefings/2026-09-22 Weekly digest", "again", false); err == nil {
		t.Fatal("must refuse to overwrite without the flag")
	}
	if _, err := v.WriteNote(ctx, "Briefings/2026-09-22 Weekly digest", "again", true); err != nil {
		t.Fatalf("overwrite=true should succeed: %v", err)
	}
	// "../escape" is confined (Clean collapses it against the vault root), not rejected — verify it lands inside
	if rel, err := v.WriteNote(ctx, "../escape", "x", false); err != nil || rel != "escape.md" {
		t.Fatalf("rel=%q err=%v", rel, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(vault), "escape.md")); err == nil {
		t.Fatal("wrote outside the vault")
	}
	if _, err := os.Stat(filepath.Join(vault, "escape.md")); err != nil {
		t.Fatal("should have been confined inside the vault")
	}
}
