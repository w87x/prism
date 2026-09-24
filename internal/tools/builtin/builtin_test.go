package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/settings"
	"prism/internal/testutil"
	"prism/internal/tools"
)

func setup(t *testing.T) (*tools.Registry, Deps, *Downloader, *ProcessManager) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	deps := Deps{DB: d.Pool, Settings: st, DataDir: t.TempDir()}
	reg := tools.NewRegistry(d.Pool)
	dl, pm := Register(reg, deps)
	return reg, deps, dl, pm
}

func run(t *testing.T, reg *tools.Registry, name string, args any) (string, error) {
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	b, _ := json.Marshal(args)
	return tool.Run(context.Background(), &tools.Env{Agent: "t"}, b)
}

func TestFilePolicyShellAndBookmarks(t *testing.T) {
	reg, deps, _, _ := setup(t)
	if out, err := run(t, reg, "file_write", map[string]any{"path": "notes/a.txt", "content": "hello"}); err != nil || !strings.Contains(out, "notes/a.txt") {
		t.Fatalf("write in workspace: %q %v", out, err)
	}
	if out, err := run(t, reg, "file_read", map[string]any{"path": "notes/a.txt"}); err != nil || !strings.Contains(out, "1\thello") {
		t.Fatalf("read: %q %v", out, err)
	}
	home, _ := os.UserHomeDir()
	for _, bad := range []string{filepath.Join(home, ".ssh", "id_rsa"), "/etc/passwd"} {
		if _, err := run(t, reg, "file_read", map[string]any{"path": bad}); err == nil {
			t.Fatalf("reading %s must be refused", bad)
		}
	}
	if _, err := run(t, reg, "file_write", map[string]any{"path": filepath.Join(home, "Desktop", "prism-should-not-exist.txt"), "content": "x"}); err == nil {
		t.Fatal("writing outside the data dir/roots must be refused")
	}
	// shell: output capture, exit status, timeout kills the process group
	if out, _ := run(t, reg, "shell", map[string]any{"command": "echo hi; exit 3"}); !strings.Contains(out, "hi") || !strings.Contains(out, "exit status 3") {
		t.Fatalf("shell: %q", out)
	}
	start := time.Now()
	if out, _ := run(t, reg, "shell", map[string]any{"command": "sleep 30", "timeout_s": 1}); !strings.Contains(out, "timed out") || time.Since(start) > 8*time.Second {
		t.Fatalf("timeout: %q after %s", out, time.Since(start))
	}
	// bookmarks: ranked lookup
	_, _ = run(t, reg, "bookmark_add", map[string]any{"url": "https://pcpartpicker.com", "title": "PCPartPicker", "description": "compare PC hardware prices", "keywords": []string{"pc", "prices"}})
	_, _ = run(t, reg, "bookmark_add", map[string]any{"url": "https://recipes.example", "title": "Recipes", "description": "cooking"})
	if out, _ := run(t, reg, "bookmark_find", map[string]any{"query": "hardware prices"}); !strings.HasPrefix(out, "#1 PCPartPicker") {
		t.Fatalf("bookmark_find: %q", out)
	}
	_ = deps
}

func TestDownloadWithProgressAndResume(t *testing.T) {
	reg, deps, dl, _ := setup(t)
	_ = deps.Settings.Set(context.Background(), settings.KeyWeb, settings.Web{AllowPrivate: true}) // test server is on loopback
	payload := strings.Repeat("0123456789", 30000)                                                 // 300 KB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rg := r.Header.Get("Range"); strings.HasPrefix(rg, "bytes=") {
			var from int
			fmt.Sscanf(rg, "bytes=%d-", &from)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			fmt.Fprint(w, payload[from:])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		fmt.Fprint(w, payload)
	}))
	defer srv.Close()
	ctx := context.Background()
	// simulate an interrupted earlier attempt: a .part file with the first 100 bytes exists
	dest := filepath.Join(dl.d.DataDir, "downloads", "data.bin")
	_ = os.MkdirAll(filepath.Dir(dest), 0o755)
	_ = os.WriteFile(dest+".part", []byte(payload[:100]), 0o644)
	x, err := dl.Start(ctx, srv.URL+"/data.bin", "data.bin", "Scout")
	if err != nil {
		t.Fatal(err)
	}
	var fin Download
	for i := 0; i < 100; i++ {
		fin, _ = dl.Get(ctx, x.ID)
		if fin.Status == "done" || fin.Status == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, _ := os.ReadFile(dest)
	if fin.Status != "done" || string(b) != payload || fin.Bytes != int64(len(payload)) {
		t.Fatalf("download: %+v len=%d", fin, len(b))
	}
	out, _ := run(t, reg, "download_status", map[string]any{"id": x.ID})
	if !strings.Contains(out, "done") {
		t.Fatalf("status tool: %q", out)
	}
}

