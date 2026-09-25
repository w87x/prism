package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/tools"
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

// The workspace lifecycle: an isolated worktree, a diff that includes new files, apply into a clean checkout as
// staged changes (refused into a dirty one), keep as a branch, and discard.
func TestWorkspacesIsolateCodingWork(t *testing.T) {
	reg, deps, _, _ := setup(t)
	ctx := context.Background()
	repo := filepath.Join(deps.DataDir, "work", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "a.txt")
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")

	if _, _, err := deps.OpenWorkspace(ctx, filepath.Join(deps.DataDir, "nope"), "", "Coder", 0); err == nil {
		t.Fatal("a non-repository must be refused")
	}
	out, err := run(t, reg, "workspace_open", map[string]any{"repo": repo, "name": "Fix A"})
	if err != nil || !strings.Contains(out, "Workspace #1") {
		t.Fatalf("open: %q %v", out, err)
	}
	ws, _ := Workspaces(ctx, deps.DB)
	if len(ws) != 1 || ws[0].Status != "open" || !strings.HasPrefix(ws[0].Branch, "prism/fix-a-") {
		t.Fatalf("workspaces = %+v", ws)
	}
	wt := ws[0].Path
	if _, err := run(t, reg, "file_edit", map[string]any{"path": filepath.Join(wt, "a.txt"), "old_text": "one", "new_text": "two"}); err != nil {
		t.Fatalf("editing inside the workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("brand new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "one\n" {
		t.Fatal("the user's checkout must stay untouched while the agent works")
	}
	diff, err := run(t, reg, "workspace_diff", map[string]any{"id": 1})
	if err != nil || !strings.Contains(diff, "+two") || !strings.Contains(diff, "new.txt") {
		t.Fatalf("diff: %q %v", diff, err)
	}
	if out, err := run(t, reg, "git_status", map[string]any{"dir": wt}); err != nil || !strings.Contains(out, "a.txt") || !strings.Contains(out, "new.txt") {
		t.Fatalf("git_status: %q %v", out, err)
	}
	if _, err := run(t, reg, "git_commit", map[string]any{"dir": wt, "message": "change a, add new", "all": true}); err != nil {
		t.Fatalf("git_commit in the workspace: %v", err)
	}
	if out, err := run(t, reg, "git_log", map[string]any{"dir": wt, "n": 2}); err != nil || !strings.Contains(out, "change a, add new") {
		t.Fatalf("git_log: %q %v", out, err)
	}
	// a dirty checkout blocks applying
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkspaceApply(ctx, deps.DB, 1); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty checkout: %v", err)
	}
	_ = os.Remove(filepath.Join(repo, "scratch.txt"))
	msg, err := WorkspaceApply(ctx, deps.DB, 1)
	if err != nil || !strings.Contains(msg, "staged") {
		t.Fatalf("apply: %q %v", msg, err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "two\n" {
		t.Fatalf("changes not applied: %q", b)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("the worktree should be gone after applying")
	}
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "apply")

	// keep: the branch stays in the repo; discard removes it
	if _, _, err := deps.OpenWorkspace(ctx, repo, "second", "Coder", 0); err != nil {
		t.Fatal(err)
	}
	ws, _ = Workspaces(ctx, deps.DB)
	var open Workspace
	for _, w := range ws {
		if w.Status == "open" {
			open = w
		}
	}
	if err := os.WriteFile(filepath.Join(open.Path, "k.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg, err := WorkspaceKeep(ctx, deps.DB, open.ID); err != nil || !strings.Contains(msg, open.Branch) {
		t.Fatalf("keep: %q %v", msg, err)
	}
	if out, _ := runGit(ctx, repo, 10*time.Second, "", "branch", "--list", open.Branch); !strings.Contains(out, open.Branch) {
		t.Fatalf("the kept branch must exist: %q", out)
	}
	if d, err := WorkspaceDiff(ctx, deps.DB, open.ID, true); err != nil || !strings.Contains(d, "k.txt") {
		t.Fatalf("kept diff: %q %v", d, err)
	}
	if err := WorkspaceDiscard(ctx, deps.DB, open.ID); err != nil {
		t.Fatal(err)
	}
	if out, _ := runGit(ctx, repo, 10*time.Second, "", "branch", "--list", open.Branch); strings.TrimSpace(out) != "" {
		t.Fatalf("the branch must be gone: %q", out)
	}
}

func TestGitHubArgumentsAreBuiltSafely(t *testing.T) {
	args, err := ghReadArgs(ghArgs{Kind: "pr", Action: "list", State: "open", Limit: 5, Author: "octo", Repo: "acme/app"})
	if err != nil || strings.Join(args, " ") != "pr list --limit 5 --state open --author octo -R acme/app" {
		t.Fatalf("list: %v %v", args, err)
	}
	if args, err := ghReadArgs(ghArgs{Kind: "run", Action: "log", Number: "123"}); err != nil || strings.Join(args, " ") != "run view 123 --log-failed" {
		t.Fatalf("run log: %v %v", args, err)
	}
	for _, bad := range []ghArgs{{Kind: "pr", Action: "view", Number: "1; rm -rf"}, {Kind: "pr", Action: "list", State: "--web"}, {Kind: "pr", Action: "list", Repo: "--repo=x"}, {Kind: "issue", Action: "list", State: "merged"}, {Kind: "nope", Action: "x"}} {
		if _, err := ghReadArgs(bad); err == nil {
			t.Fatalf("should refuse %+v", bad)
		}
	}
	args, err = ghWriteArgs(ghArgs{Action: "pr_create", Title: "Fix --draft parsing", Body: "-- body starting with dashes", Base: "main", Draft: true})
	if err != nil || args[0] != "pr" || args[3] != "Fix --draft parsing" || args[5] != "-- body starting with dashes" || args[len(args)-1] != "--draft" {
		t.Fatalf("pr_create: %v %v", args, err)
	}
	if _, err := ghWriteArgs(ghArgs{Action: "pr_comment", Number: "7"}); err == nil {
		t.Fatal("an empty comment must be refused")
	}
	if args, err := ghWriteArgs(ghArgs{Action: "issue_comment", Number: "7", Body: "thanks"}); err != nil || strings.Join(args, " ") != "issue comment 7 --body thanks" {
		t.Fatalf("issue_comment: %v %v", args, err)
	}
}

// With a fake gh on PATH the tool runs end to end.
func TestGHToolRunsTheCLI(t *testing.T) {
	reg, deps, _, _ := setup(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho \"gh called with: $@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_ = deps
	out, err := run(t, reg, "gh_read", map[string]any{"kind": "pr", "action": "view", "number": "42", "comments": true})
	if err != nil || !strings.Contains(out, "gh called with: pr view 42 --comments") {
		t.Fatalf("gh_read: %q %v", out, err)
	}
	if tool, _ := reg.Get("gh_read"); !tool.Untrusted {
		t.Fatal("GitHub content must taint the turn")
	}
	if tool, _ := reg.Get("gh_write"); tool.Auto {
		t.Fatal("writing to GitHub must not be armed silently")
	}
}

func TestRepoScanFindsCommandsAndLayout(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x\n")
	write("cmd/app/main.go", "package main\n")
	write("internal/a.go", "package internal\n")
	write("web/package.json", `{"scripts":{"build":"vite build","test":"vitest"}}`)
	write("package.json", `{"scripts":{"build":"vite build","test":"vitest","lint":"eslint ."}}`)
	write("pnpm-lock.yaml", "")
	write("Makefile", "build:\n\tgo build\ntest:\n\tgo test ./...\nrun:\n\t./x\n")
	write("README.md", "# X\n\n![badge](x)\n\nX is a tiny tool that does things\nacross lines.\n\nMore.\n")
	write("AGENTS.md", "rules")
	mustGit(t, root, "init", "-q")
	mustGit(t, root, "remote", "add", "origin", "https://user:secret@example.com/acme/x.git")
	rs := scanRepo(ctx, root)
	all := strings.Join(rs.Facts(), "\n")
	for _, want := range []string{"go test ./...", "pnpm build", "pnpm lint", "make test", "make run", "cmd/ internal/ web/", "AGENTS.md", "X is a tiny tool that does things across lines.", "Go (2 files)", "https://example.com/acme/x.git"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "secret") {
		t.Fatal("remote credentials must never reach memory")
	}
}

// Checks run in the workspace with the repository's own commands; the result is remembered, goes stale when the
// code changes, and saved commands override what the scan suggests.
func TestWorkspaceVerifyRunsTheProjectChecks(t *testing.T) {
	reg, deps, _, _ := setup(t)
	ctx := context.Background()
	repo := filepath.Join(deps.DataDir, "work", "checked")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte("test:\n\t@test -f ok.flag || (echo 'FAIL: ok.flag missing'; exit 1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "Makefile")
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	if _, err := run(t, reg, "workspace_open", map[string]any{"repo": repo}); err != nil {
		t.Fatal(err)
	}
	wt := func() string { ws, _ := Workspaces(ctx, deps.DB); return ws[0].Path }()

	if out, _ := run(t, reg, "workspace_diff", map[string]any{"id": 1}); !strings.Contains(out, "not run yet") {
		t.Fatalf("diff before verify: %q", out)
	}
	st, rep, err := VerifyWorkspace(ctx, deps.DB, 1)
	if err != nil || st != "fail" || !strings.Contains(rep, "✗ test: make test") || !strings.Contains(rep, "ok.flag missing") {
		t.Fatalf("failing check: %s %q %v", st, rep, err)
	}
	if err := os.WriteFile(filepath.Join(wt, "ok.flag"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, reg, "workspace_verify", map[string]any{"id": 1}); err != nil || !strings.HasPrefix(out, "PASS") || !strings.Contains(out, "✓ test") {
		t.Fatalf("passing check: %q %v", out, err)
	}
	if out, _ := run(t, reg, "workspace_diff", map[string]any{"id": 1}); !strings.Contains(out, "Checks: pass") || strings.Contains(out, "changed since") {
		t.Fatalf("diff after verify: %q", out)
	}
	if err := os.WriteFile(filepath.Join(wt, "more.txt"), []byte("later change"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ws, _ := Workspaces(ctx, deps.DB); !ws[0].Stale || ws[0].VerifyStatus != "pass" {
		t.Fatalf("a changed workspace must show its verification as stale: %+v", ws[0])
	}
	// a saved command replaces the scanned one
	wsList, _ := Workspaces(ctx, deps.DB)
	repo = wsList[0].Repo // the canonical repository path the app itself uses
	if err := SetCommands(ctx, deps.DB, CodeCommands{Repo: repo, Test: "echo custom-test-ran"}); err != nil {
		t.Fatal(err)
	}
	if st, rep, _ := VerifyWorkspace(ctx, deps.DB, 1); st != "pass" || !strings.Contains(rep, "echo custom-test-ran") {
		t.Fatalf("saved command: %s %q", st, rep)
	}
	if c, _ := Commands(ctx, deps.DB, repo); c.Test != "echo custom-test-ran" || c.DetectedTest != "make test" {
		t.Fatalf("commands = %+v", c)
	}
}

func TestVerdictIsReadFromAReviewReport(t *testing.T) {
	for in, want := range map[string]string{
		"Findings...\n\nVERDICT: approve with fixes": "approve with fixes",
		"blah\nVerdict: **reject**\n":                "reject",
		"Verdict: approve":                           "approve",
		"no verdict here":                            "",
	} {
		if got := verdictOf(in); got != want {
			t.Errorf("verdictOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// A workspace opened by a task is found by that task's id until a review is linked; the reviewer's verdict is
// then read from the review task's result.
func TestWorkspaceReviewLinkAndVerdict(t *testing.T) {
	reg, deps, _, _ := setup(t)
	ctx := context.Background()
	repo := filepath.Join(deps.DataDir, "work", "rv")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "a.txt")
	mustGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	tool, _ := reg.Get("workspace_open")
	if _, err := tool.Run(ctx, &tools.Env{Agent: "Coder", TaskID: 77}, []byte(`{"repo":"`+repo+`"}`)); err != nil {
		t.Fatal(err)
	}
	ws, err := OpenWorkspacesOfTask(ctx, deps.DB, 77)
	if err != nil || len(ws) != 1 {
		t.Fatalf("open workspaces of task 77: %+v %v", ws, err)
	}
	var rid int64
	if err := deps.DB.QueryRow(ctx, `INSERT INTO tasks(from_kind,to_agent,title,input,status,result,root_id) VALUES('user','Reviewer','Review','x','done','Findings: none.\nVERDICT: approve with fixes',0) RETURNING id`).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkspaceReview(ctx, deps.DB, ws[0].ID, rid); err != nil {
		t.Fatal(err)
	}
	if again, _ := OpenWorkspacesOfTask(ctx, deps.DB, 77); len(again) != 0 {
		t.Fatal("a reviewed workspace must not be reviewed again")
	}
	all, _ := Workspaces(ctx, deps.DB)
	if all[0].Verdict != "approve with fixes" || !strings.Contains(all[0].Review, "Findings") {
		t.Fatalf("review = %q / %q", all[0].Verdict, all[0].Review)
	}
}
