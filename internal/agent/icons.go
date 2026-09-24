package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"prism/internal/llm"
	"prism/internal/textmatch"
)

// ValidIcon reports whether name is one of the allowed Font Awesome icon names.
func ValidIcon(name string) bool {
	for _, i := range agentIcons {
		if i.Name == name {
			return true
		}
	}
	return false
}

func normIcon(s string) string {
	s = strings.TrimSpace(s)
	if ValidIcon(s) {
		return s
	}
	return ""
}

// GuessIcon picks the icon whose keywords best match text (offline fallback).
func GuessIcon(text string) string {
	docs := make([]string, len(agentIcons))
	for i, ic := range agentIcons {
		docs[i] = ic.Name + " " + ic.Keywords
	}
	if hits := textmatch.Rank(text, docs, 1); len(hits) > 0 {
		return agentIcons[hits[0].Index].Name
	}
	return "robot"
}

// AssignIcon chooses an icon for a profile from its soul with the fast model (keyword
// match when no model answers) and stores it. Called once, when an agent is created.
func (e *Engine) AssignIcon(ctx context.Context, id int64) {
	p, err := e.Profiles.GetID(ctx, id)
	if err != nil || p.Icon != "" {
		return
	}
	text := p.Name + " " + p.Group + " " + p.Description + " " + strings.Join(p.Traits, " ")
	icon := ""
	if e.LLM.HasChat(ctx) {
		names := make([]string, len(agentIcons))
		for i, ic := range agentIcons {
			names[i] = ic.Name
		}
		soul := p.Soul
		if len(soul) > 1200 {
			soul = soul[:1200]
		}
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		out, err := e.LLM.Complete(cctx, "role:fast", "You choose a Font Awesome icon that best represents an AI agent. Pick exactly one name from this list: "+strings.Join(names, ", ")+
			`. Answer JSON only: {"icon":"<name>"}`, "Agent: "+p.Name+"\nGroup: "+p.Group+"\nPurpose: "+p.Description+"\nSoul:\n"+soul, true)
		cancel()
		if err == nil {
			var r struct {
				Icon string `json:"icon"`
			}
			if json.Unmarshal([]byte(llm.ExtractJSON(out)), &r) == nil {
				icon = normIcon(r.Icon)
			}
		}
	}
	if icon == "" {
		icon = GuessIcon(text)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE agent_profiles SET icon=$2 WHERE id=$1 AND icon=''`, id, icon); err == nil && e.Emit != nil {
		e.Emit("agents.update", nil)
	}
}

// AssignMissingIcons gives every icon-less agent one (startup and after imports).
func (e *Engine) AssignMissingIcons(ctx context.Context) {
	rows, err := e.DB.Query(ctx, `SELECT id FROM agent_profiles WHERE icon=''`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		e.AssignIcon(ctx, id)
	}
}
