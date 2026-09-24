package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"prism/internal/tools"
)

// ── precise editing ─────────────────────────────────────────────────────────

type edit struct {
	Old        string `json:"old_text"`
	New        string `json:"new_text"`
	ReplaceAll bool   `json:"replace_all"`
}

// lineOf is the 1-based line number of byte offset i.
func lineOf(s string, i int) int { return strings.Count(s[:i], "\n") + 1 }

func indexAll(s, sub string) []int {
	var out []int
	for i := 0; ; {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return out
		}
		out = append(out, i+j)
		i += j + max(len(sub), 1)
	}
}

// looseSpan finds old in s ignoring trailing whitespace and CRLF differences on each line, the mistake models
// make most; it returns the byte span in s of the unique match.
func looseSpan(s, old string) (int, int, bool) {
	norm := func(x string) []string {
		ls := strings.Split(strings.ReplaceAll(x, "\r\n", "\n"), "\n")
		for i := range ls {
			ls[i] = strings.TrimRight(ls[i], " \t\r")
		}
		return ls
	}
	sl, ol := norm(s), norm(old)
	for len(ol) > 0 && ol[len(ol)-1] == "" {
		ol = ol[:len(ol)-1]
	}
	if len(ol) == 0 {
		return 0, 0, false
	}
	found := -1
	for i := 0; i+len(ol) <= len(sl); i++ {
		ok := true
		for j := range ol {
			if sl[i+j] != ol[j] {
				ok = false
				break
			}
		}
		if ok {
			if found >= 0 {
				return 0, 0, false // ambiguous
			}
			found = i
		}
	}
	if found < 0 {
		return 0, 0, false
	}
	raw := strings.Split(s, "\n")
	start := 0
	for i := 0; i < found; i++ {
		start += len(raw[i]) + 1
	}
	end := start
	for j := 0; j < len(ol); j++ {
		end += len(raw[found+j])
		if j < len(ol)-1 {
			end++
		}
	}
	return start, end, true
}

// applyEdits applies every edit in order to content, or none: the first problem aborts with an error that says
// what to fix. It returns the new content and one note per edit.
func applyEdits(content string, edits []edit) (string, []string, error) {
	var notes []string
	for n, e := range edits {
		label := ""
		if len(edits) > 1 {
			label = fmt.Sprintf("edit %d: ", n+1)
		}
		if e.Old == "" {
			return "", nil, fmt.Errorf("%sold_text is empty: give the exact text to replace (use file_write to create a file)", label)
		}
		if e.Old == e.New {
			return "", nil, fmt.Errorf("%sold_text and new_text are identical", label)
		}
		hits := indexAll(content, e.Old)
		switch {
		case len(hits) == 1 || (len(hits) > 1 && e.ReplaceAll):
			line := lineOf(content, hits[0])
			if e.ReplaceAll {
				content = strings.ReplaceAll(content, e.Old, e.New)
				notes = append(notes, fmt.Sprintf("%sreplaced %d occurrence(s), first at line %d", label, len(hits), line))
			} else {
				content = content[:hits[0]] + e.New + content[hits[0]+len(e.Old):]
				notes = append(notes, fmt.Sprintf("%sedited at line %d", label, line))
			}
		case len(hits) > 1:
			var ls []string
			for i, h := range hits {
				if i >= 6 {
					break
				}
				ls = append(ls, fmt.Sprint(lineOf(content, h)))
			}
			return "", nil, fmt.Errorf("%sold_text matches %d places (lines %s): include more surrounding lines to make it unique, or set replace_all", label, len(hits), strings.Join(ls, ", "))
		default:
			if a, b, ok := looseSpan(content, e.Old); ok {
				line := lineOf(content, a)
				content = content[:a] + strings.TrimRight(e.New, "\n") + content[b:]
				notes = append(notes, fmt.Sprintf("%sedited at line %d (matched ignoring trailing whitespace)", label, line))
				continue
			}
			hint := ""
			if first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(e.Old), "\n", 2)[0]); first != "" {
				if at := indexAll(content, first); len(at) > 0 {
					hint = fmt.Sprintf(" The first line of it does occur at line %d — the text after it differs; re-read that region with file_read.", lineOf(content, at[0]))
				}
			}
			return "", nil, fmt.Errorf("%sold_text was not found in the file.%s", label, hint)
		}
	}
	return content, notes, nil
}

