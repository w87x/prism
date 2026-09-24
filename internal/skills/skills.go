// Package skills stores SKILL.md skills, discloses only name+description to
// agents (bodies load on demand), imports skills from hubs (GitHub repos) and
// can "adapt" foreign skills so they no longer mention other agent platforms.
package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/agent"
	"prism/internal/textmatch"
	"prism/internal/tools"
)

type Skill struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Body        string    `json:"body"`
	Source      string    `json:"source"`
	Adapted     bool      `json:"adapted"`
	Enabled     bool      `json:"enabled"`
	Dir         string    `json:"dir"`
	Original    string    `json:"original,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Files       []string  `json:"files,omitempty"`
}

type Store struct {
	db      *pgxpool.Pool
	dataDir string
}

func NewStore(db *pgxpool.Pool, dataDir string) *Store {
	return &Store{db: db, dataDir: filepath.Join(dataDir, "skills")}
}

const cols = `id,name,description,body,source,adapted,enabled,dir,original,created_at`

func (s *Store) scanRows(ctx context.Context, q string, args ...any) ([]Skill, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Skill
	for rows.Next() {
		var k Skill
		if err := rows.Scan(&k.ID, &k.Name, &k.Description, &k.Body, &k.Source, &k.Adapted, &k.Enabled, &k.Dir, &k.Original, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) List(ctx context.Context) ([]Skill, error) {
	return s.scanRows(ctx, `SELECT `+cols+` FROM skills ORDER BY name`)
}

func (s *Store) Get(ctx context.Context, name string) (*Skill, error) {
	ks, err := s.scanRows(ctx, `SELECT `+cols+` FROM skills WHERE lower(name)=lower($1)`, name)
	if err != nil {
		return nil, err
	}
	if len(ks) == 0 {
		return nil, fmt.Errorf("skill %q not found", name)
	}
	k := ks[0]
	k.Files = listFiles(k.Dir)
	return &k, nil
}

func (s *Store) GetID(ctx context.Context, id int64) (*Skill, error) {
	ks, err := s.scanRows(ctx, `SELECT `+cols+` FROM skills WHERE id=$1`, id)
	if err != nil || len(ks) == 0 {
		return nil, fmt.Errorf("skill not found")
	}
	k := ks[0]
	k.Files = listFiles(k.Dir)
	return &k, nil
}

var nameRe = regexp.MustCompile(`[^a-z0-9._-]+`)

// SafeName normalises a skill name into a directory-safe slug.
func SafeName(n string) string {
	n = strings.Trim(nameRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(n)), "-"), "-.")
	if n == "" {
		n = "skill"
	}
	return n
}

func (s *Store) Save(ctx context.Context, k Skill) (int64, error) {
	k.Name = SafeName(k.Name)
	if strings.TrimSpace(k.Body) == "" {
		return 0, errors.New("skill body is empty")
	}
	if k.Description == "" {
		_, fm := ParseFrontmatter(k.Body)
		k.Description = fm["description"]
	}
	if k.Source == "" {
		k.Source = "local"
	}
	var id int64
	var err error
	if k.ID == 0 {
		err = s.db.QueryRow(ctx, `INSERT INTO skills(name,description,body,source,adapted,enabled,dir,original) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (name) DO UPDATE SET description=EXCLUDED.description, body=EXCLUDED.body, source=EXCLUDED.source, adapted=EXCLUDED.adapted, dir=EXCLUDED.dir, original=EXCLUDED.original
			RETURNING id`, k.Name, k.Description, k.Body, k.Source, k.Adapted, k.Enabled || k.ID == 0, k.Dir, k.Original).Scan(&id)
	} else {
		id = k.ID
		_, err = s.db.Exec(ctx, `UPDATE skills SET name=$2,description=$3,body=$4,source=$5,adapted=$6,enabled=$7,dir=$8,original=$9 WHERE id=$1`,
			k.ID, k.Name, k.Description, k.Body, k.Source, k.Adapted, k.Enabled, k.Dir, k.Original)
	}
	return id, err
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	var dir string
	_ = s.db.QueryRow(ctx, `DELETE FROM skills WHERE id=$1 RETURNING dir`, id).Scan(&dir)
	if dir != "" && strings.HasPrefix(dir, s.dataDir) {
		_ = os.RemoveAll(dir)
	}
	return nil
}

// Summaries implements agent.SkillSource: name+description only. With no explicit
// names it lists up to 12 enabled skills so a skill-rich install stays cheap
// (agents can skill_search for the rest).
func (s *Store) Summaries(ctx context.Context, names []string) []agent.SkillSummary {
	all, err := s.List(ctx)
	if err != nil {
		return nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	var out []agent.SkillSummary
	for _, k := range all {
		if !k.Enabled {
			continue
		}
		if len(want) > 0 && !want[strings.ToLower(k.Name)] {
			continue
		}
		out = append(out, agent.SkillSummary{Name: k.Name, Description: clip(k.Description, 140)})
		if len(want) == 0 && len(out) >= 12 {
			break
		}
	}
	return out
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func listFiles(dir string) []string {
	if dir == "" {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel != "SKILL.md" && len(out) < 60 {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// ParseFrontmatter splits a SKILL.md into body and the simple key: value frontmatter.
func ParseFrontmatter(md string) (body string, fm map[string]string) {
	fm = map[string]string{}
	md = strings.TrimPrefix(md, "\xef\xbb\xbf")
	if !strings.HasPrefix(md, "---") {
		return md, fm
	}
	rest := md[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return md, fm
	}
	block := rest[:end]
	body = strings.TrimLeft(rest[end+4:], "\r\n")
	lines := strings.Split(block, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if v == ">" || v == "|" || v == ">-" || v == "|-" { // folded/literal block
			var parts []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t") || strings.TrimSpace(lines[i+1]) == "") {
				i++
				parts = append(parts, strings.TrimSpace(lines[i]))
			}
			v = strings.Join(parts, " ")
		}
		v = strings.Trim(v, `"'`)
		fm[strings.ToLower(k)] = v
	}
	return body, fm
}

