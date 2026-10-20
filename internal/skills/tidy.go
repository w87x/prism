package skills

import (
	"context"
	"fmt"
	"strings"

	"prism/internal/textutil"
)

// Skills pile up: Daedalus writes one per stalled task, and a dozen differently worded tasks give a dozen skills that all say "do not loop on
// the same URL". Every skill an agent can see costs prompt space and a chance to pick the wrong one, so Tidy asks the main model which skills
// teach the same procedure and merges each group into one. The merged-away skills are switched off, not deleted (with a note saying where they
// went), and every agent that had one of them pinned gets the merged skill instead.
const tidyPrompt = `You keep an assistant's library of skills (reusable procedures) tidy. Below are the skills: name, description, and the start of the procedure.
Find groups of 2 or more skills that teach THE SAME procedure or avoid THE SAME mistake, in different words or for slightly different cases, and merge each group into ONE skill that keeps every distinct step, gotcha and example of its members but says each thing once. Skills about different procedures stay alone — most skills should be left out. When in doubt, do not merge.
For each group give "members" (the names to merge) and the merged skill as a complete SKILL.md ("body": YAML frontmatter with name and description, then the steps). Reuse a member's name for it when that reads well.
Answer JSON only: {"groups":[{"members":["a","b"],"body":"---\nname: ...\ndescription: ...\n---\n..."}]}`

// TidyResult says what a pass did.
type TidyResult struct {
	Merged   int      `json:"merged"`   // groups merged
	Disabled []string `json:"disabled"` // skills switched off
}

func (r TidyResult) String() string {
	if r.Merged == 0 {
		return "skills: nothing to merge"
	}
	return fmt.Sprintf("skills: %d groups merged (%s switched off)", r.Merged, strings.Join(r.Disabled, ", "))
}

// Tidy runs one pass and reports it in a line (it is what the agent package's SkillSource asks for).
func (s *Store) Tidy(ctx context.Context) (string, error) {
	r, err := s.tidy(ctx)
	return r.String(), err
}

// tidy runs one pass over the enabled local skills.
func (s *Store) tidy(ctx context.Context) (TidyResult, error) {
	var res TidyResult
	if s.LLM == nil || s.LLM.RoleRef(ctx, "chat") == "" {
		return res, nil
	}
	all, err := s.List(ctx)
	if err != nil {
		return res, err
	}
	byName := map[string]Skill{}
	var sb strings.Builder
	n := 0
	for _, k := range all {
		if !k.Enabled || k.Source != "local" {
			continue
		}
		byName[k.Name] = k
		body, _ := ParseFrontmatter(k.Body)
		fmt.Fprintf(&sb, "## %s\n%s\n%s\n\n", k.Name, k.Description, textutil.Brief(body, 700))
		n++
	}
	if n < 2 {
		return res, nil
	}
	var parsed struct {
		Groups []struct {
			Members []string `json:"members"`
			Body    string   `json:"body"`
		} `json:"groups"`
	}
	if err := s.LLM.CompleteJSON(ctx, s.LLM.ActionRef(ctx, "skill_tidy", "chat"), tidyPrompt, sb.String(), &parsed); err != nil {
		return res, fmt.Errorf("skill tidy: %w", err)
	}
	used := map[string]bool{}
	for _, g := range parsed.Groups {
		var members []string
		for _, m := range g.Members {
			m = SafeName(m)
			if _, ok := byName[m]; ok && !used[m] {
				members = append(members, m)
			}
		}
		_, fm := ParseFrontmatter(g.Body)
		name := SafeName(fm["name"])
		if len(members) < 2 || strings.TrimSpace(fm["name"]) == "" || strings.TrimSpace(fm["description"]) == "" {
			continue
		}
		if _, clash := byName[name]; clash && !contains(members, name) {
			continue // the merged skill would overwrite an unrelated one
		}
		for _, m := range members {
			used[m] = true
		}
		if _, err := s.Save(ctx, Skill{Name: name, Description: fm["description"], Body: g.Body, Source: "local", Enabled: true}); err != nil {
			continue
		}
		for _, m := range members {
			if m == name {
				continue
			}
			k := byName[m]
			_, _ = s.db.Exec(ctx, `UPDATE skills SET enabled=false, description=$2 WHERE id=$1`, k.ID, "(merged into "+name+") "+k.Description)
			res.Disabled = append(res.Disabled, m)
		}
		if s.Rehome != nil {
			s.Rehome(ctx, members, name)
		}
		res.Merged++
	}
	return res, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