// snippet shows lines around the first changed line, so the agent sees what its edit produced.
func snippet(content string, line int) string {
	ls := strings.Split(content, "\n")
	from, to := max(line-3, 1), min(line+6, len(ls))
	var sb strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&sb, "%d\t%s\n", i, ls[i-1])
	}
	return sb.String()
}

func writeAtomic(p string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".prism-edit-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, p)
}

// ── patches ─────────────────────────────────────────────────────────────────

var patchPathRe = regexp.MustCompile(`(?m)^(?:---|\+\+\+) (?:[ab]/)?(\S+)`)

// checkPatchPaths refuses a patch that reaches outside the directory it is applied in.
func checkPatchPaths(patch string) error {
	for _, m := range patchPathRe.FindAllStringSubmatch(patch, -1) {
		p := m[1]
		if p == "/dev/null" {
			continue
		}
		if filepath.IsAbs(p) || strings.HasPrefix(p, "..") || strings.Contains(p, "/../") {
			return fmt.Errorf("the patch touches %s, which is outside the directory it is applied in", p)
		}
	}
	if !strings.Contains(patch, "@@") {
		return errors.New("that is not a unified diff (no @@ hunks); use file_edit for a single replacement")
	}
	return nil
}

// runGit runs git in dir with a timeout and no prompts, and returns combined output.
func runGit(ctx context.Context, dir string, timeout time.Duration, stdin string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "LC_ALL=C")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := out.String()
	if len(s) > 60000 {
		s = s[:60000] + "\n…[output truncated]"
	}
	if err != nil {
		return s, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return s, nil
}

// ── listing files of a code tree ────────────────────────────────────────────

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "__pycache__": true, ".venv": true, "venv": true, ".next": true, ".svelte-kit": true, ".idea": true, ".gradle": true, "Pods": true, ".build": true}

const maxCodeFile = 1 << 20

// codeFiles lists the text files under root (relative paths, sorted). In a git checkout it asks git, which
// honours .gitignore; elsewhere it walks and skips the usual build and dependency folders.
func codeFiles(ctx context.Context, root string, limit int) ([]string, error) {
	var out []string
	if out0, err := runGit(ctx, root, 20*time.Second, "", "ls-files", "-co", "--exclude-standard", "-z"); err == nil {
		for _, f := range strings.Split(out0, "\x00") {
			if f != "" {
				out = append(out, f)
			}
		}
	} else {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") && d.Name() != ".github") {
					return filepath.SkipDir
				}
				return nil
			}
			if r, err := filepath.Rel(root, p); err == nil {
				out = append(out, r)
			}
			return nil
		})
	}
	var keep []string
	for _, f := range out {
		if st, err := os.Stat(filepath.Join(root, f)); err != nil || st.IsDir() || st.Size() > maxCodeFile {
			continue
		}
		keep = append(keep, f)
	}
	sort.Strings(keep)
	if limit > 0 && len(keep) > limit {
		keep = keep[:limit]
	}
	return keep, nil
}

func readText(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil || bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 || !utf8.Valid(b) {
		return "", false
	}
	return string(b), true
}

// ── symbols ─────────────────────────────────────────────────────────────────

