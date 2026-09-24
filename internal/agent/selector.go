package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/textmatch"
	"prism/internal/tools"
)

// ToolSelectorConfig is stored under settings key "tool_selector".
type ToolSelectorConfig struct {
	Enabled bool `json:"enabled"`
	UseLLM  bool `json:"use_llm"` // refine lexical candidates with Sherpa's model
	Max     int  `json:"max"`
}

// selectTools is "Sherpa": it looks at the task and picks extra tools from the
// repository so agents do not carry every schema in every prompt. Lexical
// ranking is free; with use_llm the fast model filters the candidates.
func (e *Engine) selectTools(ctx context.Context, task string, active map[string]bool, agent string) []string {
	cfg := settings.Load(ctx, e.Settings, "tool_selector", ToolSelectorConfig{Enabled: true, Max: 4})
	if !cfg.Enabled {
		return nil
	}
	if cfg.Max <= 0 {
		cfg.Max = 4
	}
	var cand []*tools.Tool
	var docs []string
	for _, t := range e.Tools.All() {
		if active[t.Name] || t.Base || !e.Tools.State(t.Name).Enabled || !t.AllowedFor(agent) {
			continue
		}
		cand = append(cand, t)
		docs = append(docs, strings.ReplaceAll(t.Name, "_", " ")+" "+t.Category+" "+t.Description)
	}
	hits := textmatch.Rank(task, docs, cfg.Max*3)
	if len(hits) == 0 {
		return nil
	}
	top := hits[0].Score
	var picked []*tools.Tool
	for _, h := range hits {
		if h.Score < top*0.45 { // drop weak matches
			continue
		}
		picked = append(picked, cand[h.Index])
		if len(picked) >= cfg.Max*2 {
			break
		}
	}
	if cfg.UseLLM && len(picked) > 1 {
		if names, ok := e.llmSelect(ctx, task, picked, cfg.Max); ok {
			return names
		}
	}
	if len(picked) > cfg.Max {
		picked = picked[:cfg.Max]
	}
	out := make([]string, len(picked))
	for i, t := range picked {
		out[i] = t.Name
	}
	return out
}

func (e *Engine) llmSelect(ctx context.Context, task string, cand []*tools.Tool, max int) ([]string, bool) {
	sherpa, err := e.Profiles.Get(ctx, "Sherpa")
	ref := "role:fast"
	sys := "You choose which tools an agent needs for a task."
	if err == nil {
		if sherpa.Model != "" {
			ref = sherpa.Model
		}
		if strings.TrimSpace(sherpa.Soul) != "" {
			sys = sherpa.Soul
		}
	}
	var lines []string
	for _, t := range cand {
		lines = append(lines, fmt.Sprintf("- %s: %s", t.Name, t.Description))
	}
	out, err := e.LLM.Complete(ctx, ref, sys+fmt.Sprintf("\nPick at most %d tools that are truly needed. Answer JSON only: {\"tools\":[\"name\",...]}", max),
		"Task:\n"+task+"\n\nCandidates:\n"+strings.Join(lines, "\n"), true)
	if err != nil {
		return nil, false
	}
	var parsed struct {
		Tools []string `json:"tools"`
	}
	if json.Unmarshal([]byte(llm.ExtractJSON(out)), &parsed) != nil {
		return nil, false
	}
	valid := map[string]bool{}
	for _, t := range cand {
		valid[t.Name] = true
	}
	var res []string
	for _, n := range parsed.Tools {
		if valid[n] {
			res = append(res, n)
		}
	}
	return res, true
}
