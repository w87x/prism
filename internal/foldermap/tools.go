package foldermap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"prism/internal/tools"
)

func absPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		p = filepath.Join(h, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("give an absolute path (or one starting with ~/)")
	}
	return filepath.Clean(p), nil
}

// RegisterTools adds folder_map (read) and folder_index (build).
func RegisterTools(reg *tools.Registry, s *Service) {
	reg.Register(
		&tools.Tool{
			Name: "folder_map", Category: "files", Base: true, Risk: tools.RiskRead,
			Description: "Find files by what they contain, without opening them: a folder the user attached has a MAP of one-line summaries for every file and subfolder. " +
				"With query it returns the best-matching files and folders (with absolute paths, ready for file_read / doc_read); without it, an outline of the folder to the given depth. " +
				"Use it first when you need something inside an attached folder, and hand the exact paths to whoever continues the work.",
			Params: tools.Obj("path", tools.Str("path", "the folder (or a subfolder of it), absolute"), tools.Str("query", "what you are looking for, in plain words"),
				tools.Int("depth", "outline depth when no query (default 2)"), tools.Int("limit", "max lines (default 25 for a query, 60 for an outline)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Path, Query  string
					Depth, Limit int
				}](raw)
				if err != nil {
					return "", err
				}
				abs, err := absPath(a.Path)
				if err != nil {
					return "", err
				}
				if s.CanRead == nil {
					return "", errors.New("file access is not configured")
				}
				if err := s.CanRead(ctx, abs); err != nil {
					return "", err
				}
				m, rel, err := s.Locate(ctx, abs)
				if err != nil {
					return "No map covers " + abs + ". The user attaches folders in chat (the map is built then); or call folder_index to build one now.", nil
				}
				var sb strings.Builder
				switch m.Status {
				case "done":
					fmt.Fprintf(&sb, "Map of %s (%d entries", m.Root, m.Entries)
					if m.Truncated {
						fmt.Fprintf(&sb, ", the first %d only: the folder is larger", MaxEntries)
					}
					sb.WriteString(")\n")
				case "failed":
					fmt.Fprintf(&sb, "Map of %s is incomplete (%s). Partial results:\n", m.Root, m.Error)
				default:
					fmt.Fprintf(&sb, "Map of %s is still being built (%d of %d summarised); results may be incomplete.\n", m.Root, m.Done, m.Total)
				}
				line := func(e Entry) string {
					p := filepath.Join(m.Root, filepath.FromSlash(e.Path))
					if e.Path == "" {
						p = m.Root
					}
					if e.Kind == "dir" {
						p += "/"
					}
					return fmt.Sprintf("%s — %s", p, e.Summary)
				}
				if strings.TrimSpace(a.Query) != "" {
					if a.Limit <= 0 || a.Limit > 60 {
						a.Limit = 25
					}
					hits, err := s.Search(ctx, m, rel, a.Query, a.Limit)
					if err != nil {
						return "", err
					}
					if len(hits) == 0 {
						return sb.String() + "Nothing in the map matches that; try other words, or outline the folder without a query.", nil
					}
					for _, e := range hits {
						if s.CanRead(ctx, filepath.Join(m.Root, filepath.FromSlash(e.Path))) == nil {
							sb.WriteString(line(e) + "\n")
						}
					}
					return sb.String(), nil
				}
				if a.Depth <= 0 || a.Depth > 6 {
					a.Depth = 2
				}
				if a.Limit <= 0 || a.Limit > 200 {
					a.Limit = 60
				}
				es, err := s.Entries(ctx, m.ID)
				if err != nil {
					return "", err
				}
				base := 0
				if rel != "" {
					base = strings.Count(rel, "/") + 1
				}
				var sel []Entry
				for _, e := range es {
					if !within(e.Path, rel) {
						continue
					}
					d := 0
					if e.Path != "" {
						d = strings.Count(e.Path, "/") + 1
					}
					if d-base <= a.Depth {
						sel = append(sel, e)
					}
				}
				sort.Slice(sel, func(i, j int) bool { return sel[i].Path < sel[j].Path })
				n := 0
				for _, e := range sel {
					if n >= a.Limit {
						fmt.Fprintf(&sb, "…%d more; narrow with path or use a query\n", len(sel)-n)
						break
					}
					if s.CanRead(ctx, filepath.Join(m.Root, filepath.FromSlash(e.Path))) != nil {
						continue
					}
					sb.WriteString(line(e) + "\n")
					n++
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "folder_index", Category: "files", Risk: tools.RiskWrite, Auto: true,
			Description: "Build (or refresh) the folder map of a folder: the fast model writes a one-line summary for every file and subfolder in the background. Unchanged files are not redone. Check progress with folder_map.",
			Params:      tools.Obj("path", tools.Str("path", "folder to map, absolute")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Path string }](raw)
				if err != nil {
					return "", err
				}
				abs, err := absPath(a.Path)
				if err != nil {
					return "", err
				}
				m, err := s.Build(ctx, abs)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Mapping %s in the background (%d entries to summarise so far). Use folder_map to look at it; partial results are available while it runs.", m.Root, m.Total), nil
			},
		},
	)
}
