package browser

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

const jsPage = `<!doctype html><html><body><div id="root"></div><script>
setTimeout(()=>{document.getElementById('root').innerHTML='<h1>Rendered by JS</h1><button id="b" onclick="document.getElementById(\'out\').textContent=\'clicked!\'">Press me</button><div id="out"></div><input name="q" placeholder="search here">';},150);
</script></body></html>`

func TestRenderSnapshotClick(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	_ = st.Set(context.Background(), settings.KeyBrowser, settings.Browser{Headless: true})
	_ = st.Set(context.Background(), settings.KeyWeb, settings.Web{AllowPrivate: true}) // the test site is on loopback
	m := &Manager{Settings: st, DataDir: t.TempDir()}
	if !m.Available() {
		t.Skip("no Chrome installed")
	}
	defer m.Stop()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, jsPage) }))
	defer site.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	html, err := m.Render(ctx, site.URL, "#root h1", 200*time.Millisecond)
	if err != nil || !strings.Contains(html, "Rendered by JS") {
		t.Fatalf("render: %v\n%s", err, html)
	}
	if s, _ := m.State(); s != "ok" {
		t.Fatalf("state %s", s)
	}

	reg := tools.NewRegistry(d.Pool)
	m.RegisterTools(reg)
	call := func(name string, args any) string {
		tool, _ := reg.Get(name)
		b, _ := json.Marshal(args)
		out, err := tool.Run(ctx, &tools.Env{Agent: "t"}, b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	snap := call("browser_open", map[string]any{"url": site.URL})
	if !strings.Contains(snap, "Rendered by JS") || !strings.Contains(snap, `"Press me"`) {
		t.Fatalf("snapshot:\n%s", snap)
	}
	time.Sleep(300 * time.Millisecond)
	snap = call("browser_snapshot", map[string]any{})
	// find the ref of the button
	ref := 0
	for _, l := range strings.Split(snap, "\n") {
		if strings.Contains(l, `"Press me"`) {
			fmt.Sscanf(l, "[%d]", &ref)
		}
	}
	if ref == 0 {
		t.Fatalf("button ref not found:\n%s", snap)
	}
	after := call("browser_click", map[string]any{"ref": ref})
	if !strings.Contains(after, "clicked!") {
		t.Fatalf("click had no effect:\n%s", after)
	}
}

// Two tasks must get two separate tabs by default — that's the whole point of "task-owned tabs" — so one
// task navigating around never disturbs a page another task has open.
func TestTasksGetSeparateTabsByDefault(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	_ = st.Set(context.Background(), settings.KeyBrowser, settings.Browser{Headless: true})
	_ = st.Set(context.Background(), settings.KeyWeb, settings.Web{AllowPrivate: true})
	m := &Manager{Settings: st, DataDir: t.TempDir()}
	if !m.Available() {
		t.Skip("no Chrome installed")
	}
	defer m.Stop()
	siteA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<title>Page A</title><body>Alpha content</body>`)
	}))
	defer siteA.Close()
	siteB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<title>Page B</title><body>Beta content</body>`)
	}))
	defer siteB.Close()

	reg := tools.NewRegistry(d.Pool)
	m.RegisterTools(reg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	call := func(env *tools.Env, name string, args any) string {
		t.Helper()
		tool, _ := reg.Get(name)
		b, _ := json.Marshal(args)
		out, err := tool.Run(ctx, env, b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	envA := &tools.Env{Agent: "t", TaskID: 101}
	envB := &tools.Env{Agent: "t", TaskID: 202}
	call(envA, "browser_open", map[string]any{"url": siteA.URL})
	call(envB, "browser_open", map[string]any{"url": siteB.URL})

	snapA := call(envA, "browser_snapshot", map[string]any{})
	snapB := call(envB, "browser_snapshot", map[string]any{})
	if !strings.Contains(snapA, "Alpha content") || strings.Contains(snapA, "Beta content") {
		t.Fatalf("task A's tab should still show page A, unaffected by task B: %s", snapA)
	}
	if !strings.Contains(snapB, "Beta content") || strings.Contains(snapB, "Alpha content") {
		t.Fatalf("task B's tab should show page B: %s", snapB)
	}

	tabs := m.listTabs()
	if len(tabs) != 2 {
		t.Fatalf("expected 2 separate tabs, got %d: %+v", len(tabs), tabs)
	}
}

// An explicit "tab" id lets an agent share one page across calls that would otherwise default to different
// tabs, or deliberately keep two pages open within the same task.
func TestExplicitTabOverridesDefault(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	_ = st.Set(context.Background(), settings.KeyBrowser, settings.Browser{Headless: true})
	_ = st.Set(context.Background(), settings.KeyWeb, settings.Web{AllowPrivate: true})
	m := &Manager{Settings: st, DataDir: t.TempDir()}
	if !m.Available() {
		t.Skip("no Chrome installed")
	}
	defer m.Stop()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<title>Shared</title><body>Shared page</body>`)
	}))
	defer site.Close()

	reg := tools.NewRegistry(d.Pool)
	m.RegisterTools(reg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	call := func(env *tools.Env, name string, args any) string {
		t.Helper()
		tool, _ := reg.Get(name)
		b, _ := json.Marshal(args)
		out, err := tool.Run(ctx, env, b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	call(&tools.Env{Agent: "t", TaskID: 301}, "browser_open", map[string]any{"url": site.URL, "tab": "shared"})
	// a different task, same explicit tab id, must land on the SAME tab rather than getting its own
	snap := call(&tools.Env{Agent: "t", TaskID: 302}, "browser_snapshot", map[string]any{"tab": "shared"})
	if !strings.Contains(snap, "Shared page") {
		t.Fatalf("expected the explicitly-named tab to be shared across tasks: %s", snap)
	}
	if len(m.listTabs()) != 1 {
		t.Fatalf("expected exactly one tab (the shared one), got %d", len(m.listTabs()))
	}
}

// browser_tab_close must close only the named tab, leaving others (and the browser itself) untouched.
func TestTabCloseIsScopedToOneTab(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	_ = st.Set(context.Background(), settings.KeyBrowser, settings.Browser{Headless: true})
	_ = st.Set(context.Background(), settings.KeyWeb, settings.Web{AllowPrivate: true})
	m := &Manager{Settings: st, DataDir: t.TempDir()}
	if !m.Available() {
		t.Skip("no Chrome installed")
	}
	defer m.Stop()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `<title>P</title><body>hi</body>`) }))
	defer site.Close()

	reg := tools.NewRegistry(d.Pool)
	m.RegisterTools(reg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	call := func(env *tools.Env, name string, args any) string {
		t.Helper()
		tool, _ := reg.Get(name)
		b, _ := json.Marshal(args)
		out, err := tool.Run(ctx, env, b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	envA := &tools.Env{Agent: "t", TaskID: 401}
	envB := &tools.Env{Agent: "t", TaskID: 402}
	call(envA, "browser_open", map[string]any{"url": site.URL})
	call(envB, "browser_open", map[string]any{"url": site.URL})
	if len(m.listTabs()) != 2 {
		t.Fatalf("expected 2 tabs open")
	}
	call(envA, "browser_tab_close", map[string]any{})
	if len(m.listTabs()) != 1 {
		t.Fatalf("expected exactly 1 tab left after closing task A's, got %d", len(m.listTabs()))
	}
	// task B's tab must still work
	snap := call(envB, "browser_snapshot", map[string]any{})
	if !strings.Contains(snap, "hi") {
		t.Fatalf("task B's tab should be unaffected: %s", snap)
	}
}

// A CSS selector that only appears after a delay must be reliably waited for, not raced against a fixed
// sleep — this is the point of wait_selector.
func TestWaitSelectorWaitsForLateContent(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	_ = st.Set(context.Background(), settings.KeyBrowser, settings.Browser{Headless: true})
	_ = st.Set(context.Background(), settings.KeyWeb, settings.Web{AllowPrivate: true})
	m := &Manager{Settings: st, DataDir: t.TempDir()}
	if !m.Available() {
		t.Skip("no Chrome installed")
	}
	defer m.Stop()
	const page = `<title>Late</title><body><button id="go" onclick="setTimeout(()=>{const d=document.createElement('div');d.id='late';d.textContent='arrived';document.body.appendChild(d);},1500)">go</button></body>`
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, page) }))
	defer site.Close()

	reg := tools.NewRegistry(d.Pool)
	m.RegisterTools(reg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tool, _ := reg.Get("browser_open")
	b, _ := json.Marshal(map[string]any{"url": site.URL})
	if _, err := tool.Run(ctx, &tools.Env{Agent: "t", TaskID: 501}, b); err != nil {
		t.Fatal(err)
	}
	click, _ := reg.Get("browser_click")
	cb, _ := json.Marshal(map[string]any{"selector": "#go", "wait_selector": "#late"})
	out, err := click.Run(ctx, &tools.Env{Agent: "t", TaskID: 501}, cb)
	if err != nil {
		t.Fatalf("click with wait_selector: %v", err)
	}
	if !strings.Contains(out, "arrived") {
		t.Fatalf("expected wait_selector to wait for the late element: %s", out)
	}
}

// browser_download_wait must detect a completed download (renamed by Chrome from *.crdownload to its final
// name) and, without artifact storage wired, report it directly rather than losing it silently.
func TestDownloadWait(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	_ = st.Set(context.Background(), settings.KeyBrowser, settings.Browser{Headless: true})
	_ = st.Set(context.Background(), settings.KeyWeb, settings.Web{AllowPrivate: true})
	m := &Manager{Settings: st, DataDir: t.TempDir()}
	if !m.Available() {
		t.Skip("no Chrome installed")
	}
	defer m.Stop()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<title>DL</title><body><a id="dl" href="/file.txt">download</a></body>`)
	})
	mux.HandleFunc("/file.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "the downloaded content")
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	reg := tools.NewRegistry(d.Pool)
	m.RegisterTools(reg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	env := &tools.Env{Agent: "t", TaskID: 601}
	call := func(name string, args any) string {
		t.Helper()
		tool, _ := reg.Get(name)
		b, _ := json.Marshal(args)
		out, err := tool.Run(ctx, env, b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	call("browser_open", map[string]any{"url": site.URL})
	call("browser_click", map[string]any{"selector": "#dl"})
	out := call("browser_download_wait", map[string]any{"timeout_s": 15})
	if !strings.Contains(out, "report.txt") {
		t.Fatalf("expected the downloaded file to be reported: %s", out)
	}
}

// browser_upload must reach a real <input type=file> element (SetUploadFiles), gated by the same read
// policy other file tools use.
func TestUploadReachesFileInput(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	_ = st.Set(context.Background(), settings.KeyBrowser, settings.Browser{Headless: true})
	_ = st.Set(context.Background(), settings.KeyWeb, settings.Web{AllowPrivate: true})
	dataDir := t.TempDir()
	var allowedPath string
	m := &Manager{Settings: st, DataDir: dataDir, CanRead: func(ctx context.Context, p string) error {
		if p != allowedPath {
			return fmt.Errorf("not allowed: %s", p)
		}
		return nil
	}}
	if !m.Available() {
		t.Skip("no Chrome installed")
	}
	defer m.Stop()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<title>U</title><body><input id="f" type="file"></body>`)
	}))
	defer site.Close()

	upload := filepath.Join(dataDir, "to-upload.txt")
	if err := os.WriteFile(upload, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	allowedPath = upload

	reg := tools.NewRegistry(d.Pool)
	m.RegisterTools(reg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	env := &tools.Env{Agent: "t", TaskID: 701}
	call := func(name string, args any) string {
		t.Helper()
		tool, _ := reg.Get(name)
		b, _ := json.Marshal(args)
		out, err := tool.Run(ctx, env, b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	call("browser_open", map[string]any{"url": site.URL})
	call("browser_upload", map[string]any{"selector": "#f", "path": upload})

	evalTool, _ := reg.Get("browser_eval")
	eb, _ := json.Marshal(map[string]any{"js": `document.getElementById('f').files.length`})
	out, err := evalTool.Run(ctx, env, eb)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("expected the file input to have received one file, got: %s", out)
	}

	// a path the policy refuses must be refused before ever touching the tab
	denied := filepath.Join(dataDir, "denied.txt")
	_ = os.WriteFile(denied, []byte("no"), 0o644)
	tool, _ := reg.Get("browser_upload")
	db, _ := json.Marshal(map[string]any{"selector": "#f", "path": denied})
	if _, err := tool.Run(ctx, env, db); err == nil {
		t.Fatal("expected the read policy to refuse an unlisted path")
	}
}