func TestRSSParsesRSSAndAtom(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/atom" {
			fmt.Fprint(w, `<feed xmlns="http://www.w3.org/2005/Atom"><title>A</title><entry><title>One</title><link href="https://x/1"/><id>1</id><updated>2026-01-01</updated><summary>s &amp; t</summary></entry></feed>`)
			return
		}
		fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>R</title><item><title>Hello</title><link>https://y/1</link><pubDate>Mon</pubDate><description>&lt;p&gt;Body&lt;/p&gt;</description><guid>g1</guid></item></channel></rss>`)
	}))
	defer site.Close()
	title, items, err := ReadFeedAllow(context.Background(), site.URL+"/rss", 5, true)
	if err != nil || title != "R" || len(items) != 1 || items[0].Summary != "Body" || items[0].ID != "g1" {
		t.Fatalf("rss: %q %+v %v", title, items, err)
	}
	title, items, err = ReadFeedAllow(context.Background(), site.URL+"/atom", 5, true)
	if err != nil || title != "A" || items[0].Link != "https://x/1" {
		t.Fatalf("atom: %q %+v %v", title, items, err)
	}
}

// Secrets stay unreadable however the path is spelled: case variants on case-insensitive filesystems,
// symlinks pointing into them, and the browser profile that holds logged-in sessions.
func TestFilePolicyResistsAliasing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("PRIVATE"), 0o600)
	_ = os.WriteFile(filepath.Join(home, "notes.txt"), []byte("hello"), 0o644)
	_ = os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "innocent"))
	reg, deps, _, _ := setup(t)
	_ = os.MkdirAll(filepath.Join(deps.DataDir, "browser-profile"), 0o755)
	_ = os.WriteFile(filepath.Join(deps.DataDir, "browser-profile", "Cookies"), []byte("session"), 0o644)

	if out, err := run(t, reg, "file_read", map[string]any{"path": filepath.Join(home, "notes.txt")}); err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("ordinary file must stay readable: %q %v", out, err)
	}
	blocked := []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, "innocent", "id_ed25519"), // symlink into .ssh
		filepath.Join(deps.DataDir, "browser-profile", "Cookies"),
	}
	if foldCase {
		blocked = append(blocked, filepath.Join(home, ".SSH", "id_ed25519"), filepath.Join(home, ".Ssh", "ID_ED25519"))
	}
	for _, p := range blocked {
		if out, err := run(t, reg, "file_read", map[string]any{"path": p}); err == nil || strings.Contains(out, "PRIVATE") || strings.Contains(out, "session") {
			t.Errorf("%s must be protected (out=%q err=%v)", p, out, err)
		}
	}
	// a recursive search from home must not read into protected trees or through links
	out, _ := run(t, reg, "file_search", map[string]any{"query": "PRIVATE", "path": home})
	if strings.Contains(out, "PRIVATE") || strings.Contains(out, "id_ed25519") {
		t.Fatalf("file_search leaked a protected file: %q", out)
	}
}

// Reads are auto-approved, so when untrusted content is in the turn a read outside the PRISM data dir
// must ask first (otherwise an injected instruction can read a file and then send it out).
func TestTaintedReadOutsideDataDirAsks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.WriteFile(filepath.Join(home, "diary.txt"), []byte("dear diary"), 0o644)
	reg, deps, _, _ := setup(t)
	_ = os.MkdirAll(filepath.Join(deps.DataDir, "work"), 0o755)
	_ = os.WriteFile(filepath.Join(deps.DataDir, "work", "scratch.txt"), []byte("mine"), 0o644)
	tool, _ := reg.Get("file_read")
	asked := 0
	answer := "deny"
	env := &tools.Env{Agent: "t", Tainted: true, Ask: func(ctx context.Context, q tools.Question) (string, error) {
		asked++
		return answer, nil
	}}
	rd := func(p string) (string, error) {
		b, _ := json.Marshal(map[string]any{"path": p})
		return tool.Run(context.Background(), env, b)
	}
	if out, err := rd(filepath.Join(home, "diary.txt")); err == nil || strings.Contains(out, "diary") || asked != 1 {
		t.Fatalf("denied tainted read: out=%q err=%v asked=%d", out, err, asked)
	}
	answer = "allow"
	if out, err := rd(filepath.Join(home, "diary.txt")); err != nil || !strings.Contains(out, "dear diary") || asked != 2 {
		t.Fatalf("allowed tainted read: out=%q err=%v asked=%d", out, err, asked)
	}
	if out, err := rd("scratch.txt"); err != nil || !strings.Contains(out, "mine") || asked != 2 {
		t.Fatalf("the workspace must not ask: out=%q err=%v asked=%d", out, err, asked)
	}
	env.Tainted = false
	if _, err := rd(filepath.Join(home, "diary.txt")); err != nil || asked != 2 {
		t.Fatalf("clean turns must not ask: asked=%d err=%v", asked, err)
	}
}
