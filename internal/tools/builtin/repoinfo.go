package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"prism/internal/tools"
)

// RepoSummary is what can be learned about a codebase from its files alone: what it is written in, how it is built,
// tested and linted, and how it is laid out. It is meant to be stored once as facts in the repository's project bank,
// so the next coding task starts knowing the commands instead of rediscovering them.
type RepoSummary struct {
	Name      string
	Path      string
	Remote    string
	Languages []string // "Go (312 files)"
	Build     []string
	Test      []string
	Lint      []string
	Run       []string
	Layout    []string // top-level directories
	Guides    []string // files agents should read for conventions
	Readme    string
}

var langByExt = map[string]string{".go": "Go", ".py": "Python", ".js": "JavaScript", ".jsx": "JavaScript", ".mjs": "JavaScript", ".ts": "TypeScript", ".tsx": "TypeScript", ".svelte": "Svelte", ".vue": "Vue",
	".rs": "Rust", ".swift": "Swift", ".java": "Java", ".kt": "Kotlin", ".rb": "Ruby", ".php": "PHP", ".c": "C", ".h": "C", ".cpp": "C++", ".cc": "C++", ".cs": "C#", ".sh": "Shell", ".sql": "SQL"}

var credsRe = regexp.MustCompile(`//[^/@\s]+:[^/@\s]*@`)

func addOnce(dst *[]string, v string) {
	for _, x := range *dst {
		if x == v {
			return
		}
	}
	*dst = append(*dst, v)
}

func scanRepo(ctx context.Context, root string) RepoSummary {
	rs := RepoSummary{Name: filepath.Base(root), Path: root}
	exists := func(n string) bool { _, err := os.Stat(filepath.Join(root, n)); return err == nil }
	read := func(n string) string { b, _ := os.ReadFile(filepath.Join(root, n)); return string(b) }

	if out, err := runGit(ctx, root, 10*time.Second, "", "remote", "get-url", "origin"); err == nil {
		rs.Remote = credsRe.ReplaceAllString(strings.TrimSpace(out), "//")
	}
	files, _ := codeFiles(ctx, root, 20000)
	counts := map[string]int{}
	tops := map[string]bool{}
	for _, f := range files {
		if l := langByExt[strings.ToLower(filepath.Ext(f))]; l != "" {
			counts[l]++
		}
		if i := strings.IndexByte(f, filepath.Separator); i > 0 {
			tops[f[:i]] = true
		}
	}
	type lc struct {
		l string
		n int
	}
	var ls []lc
	for l, n := range counts {
		ls = append(ls, lc{l, n})
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].n > ls[j].n || (ls[i].n == ls[j].n && ls[i].l < ls[j].l) })
	for i, x := range ls {
		if i >= 4 {
			break
		}
		rs.Languages = append(rs.Languages, fmt.Sprintf("%s (%d files)", x.l, x.n))
	}
	for d := range tops {
		if !strings.HasPrefix(d, ".") {
			rs.Layout = append(rs.Layout, d+"/")
		}
	}
	sort.Strings(rs.Layout)

	if exists("go.mod") {
		addOnce(&rs.Build, "go build ./...")
		addOnce(&rs.Test, "go test ./...")
		addOnce(&rs.Lint, "go vet ./...")
	}
	if exists("package.json") {
		var pj struct {
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal([]byte(read("package.json")), &pj)
		pm := "npm run"
		switch {
		case exists("pnpm-lock.yaml"):
			pm = "pnpm"
		case exists("yarn.lock"):
			pm = "yarn"
		case exists("bun.lockb") || exists("bun.lock"):
			pm = "bun run"
		}
		for _, k := range []string{"build", "test", "lint", "dev", "start"} {
			if _, ok := pj.Scripts[k]; ok {
				cmd := fmt.Sprintf("%s %s", pm, k)
				switch k {
				case "build":
					addOnce(&rs.Build, cmd)
				case "test":
					addOnce(&rs.Test, cmd)
				case "lint":
					addOnce(&rs.Lint, cmd)
				default:
					addOnce(&rs.Run, cmd)
				}
			}
		}
	}
	if mk := read("Makefile"); mk != "" {
		for _, m := range regexp.MustCompile(`(?m)^([A-Za-z][\w-]*):`).FindAllStringSubmatch(mk, -1) {
			switch m[1] {
			case "build", "all":
				addOnce(&rs.Build, "make "+m[1])
			case "test", "check":
				addOnce(&rs.Test, "make "+m[1])
			case "lint", "vet", "fmt":
				addOnce(&rs.Lint, "make "+m[1])
			case "run", "dev", "serve":
				addOnce(&rs.Run, "make "+m[1])
			}
		}
	}
	if exists("pyproject.toml") || exists("requirements.txt") || exists("setup.py") {
		py := read("pyproject.toml") + read("requirements.txt")
		if strings.Contains(py, "pytest") || exists("pytest.ini") || exists("tests") {
			addOnce(&rs.Test, "pytest")
		}
		if strings.Contains(py, "ruff") {
			addOnce(&rs.Lint, "ruff check .")
		}
	}
	if exists("Cargo.toml") {
		addOnce(&rs.Build, "cargo build")
		addOnce(&rs.Test, "cargo test")
		addOnce(&rs.Lint, "cargo clippy")
	}
	if exists("Package.swift") {
		addOnce(&rs.Build, "swift build")
		addOnce(&rs.Test, "swift test")
	}
	if exists("pom.xml") {
		addOnce(&rs.Build, "mvn package")
		addOnce(&rs.Test, "mvn test")
	}
	if exists("build.gradle") || exists("build.gradle.kts") {
		addOnce(&rs.Build, "./gradlew build")
		addOnce(&rs.Test, "./gradlew test")
	}
	if exists("Gemfile") {
		addOnce(&rs.Test, "bundle exec rspec")
	}
	for _, g := range []string{"AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md", ".cursorrules", "docs/architecture.md", "architecture.md"} {
		if exists(g) {
			rs.Guides = append(rs.Guides, g)
		}
	}
	for _, para := range strings.Split(read("README.md"), "\n\n") {
		p := strings.TrimSpace(para)
		if p != "" && !strings.HasPrefix(p, "#") && !strings.HasPrefix(p, "![") && !strings.HasPrefix(p, "[!") && !strings.HasPrefix(p, "<") {
			p = strings.Join(strings.Fields(p), " ")
			if len(p) > 240 {
				p = p[:240] + "…"
			}
			rs.Readme = p
			break
		}
	}
	return rs
}

