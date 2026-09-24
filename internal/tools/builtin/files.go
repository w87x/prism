package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"prism/internal/settings"
	"prism/internal/tools"
)

// Paths agents can never read, regardless of configuration (secrets), relative to the home directory.
var alwaysDenied = []string{
	".ssh", ".aws", ".gnupg", ".config/gcloud", ".config/gh", ".config/himalaya", ".kube", ".docker", ".netrc", ".npmrc", ".pgpass",
	".prism/config.json",
	"Library/Keychains", "Library/Cookies", "Library/Mail", "Library/Messages", "Library/Safari",
	"Library/Application Support/Google/Chrome", "Library/Application Support/BraveSoftware", "Library/Application Support/Firefox",
	"Library/Application Support/Microsoft Edge",
}

func homeDir() string { h, _ := os.UserHomeDir(); return h }

func (d Deps) fsConfig(ctx context.Context) FSConfig {
	return settings.Load(ctx, d.Settings, "fs", FSConfig{})
}

// foldCase is true on filesystems that are case-insensitive by default (macOS, Windows): ~/.SSH is ~/.ssh there.
var foldCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// canonical resolves symlinks in the longest existing prefix of p, so a link cannot smuggle a protected
// or out-of-bounds location past the prefix checks.
func canonical(p string) string {
	p = filepath.Clean(p)
	var rest []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				r = filepath.Join(r, rest[i])
			}
			return r
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

func hasPrefixPath(p, r string) bool {
	if len(p) < len(r) {
		return false
	}
	head := p[:len(r)]
	if foldCase {
		if !strings.EqualFold(head, r) {
			return false
		}
	} else if head != r {
		return false
	}
	return len(p) == len(r) || p[len(r)] == filepath.Separator
}

// underRoots is the cheap prefix test against already-canonical roots (used per entry while walking).
func underRoots(p string, roots []string) bool {
	for _, r := range roots {
		if hasPrefixPath(p, r) {
			return true
		}
	}
	return false
}

func underAny(p string, roots []string) bool {
	canon := make([]string, len(roots))
	for i, r := range roots {
		canon[i] = canonical(expandHome(r))
	}
	return underRoots(canonical(p), canon)
}

func (d Deps) resolve(ctx context.Context, p string) (string, error) {
	p = expandHome(strings.TrimSpace(p))
	if p == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(d.DataDir, "work", p)
	}
	return canonical(p), nil
}

// denyRoots lists every location agents may never read (built-in secrets plus the user's read-deny list).
func (d Deps) denyRoots(ctx context.Context) []string {
	home := homeDir()
	out := []string{filepath.Join(d.DataDir, "browser-profile")} // logged-in browser sessions
	for _, x := range alwaysDenied {
		out = append(out, filepath.Join(home, x))
	}
	out = append(out, d.fsConfig(ctx).ReadDeny...)
	for i, r := range out {
		out[i] = canonical(expandHome(r))
	}
	return out
}

func (d Deps) canRead(ctx context.Context, p string) error {
	if !underAny(p, []string{homeDir(), d.DataDir, os.TempDir(), "/Volumes"}) {
		return fmt.Errorf("reading outside your home directory is not allowed: %s", p)
	}
	if underRoots(canonical(p), d.denyRoots(ctx)) {
		return fmt.Errorf("path is protected or denied by configuration: %s", p)
	}
	return nil
}

// canReadTurn is canRead plus a confirmation when untrusted content is in the turn and the path is
// outside the PRISM data dir (the workspace and artifacts stay frictionless).
func (d Deps) canReadTurn(ctx context.Context, env *tools.Env, tool, p string) error {
	if err := d.canRead(ctx, p); err != nil {
		return err
	}
	if !underAny(p, []string{d.DataDir}) {
		return tools.ConfirmIfTainted(ctx, env, tool, p)
	}
	return nil
}

// DenyRoots lists the locations agents may never read (used to confine plugins the same way).
func (d Deps) DenyRoots(ctx context.Context) []string { return d.denyRoots(ctx) }

// CanReadPath applies the agents' read policy (protected locations, home/data-dir bounds) to a path.
func (d Deps) CanReadPath(ctx context.Context, p string) error {
	rp, err := d.resolve(ctx, p)
	if err != nil {
		return err
	}
	return d.canRead(ctx, rp)
}

func (d Deps) canWrite(ctx context.Context, p string) error {
	if err := d.canRead(ctx, p); err != nil {
		return err
	}
	roots := append([]string{d.DataDir}, d.fsConfig(ctx).WriteRoots...)
	if !underAny(p, roots) {
		return fmt.Errorf("writing is only allowed inside the PRISM data dir or configured write roots (%s); use artifact_save for deliverables", strings.Join(roots, ", "))
	}
	return nil
}

