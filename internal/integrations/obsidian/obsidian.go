// Package obsidian gives agents access to an Obsidian vault (often inside
// iCloud): search, read, write, append and daily notes — all confined to the vault.
package obsidian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"prism/internal/settings"
	"prism/internal/textmatch"
	"prism/internal/tools"
)

// Detect looks for vaults (directories containing .obsidian) in the usual places,
// including the iCloud container that Obsidian's iOS/macOS sync uses.
func Detect() []string {
	home, _ := os.UserHomeDir()
	var roots []string
	icloud := filepath.Join(home, "Library", "Mobile Documents", "iCloud~md~obsidian", "Documents")
	roots = append(roots, icloud, filepath.Join(home, "Documents"), filepath.Join(home, "Obsidian"), home)
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, r := range roots {
		if st, err := os.Stat(filepath.Join(r, ".obsidian")); err == nil && st.IsDir() {
			add(r)
		}
		ents, err := os.ReadDir(r)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") && r == home {
				continue
			}
			p := filepath.Join(r, e.Name())
			if st, err := os.Stat(filepath.Join(p, ".obsidian")); err == nil && st.IsDir() {
				add(p)
			}
		}
	}
	return out
}

type Vault struct {
	Settings *settings.Store

	mu    sync.Mutex
	cache map[string]*note // rel path → indexed note
}

type note struct {
	mtime time.Time
	text  string
}

func (v *Vault) root(ctx context.Context) (string, error) {
	c := settings.Load(ctx, v.Settings, settings.KeyObsidian, settings.Obsidian{})
	if c.VaultPath == "" {
		return "", errors.New("no Obsidian vault configured (Settings → Obsidian)")
	}
	r, err := filepath.EvalSymlinks(expand(c.VaultPath))
	if err != nil {
		return "", fmt.Errorf("vault not accessible: %w", err)
	}
	return r, nil
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}

// WriteNote creates a note at a vault-relative path (parent folders are created); it refuses to overwrite
// an existing note unless overwrite is true. Returns the vault-relative path actually written. Used by
// obsidian_write (agents) and by the UI's "save to Obsidian" actions (briefings and the like).
func (v *Vault) WriteNote(ctx context.Context, path, content string, overwrite bool) (string, error) {
	p, rel, err := v.resolve(ctx, path, true)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil && !overwrite {
		return "", fmt.Errorf("%s already exists; use obsidian_append or set overwrite=true", rel)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return rel, os.WriteFile(p, []byte(content), 0o644)
}

// Configured reports whether a vault path is set and readable (status LED).
func (v *Vault) Configured(ctx context.Context) (bool, string) {
	r, err := v.root(ctx)
	if err != nil {
		return false, err.Error()
	}
	return true, r
}

// resolve confines a vault-relative path to the vault (no traversal, no symlink escape).
func (v *Vault) resolve(ctx context.Context, rel string, forWrite bool) (string, string, error) {
	root, err := v.root(ctx)
	if err != nil {
		return "", "", err
	}
	rel = strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+rel)), "/")
	if rel == "" {
		return root, rel, nil
	}
	if !strings.HasSuffix(strings.ToLower(rel), ".md") && filepath.Ext(rel) == "" {
		rel += ".md"
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	check := p
	if forWrite {
		check = filepath.Dir(p)
		for {
			if _, err := os.Stat(check); err == nil || check == root || check == "/" {
				break
			}
			check = filepath.Dir(check)
		}
	}
	if real, err := filepath.EvalSymlinks(check); err == nil {
		if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
			return "", "", errors.New("path escapes the vault")
		}
	}
	if strings.Contains(rel, ".obsidian/") || strings.HasPrefix(rel, ".obsidian") {
		return "", "", errors.New("the .obsidian config folder is off limits")
	}
	return p, rel, nil
}