// Facts are the self-contained sentences worth storing about the repository.
func (r RepoSummary) Facts() []string {
	var out []string
	head := fmt.Sprintf("The repository %q is at %s", r.Name, r.Path)
	if r.Remote != "" {
		head += " (remote " + r.Remote + ")"
	}
	if len(r.Languages) > 0 {
		head += "; it is mainly " + strings.Join(r.Languages, ", ")
	}
	out = append(out, head+".")
	if r.Readme != "" {
		out = append(out, fmt.Sprintf("What %s is: %s", r.Name, r.Readme))
	}
	add := func(what string, cmds []string) {
		if len(cmds) > 0 {
			out = append(out, fmt.Sprintf("In %s, %s with: %s.", r.Name, what, strings.Join(cmds, " ; ")))
		}
	}
	add("build", r.Build)
	add("run the tests", r.Test)
	add("lint or vet", r.Lint)
	add("start it locally", r.Run)
	if len(r.Layout) > 0 {
		out = append(out, fmt.Sprintf("Top-level layout of %s: %s.", r.Name, strings.Join(r.Layout, " ")))
	}
	if len(r.Guides) > 0 {
		out = append(out, fmt.Sprintf("%s has convention guides to read before changing code: %s.", r.Name, strings.Join(r.Guides, ", ")))
	}
	return out
}

func (r RepoSummary) String() string {
	var sb strings.Builder
	for _, f := range r.Facts() {
		sb.WriteString("- " + f + "\n")
	}
	return sb.String()
}

func registerRepoInfo(reg *tools.Registry, d Deps) {
	reg.Register(&tools.Tool{
		Name: "repo_scan", Category: "code", Risk: tools.RiskRead,
		Description: "Learn how a repository is built and tested: languages, build / test / lint / run commands (from go.mod, package.json, Makefile, pyproject, Cargo.toml…), layout and convention guides. Returns facts you should then store with memory_store in the project bank (project:<repo name>, tag 'repo') so later tasks start knowing them.",
		Params:      tools.Obj("", tools.Str("path", "repository directory (default: the workspace)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ Path string }](raw)
			if err != nil {
				return "", err
			}
			if a.Path == "" {
				a.Path = "."
			}
			p, err := d.resolve(ctx, a.Path)
			if err != nil {
				return "", err
			}
			if err := d.canReadTurn(ctx, env, "repo_scan", p); err != nil {
				return "", err
			}
			rs := scanRepo(ctx, p)
			return rs.String() + fmt.Sprintf("\nStore the useful ones with memory_store (bank \"project:%s\", tags [\"repo\"]).", rs.Name), nil
		},
	})
}