var symbolRes = map[string][]*regexp.Regexp{
	".go":     {regexp.MustCompile(`^(func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)|type\s+([A-Za-z_]\w*)|(?:var|const)\s+([A-Za-z_]\w*))`)},
	".py":     {regexp.MustCompile(`^(?:async\s+)?(?:def|class)\s+([A-Za-z_]\w*)`)},
	".js":     jsSymbols,
	".jsx":    jsSymbols,
	".ts":     jsSymbols,
	".tsx":    jsSymbols,
	".mjs":    jsSymbols,
	".svelte": jsSymbols,
	".rs":     {regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:fn|struct|enum|trait|impl|mod|const|static|type)\s+(?:<[^>]*>\s*)?([A-Za-z_]\w*)`)},
	".swift":  {regexp.MustCompile(`^(?:(?:public|private|internal|fileprivate|open|final|static)\s+)*(?:func|class|struct|enum|protocol|extension|actor)\s+([A-Za-z_]\w*)`)},
	".java":   {regexp.MustCompile(`^(?:(?:public|private|protected|static|final|abstract)\s+)*(?:class|interface|enum|record)\s+([A-Za-z_]\w*)`)},
	".kt":     {regexp.MustCompile(`^(?:(?:public|private|internal|open|data|sealed)\s+)*(?:fun|class|interface|object)\s+([A-Za-z_]\w*)`)},
	".rb":     {regexp.MustCompile(`^(?:def|class|module)\s+(?:self\.)?([A-Za-z_]\w*[?!]?)`)},
	".c":      cSymbols, ".h": cSymbols, ".cpp": cSymbols, ".hpp": cSymbols, ".cc": cSymbols,
}

var jsSymbols = []*regexp.Regexp{regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:function\*?|class|interface|type|enum|const|let|var)\s+([A-Za-z_$][\w$]*)`)}
var cSymbols = []*regexp.Regexp{regexp.MustCompile(`^(?:static\s+|inline\s+|extern\s+)*(?:struct|class|enum|union|typedef\s+struct)\s+([A-Za-z_]\w*)|^[A-Za-z_][\w\s\*:<>,]*?\b([A-Za-z_]\w*)\s*\([^;]*\)\s*(?:const\s*)?\{?\s*$`)}

type symbol struct {
	Line int
	Name string
	Sig  string
}

// fileSymbols extracts top-level definitions of a source file (by extension); it is a fast lexical pass, not a parser.
func fileSymbols(name, text string) []symbol {
	res := symbolRes[strings.ToLower(filepath.Ext(name))]
	if res == nil {
		return nil
	}
	var out []symbol
	for i, ln := range strings.Split(text, "\n") {
		if ln == "" || ln[0] == ' ' || ln[0] == '\t' || ln[0] == '#' || ln[0] == '/' {
			if !strings.HasSuffix(name, ".py") || strings.HasPrefix(ln, "    ") { // python methods are indented; keep classes' methods out of the top level
				continue
			}
		}
		for _, re := range res {
			if m := re.FindStringSubmatch(ln); m != nil {
				nm := ""
				for _, g := range m[1:] {
					if g != "" && g != m[0] && !strings.ContainsAny(g, " (") {
						nm = g
					}
				}
				if nm == "" {
					continue
				}
				sig := strings.TrimSpace(ln)
				if len(sig) > 120 {
					sig = sig[:120] + "…"
				}
				out = append(out, symbol{Line: i + 1, Name: nm, Sig: strings.TrimSuffix(sig, "{")})
				break
			}
		}
	}
	return out
}

// ── registration ────────────────────────────────────────────────────────────