// ── tools ───────────────────────────────────────────────────────────────────

func RegisterTools(reg *tools.Registry, s *Store) {
	reg.Register(
		&tools.Tool{
			Name: "skill_search", Category: "skills", Base: true, Risk: tools.RiskRead,
			Description: "Search installed skills by capability. Returns names and one-line descriptions; load one with skill_load.",
			Params:      tools.Obj("query", tools.Str("query", "what you need to do"), tools.Int("limit", "max (default 6)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Query string
					Limit int
				}](raw)
				if err != nil {
					return "", err
				}
				all, err := s.List(ctx)
				if err != nil {
					return "", err
				}
				var cand []Skill
				var docs []string
				for _, k := range all {
					if k.Enabled {
						cand = append(cand, k)
						docs = append(docs, strings.ReplaceAll(k.Name, "-", " ")+" "+k.Description)
					}
				}
				if a.Limit == 0 {
					a.Limit = 6
				}
				hits := textmatch.Rank(a.Query, docs, a.Limit)
				if len(hits) == 0 {
					return "No matching skills.", nil
				}
				var sb strings.Builder
				for _, h := range hits {
					fmt.Fprintf(&sb, "- %s: %s\n", cand[h.Index].Name, clip(cand[h.Index].Description, 160))
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "skill_load", Category: "skills", Base: true, Risk: tools.RiskRead,
			Description: "Load a skill's full instructions (progressive disclosure: only names are in your prompt). Optionally read one of its bundled files.",
			Params:      tools.Obj("name", tools.Str("name", "skill name"), tools.Str("file", "optional bundled file path from the skill's file list")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Name, File string }](raw)
				if err != nil {
					return "", err
				}
				k, err := s.Get(ctx, a.Name)
				if err != nil {
					return "", err
				}
				if !k.Enabled {
					return "", fmt.Errorf("skill %s is disabled", k.Name)
				}
				if a.File != "" {
					p := filepath.Join(k.Dir, filepath.Clean("/"+a.File))
					if !strings.HasPrefix(p, k.Dir) {
						return "", errors.New("invalid file")
					}
					b, err := os.ReadFile(p)
					if err != nil {
						return "", err
					}
					return string(b), nil
				}
				body, _ := ParseFrontmatter(k.Body)
				out := "# Skill: " + k.Name + "\n" + body
				if len(k.Files) > 0 {
					out += "\n\n[Bundled files in " + k.Dir + ": " + strings.Join(k.Files, ", ") + " — read with skill_load(file=…), or use shell/python for scripts]"
				}
				return out, nil
			},
		},
		&tools.Tool{
			Name: "skill_write", Category: "skills", Risk: tools.RiskWrite, Only: []string{"Daedalus"},
			Description: "Save a local skill from a complete SKILL.md body (YAML frontmatter with name + description, then the procedure). Writing a name that already exists overwrites that skill — use this to correct or extend one, not to fork a near-duplicate. Load it back with skill_load.",
			Params:      tools.Obj("body", tools.Str("body", "the complete SKILL.md content: --- frontmatter (name, description) --- then the body")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Body string }](raw)
				if err != nil {
					return "", err
				}
				_, fm := ParseFrontmatter(a.Body)
				if strings.TrimSpace(fm["name"]) == "" {
					return "", errors.New("the SKILL.md frontmatter needs a name: line")
				}
				id, err := s.Save(ctx, Skill{Name: fm["name"], Description: fm["description"], Body: a.Body, Source: "local", Enabled: true})
				if err != nil {
					return "", err
				}
				name := SafeName(fm["name"])
				return fmt.Sprintf("Saved skill %q (#%d). Reusable with skill_load(name=%q).", name, id, name), nil
			},
		},
	)
}