func (v *Vault) refresh(root string) (skipped int, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cache == nil {
		v.cache = map[string]*note{}
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != root {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".icloud") { // not downloaded from iCloud yet
			skipped++
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		seen[rel] = true
		info, err := d.Info()
		if err != nil || info.Size() > 512<<10 {
			return nil
		}
		if n, ok := v.cache[rel]; ok && n.mtime.Equal(info.ModTime()) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		v.cache[rel] = &note{mtime: info.ModTime(), text: string(b)}
		return nil
	})
	for k := range v.cache {
		if !seen[k] {
			delete(v.cache, k)
		}
	}
	return skipped, err
}

// Search ranks notes by BM25 over title+content.
func (v *Vault) Search(ctx context.Context, query string, limit int) ([]string, error) {
	root, err := v.root(ctx)
	if err != nil {
		return nil, err
	}
	skipped, _ := v.refresh(root)
	v.mu.Lock()
	rels := make([]string, 0, len(v.cache))
	docs := make([]string, 0, len(v.cache))
	for rel, n := range v.cache {
		rels = append(rels, rel)
		t := n.text
		if len(t) > 6000 {
			t = t[:6000]
		}
		docs = append(docs, strings.TrimSuffix(filepath.Base(rel), ".md")+" "+strings.TrimSuffix(filepath.Base(rel), ".md")+" "+t)
	}
	v.mu.Unlock()
	hits := textmatch.Rank(query, docs, limit)
	var out []string
	for _, h := range hits {
		v.mu.Lock()
		txt := ""
		if n := v.cache[rels[h.Index]]; n != nil {
			txt = n.text
		}
		v.mu.Unlock()
		out = append(out, fmt.Sprintf("%s\n    %s", strings.TrimSuffix(rels[h.Index], ".md"), snippet(txt, query)))
	}
	if skipped > 0 {
		out = append(out, fmt.Sprintf("(note: %d files are still iCloud placeholders and were not searched)", skipped))
	}
	return out, nil
}

func snippet(text, query string) string {
	l := strings.ToLower(text)
	for _, w := range textmatch.Tokens(query) {
		if i := strings.Index(l, w); i >= 0 {
			a := max(0, i-60)
			b := min(len(text), i+140)
			return strings.Join(strings.Fields(text[a:b]), " ")
		}
	}
	return strings.Join(strings.Fields(text[:min(len(text), 160)]), " ")
}

func (v *Vault) daily(ctx context.Context) (string, error) {
	root, err := v.root(ctx)
	if err != nil {
		return "", err
	}
	folder, format := "", "2006-01-02"
	if b, err := os.ReadFile(filepath.Join(root, ".obsidian", "daily-notes.json")); err == nil {
		var c struct{ Folder, Format string }
		if json.Unmarshal(b, &c) == nil {
			folder = c.Folder
			if c.Format != "" {
				format = momentToGo(c.Format)
			}
		}
	}
	return filepath.ToSlash(filepath.Join(folder, time.Now().Format(format)+".md")), nil
}

// momentToGo converts the common moment.js tokens used by Obsidian's daily-notes format.
func momentToGo(f string) string {
	r := strings.NewReplacer("YYYY", "2006", "YY", "06", "MMMM", "January", "MMM", "Jan", "MM", "01", "DD", "02", "dddd", "Monday", "ddd", "Mon")
	return r.Replace(f)
}