func registerCode(reg *tools.Registry, d Deps) {
	reg.Register(
		&tools.Tool{
			Name: "file_edit", Category: "code", Risk: tools.RiskWrite,
			Description: "Change a text file precisely: replace exact text with new text, without rewriting the file. Give old_text (copy it exactly, with enough surrounding lines to be unique) and new_text; or several edits at once in 'edits' (all applied, or none). Fails with a clear reason when the text is not found or matches several places (then add context or set replace_all). Read the region with file_read first. Only inside the workspace or configured write roots.",
			Params: tools.Obj("path",
				tools.Str("path", "file path"),
				tools.Str("old_text", "exact text to replace (with unique surrounding context)"),
				tools.Str("new_text", "replacement text (may be empty to delete)"),
				tools.Bool("replace_all", "replace every occurrence"),
				tools.ObjList("edits", "several edits applied in order, all or none", "old_text,new_text",
					tools.Str("old_text", "exact text"), tools.Str("new_text", "replacement"), tools.Bool("replace_all", "every occurrence"))),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Path string `json:"path"`
					edit
					Edits []edit `json:"edits"`
				}](raw)
				if err != nil {
					return "", err
				}
				p, err := d.resolve(ctx, a.Path)
				if err != nil {
					return "", err
				}
				if err := d.canWrite(ctx, p); err != nil {
					return "", err
				}
				edits := a.Edits
				if len(edits) == 0 {
					edits = []edit{a.edit}
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return "", err
				}
				if !utf8.Valid(b) {
					return "", fmt.Errorf("%s is not a UTF-8 text file", p)
				}
				out, notes, err := applyEdits(string(b), edits)
				if err != nil {
					return "", err
				}
				if err := writeAtomic(p, []byte(out)); err != nil {
					return "", err
				}
				line := 1
				if i := strings.Index(out, strings.TrimSpace(strings.SplitN(edits[0].New, "\n", 2)[0])); i >= 0 && strings.TrimSpace(edits[0].New) != "" {
					line = lineOf(out, i)
				}
				return fmt.Sprintf("%s: %s.\n%s", p, strings.Join(notes, "; "), snippet(out, line)), nil
			},
		},
		&tools.Tool{
			Name: "apply_patch", Category: "code", Risk: tools.RiskWrite,
			Description: "Apply a unified diff (as produced by git diff) inside a directory — for changes spanning several places or files. All hunks apply or nothing changes. Paths in the patch are relative to dir (a/ b/ prefixes are stripped). Only inside the workspace or configured write roots.",
			Params:      tools.Obj("dir,patch", tools.Str("dir", "directory the patch's paths are relative to (e.g. the repo or workspace root)"), tools.Str("patch", "the unified diff")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Dir, Patch string }](raw)
				if err != nil {
					return "", err
				}
				dir, err := d.resolve(ctx, a.Dir)
				if err != nil {
					return "", err
				}
				if err := d.canWrite(ctx, dir); err != nil {
					return "", err
				}
				if err := checkPatchPaths(a.Patch); err != nil {
					return "", err
				}
				patch := a.Patch
				if !strings.HasSuffix(patch, "\n") {
					patch += "\n"
				}
				args := []string{"apply", "--whitespace=nowarn", "-p1", "--unsafe-paths", "--directory=."}
				if out, err := runGit(ctx, dir, time.Minute, patch, append(args, "--check", "-")...); err != nil {
					return "", fmt.Errorf("the patch does not apply cleanly, nothing was changed:\n%s", strings.TrimSpace(out))
				}
				if out, err := runGit(ctx, dir, time.Minute, patch, append(args, "-")...); err != nil {
					return "", fmt.Errorf("applying failed: %s", strings.TrimSpace(out))
				}
				stat, _ := runGit(ctx, dir, time.Minute, patch, "apply", "--stat", "-p1", "-")
				return "Patch applied.\n" + strings.TrimSpace(stat), nil
			},
		},
		&tools.Tool{
			Name: "code_search", Category: "code", Risk: tools.RiskRead,
			Description: "Search source code with a regular expression (or literal text) across a project, respecting .gitignore and skipping build / dependency folders. Returns file:line matches with optional context lines. Better than file_search for code.",
			Params: tools.Obj("pattern",
				tools.Str("pattern", "regular expression (Go/RE2 syntax), or literal text with literal=true"),
				tools.Str("path", "directory to search (default: the workspace)"),
				tools.Str("glob", "only files whose name matches, e.g. *.go or *_test.go"),
				tools.Bool("literal", "treat the pattern as plain text"),
				tools.Bool("ignore_case", "case-insensitive"),
				tools.Int("context", "lines of context around each match (default 0, max 5)"),
				tools.Int("max_results", "max matches (default 60)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Pattern    string `json:"pattern"`
					Path       string `json:"path"`
					Glob       string `json:"glob"`
					Literal    bool   `json:"literal"`
					IgnoreCase bool   `json:"ignore_case"`
					Context    int    `json:"context"`
					MaxResults int    `json:"max_results"`
				}](raw)
				if err != nil {
					return "", err
				}
				if a.Path == "" {
					a.Path = "."
				}
				root, err := d.resolve(ctx, a.Path)
				if err != nil {
					return "", err
				}
				if err := d.canReadTurn(ctx, env, "code_search", root); err != nil {
					return "", err
				}
				pat := a.Pattern
				if a.Literal {
					pat = regexp.QuoteMeta(pat)
				}
				if a.IgnoreCase {
					pat = "(?i)" + pat
				}
				re, err := regexp.Compile(pat)
				if err != nil {
					return "", fmt.Errorf("bad pattern: %w", err)
				}
				if a.MaxResults <= 0 || a.MaxResults > 300 {
					a.MaxResults = 60
				}
				a.Context = min(max(a.Context, 0), 5)
				files, err := codeFiles(ctx, root, 20000)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				hits := 0
			files:
				for _, f := range files {
					if a.Glob != "" {
						if ok, _ := filepath.Match(a.Glob, filepath.Base(f)); !ok {
							continue
						}
					}
					text, ok := readText(filepath.Join(root, f))
					if !ok {
						continue
					}
					ls := strings.Split(text, "\n")
					last := -1
					for i, ln := range ls {
						if !re.MatchString(ln) {
							continue
						}
						if hits >= a.MaxResults {
							sb.WriteString(fmt.Sprintf("…more matches; narrow the pattern or path (showing %d)\n", hits))
							break files
						}
						hits++
						from, to := max(i-a.Context, 0), min(i+a.Context, len(ls)-1)
						if a.Context > 0 && from > last+1 && last >= 0 {
							sb.WriteString("--\n")
						}
						for j := max(from, last+1); j <= to; j++ {
							sep := "-"
							if j == i {
								sep = ":"
							}
							t := ls[j]
							if len(t) > 240 {
								t = t[:240] + "…"
							}
							fmt.Fprintf(&sb, "%s%s%d%s %s\n", f, sep, j+1, sep, t)
						}
						last = to
					}
				}
				if hits == 0 {
					return "No matches.", nil
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "repo_map", Category: "code", Risk: tools.RiskRead,
			Description: "Orient yourself in a codebase: the file tree of a project with the top-level functions, types and classes of each source file (Go, Python, JS/TS/Svelte, Rust, Swift, Java, Kotlin, Ruby, C/C++). Respects .gitignore. Use it first on an unfamiliar repo, then code_search / file_read for details.",
			Params: tools.Obj("", tools.Str("path", "project directory (default: the workspace)"), tools.Int("depth", "how many directory levels to list (default 4)"),
				tools.Bool("symbols", "include symbols (default true)"), tools.Int("max_chars", "output budget (default 9000)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Path     string `json:"path"`
					Depth    int    `json:"depth"`
					Symbols  *bool  `json:"symbols"`
					MaxChars int    `json:"max_chars"`
				}](raw)
				if err != nil {
					return "", err
				}
				if a.Path == "" {
					a.Path = "."
				}
				root, err := d.resolve(ctx, a.Path)
				if err != nil {
					return "", err
				}
				if err := d.canReadTurn(ctx, env, "repo_map", root); err != nil {
					return "", err
				}
				if a.Depth <= 0 {
					a.Depth = 4
				}
				if a.MaxChars <= 0 || a.MaxChars > 40000 {
					a.MaxChars = 9000
				}
				withSyms := a.Symbols == nil || *a.Symbols
				files, err := codeFiles(ctx, root, 5000)
				if err != nil {
					return "", err
				}
				return repoMap(root, files, a.Depth, withSyms, a.MaxChars), nil
			},
		},
		&tools.Tool{
			Name: "code_symbols", Category: "code", Risk: tools.RiskRead,
			Description: "Find where a function, type, class or constant is DEFINED in a project (file:line and its signature), and how many other places mention it. Faster and more precise than grepping for the name.",
			Params:      tools.Obj("name", tools.Str("name", "the identifier, or a prefix of it with prefix=true"), tools.Str("path", "project directory (default: the workspace)"), tools.Bool("prefix", "match names starting with this")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Name   string `json:"name"`
					Path   string `json:"path"`
					Prefix bool   `json:"prefix"`
				}](raw)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(a.Name) == "" {
					return "", errors.New("name is required")
				}
				if a.Path == "" {
					a.Path = "."
				}
				root, err := d.resolve(ctx, a.Path)
				if err != nil {
					return "", err
				}
				if err := d.canReadTurn(ctx, env, "code_symbols", root); err != nil {
					return "", err
				}
				files, err := codeFiles(ctx, root, 20000)
				if err != nil {
					return "", err
				}
				return findSymbols(root, files, a.Name, a.Prefix), nil
			},
		},
	)
}