func registerFiles(reg *tools.Registry, d Deps) {
	reg.Register(
		&tools.Tool{
			Name: "file_read", Category: "files", Risk: tools.RiskRead,
			Description: "Read a text file (UTF-8). Relative paths resolve inside the PRISM workspace. Use offset/limit (lines) for big files.",
			Params: tools.Obj("path", tools.Str("path", "file path"), tools.Int("offset", "first line, 1-based (default 1)"),
				tools.Int("limit", "max lines (default 400)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Path          string
					Offset, Limit int
				}](raw)
				if err != nil {
					return "", err
				}
				p, err := d.resolve(ctx, a.Path)
				if err != nil {
					return "", err
				}
				if err := d.canReadTurn(ctx, env, "file_read", p); err != nil {
					return "", err
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return "", err
				}
				if !utf8.Valid(b) {
					return "", fmt.Errorf("%s is not a UTF-8 text file (%d bytes)", p, len(b))
				}
				lines := strings.Split(string(b), "\n")
				if a.Offset < 1 {
					a.Offset = 1
				}
				if a.Limit <= 0 {
					a.Limit = 400
				}
				start := a.Offset - 1
				if start >= len(lines) {
					return fmt.Sprintf("(file has %d lines)", len(lines)), nil
				}
				end := min(start+a.Limit, len(lines))
				var sb strings.Builder
				for i := start; i < end; i++ {
					fmt.Fprintf(&sb, "%d\t%s\n", i+1, lines[i])
				}
				if end < len(lines) {
					fmt.Fprintf(&sb, "…[%d more lines; continue with offset=%d]\n", len(lines)-end, end+1)
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "file_write", Category: "files", Risk: tools.RiskWrite,
			Description: "Write a text file (creates parent directories). Only inside the PRISM data dir/workspace or configured write roots. mode: overwrite (default) or append.",
			Params: tools.Obj("path,content", tools.Str("path", "file path"), tools.Str("content", "text"),
				tools.Enum("mode", "overwrite or append", "overwrite", "append")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Path, Content, Mode string }](raw)
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
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return "", err
				}
				flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
				if a.Mode == "append" {
					flag = os.O_WRONLY | os.O_CREATE | os.O_APPEND
				}
				f, err := os.OpenFile(p, flag, 0o644)
				if err != nil {
					return "", err
				}
				defer f.Close()
				n, err := f.WriteString(a.Content)
				return fmt.Sprintf("Wrote %d bytes to %s.", n, p), err
			},
		},
		&tools.Tool{
			Name: "file_list", Category: "files", Risk: tools.RiskRead,
			Description: "List a directory (optionally recursive with a glob like *.md).",
			Params: tools.Obj("path", tools.Str("path", "directory"), tools.Bool("recursive", "descend into subdirectories"),
				tools.Str("glob", "filename glob filter"), tools.Int("limit", "max entries (default 200)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Path      string
					Recursive bool
					Glob      string
					Limit     int
				}](raw)
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
				if err := d.canReadTurn(ctx, env, "file_list", p); err != nil {
					return "", err
				}
				if a.Limit <= 0 {
					a.Limit = 200
				}
				var out []string
				deny := d.denyRoots(ctx)
				walk := func(path string, de fs.DirEntry, err error) error {
					if err != nil {
						return nil
					}
					if path == p {
						return nil
					}
					if underRoots(path, deny) {
						if de.IsDir() {
							return fs.SkipDir
						}
						return nil
					}
					if de.IsDir() && strings.HasPrefix(de.Name(), ".") {
						return fs.SkipDir
					}
					if !a.Recursive && de.IsDir() {
						rel, _ := filepath.Rel(p, path)
						out = append(out, rel+"/")
						return fs.SkipDir
					}
					if a.Glob != "" {
						if ok, _ := filepath.Match(a.Glob, de.Name()); !ok {
							return nil
						}
					}
					if !de.IsDir() {
						rel, _ := filepath.Rel(p, path)
						info, _ := de.Info()
						sz := int64(0)
						if info != nil {
							sz = info.Size()
						}
						out = append(out, fmt.Sprintf("%s (%d B)", rel, sz))
					}
					if len(out) >= a.Limit {
						return errors.New("limit")
					}
					return nil
				}
				_ = filepath.WalkDir(p, walk)
				sort.Strings(out)
				if len(out) == 0 {
					return "(empty)", nil
				}
				return strings.Join(out, "\n"), nil
			},
		},
		&tools.Tool{
			Name: "file_search", Category: "files", Risk: tools.RiskRead,
			Description: "Grep-like search: find lines matching a substring (case-insensitive) in text files under a directory.",
			Params: tools.Obj("query", tools.Str("query", "substring"), tools.Str("path", "directory (default workspace)"),
				tools.Str("glob", "filename glob, e.g. *.md"), tools.Int("limit", "max matches (default 40)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Query, Path, Glob string
					Limit             int
				}](raw)
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
				if err := d.canReadTurn(ctx, env, "file_search", p); err != nil {
					return "", err
				}
				if a.Limit <= 0 {
					a.Limit = 40
				}
				q := strings.ToLower(a.Query)
				var out []string
				deny := d.denyRoots(ctx)
				_ = filepath.WalkDir(p, func(path string, de fs.DirEntry, err error) error {
					if err != nil || len(out) >= a.Limit {
						return fs.SkipAll
					}
					if underRoots(path, deny) {
						if de.IsDir() {
							return fs.SkipDir
						}
						return nil
					}
					if de.Type()&fs.ModeSymlink != 0 { // never follow links out of the searched tree
						return nil
					}
					if de.IsDir() {
						if strings.HasPrefix(de.Name(), ".") && path != p {
							return fs.SkipDir
						}
						return nil
					}
					if a.Glob != "" {
						if ok, _ := filepath.Match(a.Glob, de.Name()); !ok {
							return nil
						}
					}
					if info, e := de.Info(); e != nil || info.Size() > 2<<20 {
						return nil
					}
					b, e := os.ReadFile(path)
					if e != nil || !utf8.Valid(b) {
						return nil
					}
					for i, line := range strings.Split(string(b), "\n") {
						if strings.Contains(strings.ToLower(line), q) {
							rel, _ := filepath.Rel(p, path)
							out = append(out, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
							if len(out) >= a.Limit {
								break
							}
						}
					}
					return nil
				})
				if len(out) == 0 {
					return "No matches.", nil
				}
				return strings.Join(out, "\n"), nil
			},
		},
	)
}