// RegisterTools installs the obsidian_* tools.
func RegisterTools(reg *tools.Registry, v *Vault) {
	reg.Register(
		&tools.Tool{
			Name: "obsidian_search", Category: "obsidian", Risk: tools.RiskRead,
			Description: "Search the user's Obsidian vault (ranked by relevance to title and content). Returns note paths with a snippet.",
			Params:      tools.Obj("query", tools.Str("query", "keywords"), tools.Int("limit", "max notes (default 8)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Query string
					Limit int
				}](raw)
				if err != nil {
					return "", err
				}
				if a.Limit <= 0 {
					a.Limit = 8
				}
				res, err := v.Search(ctx, a.Query, a.Limit)
				if err != nil {
					return "", err
				}
				if len(res) == 0 {
					return "No matching notes.", nil
				}
				return strings.Join(res, "\n"), nil
			},
		},
		&tools.Tool{
			Name: "obsidian_read", Category: "obsidian", Risk: tools.RiskRead,
			Description: "Read a note by its vault-relative path (with or without .md).",
			Params:      tools.Obj("path", tools.Str("path", "e.g. Projects/Trip plan")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Path string }](raw)
				if err != nil {
					return "", err
				}
				p, _, err := v.resolve(ctx, a.Path, false)
				if err != nil {
					return "", err
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return "", err
				}
				return string(b), nil
			},
		},
		&tools.Tool{
			Name: "obsidian_list", Category: "obsidian", Risk: tools.RiskRead,
			Description: "List notes and folders in a vault folder (default: vault root).",
			Params:      tools.Obj("", tools.Str("dir", "vault-relative folder")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Dir string }](raw)
				if err != nil {
					return "", err
				}
				root, err := v.root(ctx)
				if err != nil {
					return "", err
				}
				p := root
				if a.Dir != "" {
					if p, _, err = v.resolve(ctx, strings.TrimSuffix(a.Dir, ".md"), false); err != nil {
						return "", err
					}
					p = strings.TrimSuffix(p, ".md")
				}
				ents, err := os.ReadDir(p)
				if err != nil {
					return "", err
				}
				var out []string
				for _, e := range ents {
					n := e.Name()
					if strings.HasPrefix(n, ".") {
						continue
					}
					if e.IsDir() {
						out = append(out, n+"/")
					} else if strings.HasSuffix(strings.ToLower(n), ".md") {
						out = append(out, strings.TrimSuffix(n, ".md"))
					}
				}
				sort.Strings(out)
				return strings.Join(out, "\n"), nil
			},
		},
		&tools.Tool{
			Name: "obsidian_write", Category: "obsidian", Risk: tools.RiskWrite,
			Description: "Create a note (parent folders are created). Refuses to overwrite an existing note unless overwrite=true; prefer obsidian_append for existing notes.",
			Params:      tools.Obj("path,content", tools.Str("path", "vault-relative path"), tools.Str("content", "markdown"), tools.Bool("overwrite", "replace an existing note")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Path, Content string
					Overwrite     bool
				}](raw)
				if err != nil {
					return "", err
				}
				rel, err := v.WriteNote(ctx, a.Path, a.Content, a.Overwrite)
				if err != nil {
					return "", err
				}
				return "Wrote " + rel, nil
			},
		},
		&tools.Tool{
			Name: "obsidian_append", Category: "obsidian", Risk: tools.RiskWrite,
			Description: "Append text to a note (created if missing).",
			Params:      tools.Obj("path,text", tools.Str("path", "vault-relative path"), tools.Str("text", "markdown to append")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Path, Text string }](raw)
				if err != nil {
					return "", err
				}
				return appendTo(ctx, v, a.Path, a.Text)
			},
		},
		&tools.Tool{
			Name: "obsidian_daily", Category: "obsidian", Risk: tools.RiskWrite,
			Description: "Append a line/block to today's daily note (honours the vault's daily-notes settings); with no text it returns today's note.",
			Params:      tools.Obj("", tools.Str("text", "markdown to append (omit to read)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Text string }](raw)
				if err != nil {
					return "", err
				}
				rel, err := v.daily(ctx)
				if err != nil {
					return "", err
				}
				if a.Text == "" {
					p, _, err := v.resolve(ctx, rel, false)
					if err != nil {
						return "", err
					}
					b, err := os.ReadFile(p)
					if err != nil {
						return "Today's note does not exist yet (" + rel + ").", nil
					}
					return string(b), nil
				}
				return appendTo(ctx, v, rel, a.Text)
			},
		},
	)
}

func appendTo(ctx context.Context, v *Vault, path, text string) (string, error) {
	p, rel, err := v.resolve(ctx, path, true)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if st, _ := f.Stat(); st != nil && st.Size() > 0 {
		text = "\n" + text
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, err = f.WriteString(text)
	return "Appended to " + rel, err
}