func repoMap(root string, files []string, depth int, withSyms bool, budget int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s — %d files\n", filepath.Base(root), len(files))
	lastDir := ""
	shownFiles := 0
	for i, f := range files {
		dir := filepath.Dir(f)
		if strings.Count(dir, string(filepath.Separator)) >= depth {
			continue
		}
		var line strings.Builder
		if dir != lastDir {
			if dir == "." {
				line.WriteString("./\n")
			} else {
				line.WriteString(dir + "/\n")
			}
			lastDir = dir
		}
		fmt.Fprintf(&line, "  %s", filepath.Base(f))
		if withSyms {
			if text, ok := readText(filepath.Join(root, f)); ok && len(text) < 400000 {
				if ss := fileSymbols(f, text); len(ss) > 0 {
					var names []string
					for k, s := range ss {
						if k >= 10 {
							names = append(names, fmt.Sprintf("+%d more", len(ss)-k))
							break
						}
						names = append(names, s.Name)
					}
					fmt.Fprintf(&line, "  — %s", strings.Join(names, ", "))
				}
			}
		}
		line.WriteString("\n")
		if sb.Len()+line.Len() > budget {
			fmt.Fprintf(&sb, "…%d more files not shown (raise max_chars, lower depth, or map a subdirectory)\n", len(files)-i)
			return sb.String()
		}
		sb.WriteString(line.String())
		shownFiles++
	}
	return sb.String()
}

func findSymbols(root string, files []string, name string, prefix bool) string {
	var sb strings.Builder
	found := 0
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	mentions := 0
	for _, f := range files {
		text, ok := readText(filepath.Join(root, f))
		if !ok {
			continue
		}
		mentions += len(word.FindAllStringIndex(text, -1))
		for _, s := range fileSymbols(f, text) {
			if s.Name == name || (prefix && strings.HasPrefix(s.Name, name)) {
				found++
				if found <= 40 {
					fmt.Fprintf(&sb, "%s:%d  %s\n", f, s.Line, s.Sig)
				}
			}
		}
	}
	if found == 0 {
		return fmt.Sprintf("No definition of %q found (%d mentions). It may be a method, a field, or defined in a language this lexical scan does not cover — try code_search.", name, mentions)
	}
	fmt.Fprintf(&sb, "%d definition(s); the name appears %d times in the project.\n", found, mentions)
	return sb.String()
}
