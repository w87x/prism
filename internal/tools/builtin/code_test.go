package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyEditsIsPreciseAndAllOrNothing(t *testing.T) {
	src := "func a() {\n\treturn 1\n}\n\nfunc b() {\n\treturn 1\n}\n"
	// two matches: refused, with line numbers
	if _, _, err := applyEdits(src, []edit{{Old: "return 1", New: "return 2"}}); err == nil || !strings.Contains(err.Error(), "lines 2, 6") {
		t.Fatalf("ambiguous edit: %v", err)
	}
	out, notes, err := applyEdits(src, []edit{{Old: "func a() {\n\treturn 1", New: "func a() {\n\treturn 42"}})
	if err != nil || !strings.Contains(out, "return 42") || strings.Count(out, "return 1") != 1 || !strings.Contains(notes[0], "line 1") {
		t.Fatalf("unique edit: %q %v %v", out, notes, err)
	}
	if out, _, err := applyEdits(src, []edit{{Old: "return 1", New: "return 0", ReplaceAll: true}}); err != nil || strings.Count(out, "return 0") != 2 {
		t.Fatalf("replace_all: %q %v", out, err)
	}
	// trailing whitespace / CRLF differences in old_text are tolerated
	if out, notes, err := applyEdits("x := 1  \r\ny := 2\r\n", []edit{{Old: "x := 1\ny := 2", New: "x := 3\ny := 4"}}); err != nil || !strings.Contains(out, "x := 3") || !strings.Contains(notes[0], "ignoring trailing whitespace") {
		t.Fatalf("loose match: %q %v %v", out, notes, err)
	}
	// missing text: the error points at where the first line does occur
	if _, _, err := applyEdits(src, []edit{{Old: "func b() {\n\treturn 999", New: "x"}}); err == nil || !strings.Contains(err.Error(), "line 5") {
		t.Fatalf("hint: %v", err)
	}
	// all or nothing: the second edit fails, the first must not leak out
	if out, _, err := applyEdits(src, []edit{{Old: "func a", New: "func A"}, {Old: "nope", New: "x"}}); err == nil || out != "" || !strings.Contains(err.Error(), "edit 2") {
		t.Fatalf("all or nothing: %q %v", out, err)
	}
}

func TestPatchPathsCannotEscape(t *testing.T) {
	good := "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n"
	if err := checkPatchPaths(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"--- a/../../etc/passwd\n+++ b/../../etc/passwd\n@@ -1 +1 @@\n-a\n+b\n", "--- /etc/hosts\n+++ /etc/hosts\n@@ -1 +1 @@\n-a\n+b\n", "just some text"} {
		if err := checkPatchPaths(bad); err == nil {
			t.Fatalf("should refuse %q", bad)
		}
	}
}

func TestSymbolsAreFoundPerLanguage(t *testing.T) {
	goSrc := "package x\n\nfunc (s *Server) Handle(w int) {}\nfunc helper() {}\ntype Config struct{}\nconst Max = 3\n"
	var names []string
	for _, s := range fileSymbols("a.go", goSrc) {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "Handle,helper,Config,Max" {
		t.Fatalf("go symbols: %v", names)
	}
	py := "class Foo:\n    def method(self): pass\n\ndef top(): pass\nasync def later(): pass\n"
	names = nil
	for _, s := range fileSymbols("a.py", py) {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "Foo,top,later" {
		t.Fatalf("python symbols: %v", names)
	}
	ts := "export async function load() {}\nexport const value = 1\nclass Widget {}\ninterface Props {}\n"
	names = nil
	for _, s := range fileSymbols("a.ts", ts) {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "load,value,Widget,Props" {
		t.Fatalf("ts symbols: %v", names)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A small repo: search honours .gitignore, the map lists files with their symbols, symbols are located, and the
// edit and patch tools change files inside the workspace.
func TestCodeToolsOnARepository(t *testing.T) {
	reg, deps, _, _ := setup(t)
	repo := filepath.Join(deps.DataDir, "work", "proj")
	if err := os.MkdirAll(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustGit(t, repo, "init", "-q")
	write("main.go", "package main\n\nfunc main() {\n\tserve()\n}\n")
	write("pkg/serve.go", "package pkg\n\n// serve starts things\nfunc Serve() {\n\tprintln(\"serving\")\n}\n")
	write("ignored.log", "serve serve serve\n")
	write(".gitignore", "*.log\n")
	mustGit(t, repo, "add", ".gitignore", "main.go", "pkg/serve.go")
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")

	out, err := run(t, reg, "code_search", map[string]any{"pattern": "serve", "path": repo, "ignore_case": true, "context": 1})
	if err != nil || !strings.Contains(out, "main.go:4:") || !strings.Contains(out, "pkg/serve.go") || strings.Contains(out, "ignored.log") {
		t.Fatalf("search: %q %v", out, err)
	}
	if out, _ := run(t, reg, "code_search", map[string]any{"pattern": "func (\\w+)\\(", "path": repo, "glob": "main.go"}); !strings.Contains(out, "main.go:3:") || strings.Contains(out, "serve.go") {
		t.Fatalf("regex+glob: %q", out)
	}
	if out, err := run(t, reg, "repo_map", map[string]any{"path": repo}); err != nil || !strings.Contains(out, "main.go  — main") || !strings.Contains(out, "pkg/") || !strings.Contains(out, "serve.go  — Serve") {
		t.Fatalf("map: %q %v", out, err)
	}
	if out, err := run(t, reg, "code_symbols", map[string]any{"name": "Serve", "path": repo}); err != nil || !strings.Contains(out, "pkg/serve.go:4") || !strings.Contains(out, "1 definition(s)") {
		t.Fatalf("symbols: %q %v", out, err)
	}

	if out, err := run(t, reg, "file_edit", map[string]any{"path": filepath.Join(repo, "pkg/serve.go"), "old_text": "\"serving\"", "new_text": "\"serving on :8080\""}); err != nil || !strings.Contains(out, "serving on :8080") {
		t.Fatalf("edit: %q %v", out, err)
	}
	b, _ := os.ReadFile(filepath.Join(repo, "pkg/serve.go"))
	if !strings.Contains(string(b), "serving on :8080") {
		t.Fatalf("file not changed: %s", b)
	}
	if _, err := run(t, reg, "file_edit", map[string]any{"path": filepath.Join(repo, "pkg/serve.go"), "edits": []map[string]any{{"old_text": "func Serve", "new_text": "func Run"}, {"old_text": "does not exist", "new_text": "x"}}}); err == nil {
		t.Fatal("a failing edit in the batch must fail the whole call")
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "pkg/serve.go")); !strings.Contains(string(b), "func Serve") {
		t.Fatal("nothing may change when one edit of a batch fails")
	}

	patch := "--- a/main.go\n+++ b/main.go\n@@ -1,5 +1,5 @@\n package main\n \n func main() {\n-\tserve()\n+\tpkg.Serve()\n }\n"
	if out, err := run(t, reg, "apply_patch", map[string]any{"dir": repo, "patch": patch}); err != nil || !strings.Contains(out, "Patch applied") {
		t.Fatalf("patch: %q %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "main.go")); !strings.Contains(string(b), "pkg.Serve()") {
		t.Fatalf("patch not applied: %s", b)
	}
	bad := "--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,3 @@\n-nothing like this\n+x\n y\n z\n"
	if _, err := run(t, reg, "apply_patch", map[string]any{"dir": repo, "patch": bad}); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("bad patch: %v", err)
	}
	_ = context.Background()
}
