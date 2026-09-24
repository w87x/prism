// Package onboarding creates the user's initial agent profiles from a few hints.
// Generation is deterministic (one model call producing JSON, validated against
// the real toolset); built-in templates cover the no-model case. Forge, the
// hiring agent, remains available afterwards for on-demand hires.
package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/agent"
	"prism/internal/llm"
	"prism/internal/tools"
)

// Draft is a proposed agent that the user can review before it is created.
type Draft struct {
	Name        string   `json:"name"`
	Icon        string   `json:"icon"`
	Group       string   `json:"group"`
	Description string   `json:"description"`
	Soul        string   `json:"soul"`
	Traits      []string `json:"traits"`
	Tools       []string `json:"tools"`
	CanDelegate bool     `json:"can_delegate"`
	Exists      bool     `json:"exists"`
}

// Templates are sensible defaults used when no model is available or as a starting point.
func Templates() []Draft {
	return []Draft{
		{Name: "Scout", Icon: "magnifying-glass", Group: "Web", Description: "Finds and verifies information on the web: search, reading pages, price and news checks.",
			Traits: []string{"web research", "search", "prices", "news", "fact checking"}, Tools: []string{"web_search", "web_fetch", "web_extract", "rss_read", "bookmark_find", "bookmark_add"},
			Soul: "You are Scout, a web researcher.\n\nMethod:\n1. Restate the question in one line and decide what evidence would settle it.\n2. Check bookmark_find first for known sources, then web_search with precise queries (try 2–3 variants if the first is weak).\n3. Open the most credible sources with web_fetch; use web_extract when you need structured data (lists, prices, tables).\n4. Cross-check important facts against a second independent source. Prefer primary sources and recent dates.\n5. Report: a direct answer first, then the key evidence with source URLs and dates. Flag uncertainty and conflicting sources explicitly.\n\nRules: never fabricate URLs or numbers; quote exact prices/dates as found; keep answers compact."},
		{Name: "Cipher", Icon: "terminal", Group: "Coding", Description: "Writes, runs and debugs code; automates tasks with shell and Python.",
			Traits: []string{"coding", "python", "shell", "debugging", "automation", "scripts"}, Tools: []string{"shell", "python", "file_read", "file_write", "file_list", "file_search", "artifact_save"},
			Soul: "You are Cipher, a pragmatic software engineer.\n\nMethod:\n1. Understand the goal and constraints; inspect existing files before changing anything (file_list, file_search, file_read).\n2. Prefer small, verifiable steps: write code, run it (python/shell), read the actual output, fix, repeat.\n3. Keep work inside the PRISM workspace unless told otherwise; save deliverables with artifact_save.\n4. Never run destructive commands (rm -rf, force pushes, chmod -R) without explicit instruction.\n\nOutput: what you built or found, how to run it, and the result you actually observed. Do not claim success without having run the code."},
		{Name: "Abacus", Icon: "calculator", Group: "Data Analysis", Description: "Crunches numbers: parses data, computes statistics, builds tables and summaries.",
			Traits: []string{"data analysis", "statistics", "csv", "spreadsheets", "calculations", "python"}, Tools: []string{"python", "file_read", "file_write", "web_extract", "artifact_save"},
			Soul: "You are Abacus, a careful data analyst.\n\nMethod:\n1. Inspect the data first: shape, types, missing values, obvious anomalies.\n2. Do all arithmetic in Python — never by hand — and print intermediate results.\n3. State assumptions and units. Sanity-check results (orders of magnitude, totals that must add up).\n4. Present findings as a short summary plus a compact table; save larger outputs (CSV, markdown) with artifact_save.\n\nRules: report what the data says, not what would be nice; call out data-quality problems."},
		{Name: "Quill", Icon: "pen-nib", Group: "Writing", Description: "Drafts and edits texts, keeps notes in Obsidian, summarizes documents.",
			Traits: []string{"writing", "editing", "summaries", "notes", "obsidian", "documents"}, Tools: []string{"obsidian_search", "obsidian_read", "obsidian_write", "obsidian_append", "obsidian_daily", "file_read", "semantic_search"},
			Soul: "You are Quill, a writer and note-keeper.\n\nMethod:\n1. For writing tasks, ask yourself: audience, purpose, tone, length. Infer from context; ask only if it changes the result.\n2. Search the user's notes (obsidian_search / semantic_search) for relevant material and style before drafting.\n3. Draft, then tighten: cut filler, prefer concrete words. Match the user's voice when examples exist.\n4. When saving notes, use clear titles, short paragraphs and [[wikilinks]] to related notes; never overwrite existing notes — append or create new ones.\n\nOutput: the text itself first, then a one-line note on choices made."},
		{Name: "Hearth", Icon: "house", Group: "Home & Life", Description: "Personal organizer: reminders, downloads and watches, daily routines and follow-ups.",
			Traits: []string{"reminders", "schedule", "organizer", "downloads", "watch", "routines", "macos"}, Tools: []string{"mac_reminder_add", "mac_notify", "mac_open", "intent_create", "intent_list", "intent_cancel", "cron_create", "cron_list", "cron_delete", "download_start", "download_status", "watch_command", "clock"},
			Soul: "You are Hearth, the user's personal organizer.\n\nYou set reminders, schedule recurring routines, watch long-running things (downloads, copies, releases) and make sure the user is told at the right moment.\n\nMethod:\n1. Get the exact time from the clock tool before creating anything time-based; state absolute times back to the user.\n2. Use mac_reminder_add for personal reminders, cron_create for recurring jobs, intent_create for “tell me when X happens”, watch_command for progress of long tasks.\n3. Confirm what you set up in one line, including when it will fire. List and clean up stale intents when asked.\n\nRules: never create noisy schedules; prefer one well-worded reminder over many."},
	}
}

const planPrompt = `You design the initial team of specialist AI agents for a personal assistant called PRISM. The user gave you hints about themselves and their needs.

Plan %d agents. For each give:
- "name": a short, memorable, unique first-name-like name (NOT descriptive like "SearchBot"). Must not be any of: %s.
- "group": a broad category (Web, Coding, Data Analysis, Writing, Research, Home, Finance, Learning, Media, Health, ...).
- "description": one line — what it is good for (shown in the agent catalog).
- "traits": 4-8 lowercase search keywords.
- "tools": the minimal set of tool names from the list below that the agent really needs (3-9).
- "can_delegate": true only for agents that coordinate broader work.

Cover the user's stated needs first; avoid overlapping roles; do not create agents for things the built-in staff already do (memory curation, agent hiring, tool selection).

Available tools (name — purpose):
%s

Answer JSON only: {"agents":[{"name":"","group":"","description":"","traits":[],"tools":[],"can_delegate":false}]}`

const soulPrompt = `Write the system prompt ("soul") for an AI agent inside a personal assistant. 120-250 words, plain text, no markdown headings, no code fences: its role, a numbered working method, the output format, and hard rules. Tailor it to what the user needs. Do not list tools. Output ONLY the prompt text, starting with "You are <Name>, ...".`

// Progress reports the generation pipeline to the caller (the UI streams it).
type Progress struct {
	Stage string // planning | writing | done
	Note  string
	Draft *Draft // a finished draft
	Total int
}

// Propose plans a team with the chat model, then writes each agent's soul in its own
// call so slow local models make visible progress instead of one huge request. On
// failure it falls back to templates. progress may be nil.
func Propose(ctx context.Context, r *llm.Router, reg *tools.Registry, existing []string, hints string, count int, model string, progress func(Progress)) (drafts []Draft, usedModel bool, err error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	if count <= 0 {
		count = 5
	}
	have := map[string]bool{}
	for _, n := range existing {
		have[strings.ToLower(n)] = true
	}
	mark := func(ds []Draft) []Draft {
		for i := range ds {
			ds[i].Exists = have[strings.ToLower(ds[i].Name)]
		}
		return ds
	}
	fallback := func(why string) ([]Draft, bool, error) {
		return mark(Templates()), false, errors.New(why + " — showing built-in templates")
	}
	if !r.HasChat(ctx) {
		return fallback("no chat model configured")
	}
	var tl, mcpTools []string
	valid := map[string]bool{}
	for _, t := range reg.All() {
		valid[t.Name] = true
		// MCP tools are registered as Deferred (their schemas load on demand), but a specialist can perfectly
		// well be given one in its toolset — leaving them out meant the planner never proposed any.
		if t.Deferred && strings.HasPrefix(t.Category, "mcp:") {
			d := t.Description
			if i := strings.IndexAny(d, ".\n"); i > 0 {
				d = d[:i]
			}
			if r := []rune(d); len(r) > 90 {
				d = string(r[:90]) + "…"
			}
			mcpTools = append(mcpTools, fmt.Sprintf("%s — %s", t.Name, d))
			continue
		}
		if t.Base || t.Deferred || strings.HasPrefix(t.Name, "agent_") || strings.HasPrefix(t.Name, "memory_") || t.Name == "delegate" || t.Name == "evolve_propose" || t.Name == "task_status" || t.Name == "notify_user" {
			continue
		}
		d := t.Description
		if i := strings.IndexAny(d, ".\n"); i > 0 {
			d = d[:i]
		}
		tl = append(tl, fmt.Sprintf("%s — %s", t.Name, d))
	}
	if n := len(mcpTools); n > 0 {
		const maxMCP = 60
		if n > maxMCP {
			mcpTools = append(mcpTools[:maxMCP], fmt.Sprintf("…and %d more MCP tools (same prefixes)", n-maxMCP))
		}
		tl = append(tl, "", "MCP tools (from connected external servers — give an agent the ones its job needs):")
		tl = append(tl, mcpTools...)
	}
	userHints := "Hints from the user:\n" + strings.TrimSpace(hints)
	if strings.TrimSpace(hints) == "" {
		userHints = "The user gave no hints: propose a broadly useful starting team."
	}

	progress(Progress{Stage: "planning", Note: "Planning the team…", Total: count})
	pctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	out, err := r.Complete(pctx, model, fmt.Sprintf(planPrompt, count, strings.Join(existing, ", "), strings.Join(tl, "\n")), userHints, true)
	cancel()
	if err != nil {
		return fallback(fmt.Sprintf("planning failed (%v)", err))
	}
	var plan struct {
		Agents []Draft `json:"agents"`
	}
	if err := json.Unmarshal([]byte(llm.ExtractJSON(out)), &plan); err != nil || len(plan.Agents) == 0 {
		return fallback("the model returned no usable plan")
	}
	seen := map[string]bool{}
	var items []Draft
	for _, d := range plan.Agents {
		d.Name = strings.TrimSpace(d.Name)
		if d.Name == "" || seen[strings.ToLower(d.Name)] {
			continue
		}
		seen[strings.ToLower(d.Name)] = true
		var ts []string
		for _, t := range d.Tools {
			if valid[t] {
				ts = append(ts, t)
			}
		}
		d.Tools = ts
		if d.Group == "" {
			d.Group = "General"
		}
		items = append(items, d)
		if len(items) >= count {
			break
		}
	}
	if len(items) == 0 {
		return fallback("the model returned no usable plan")
	}
	for i := range items {
		progress(Progress{Stage: "writing", Note: fmt.Sprintf("Writing %s (%d/%d)…", items[i].Name, i+1, len(items)), Total: len(items)})
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Minute)
		soul, err := r.Complete(wctx, model, soulPrompt, fmt.Sprintf("%s\n\nAgent: %s\nGroup: %s\nPurpose: %s\nTraits: %s", userHints, items[i].Name, items[i].Group, items[i].Description, strings.Join(items[i].Traits, ", ")), false)
		wcancel()
		soul = strings.TrimSpace(strings.Trim(strings.TrimSpace(soul), "`"))
		if err != nil || len(soul) < 40 {
			// keep going: a matching template soul (or a minimal one) still yields a usable agent
			soul = fallbackSoul(items[i])
		}
		items[i].Soul = soul
		items[i].Exists = have[strings.ToLower(items[i].Name)]
		d := items[i]
		progress(Progress{Stage: "writing", Draft: &d, Total: len(items)})
	}
	progress(Progress{Stage: "done", Total: len(items)})
	return mark(items), true, nil
}

func fallbackSoul(d Draft) string {
	return fmt.Sprintf("You are %s, a specialist in %s. %s\n\nMethod:\n1. Clarify the goal and constraints from the instruction you received.\n2. Use your tools step by step; verify results before relying on them.\n3. Report the outcome concisely with the evidence behind it.\n\nRules: never invent facts; say when you are unsure; keep answers compact.", d.Name, strings.ToLower(d.Group), d.Description)
}

// Apply creates the drafts as agents. With replace, existing non-system worker agents are removed first.
func Apply(ctx context.Context, ps *agent.ProfileStore, drafts []Draft, replace bool) (created int, err error) {
	if replace {
		all, err := ps.List(ctx)
		if err != nil {
			return 0, err
		}
		for _, p := range all {
			if !p.System {
				if err := ps.Delete(ctx, p.ID); err != nil {
					return 0, err
				}
			}
		}
	}
	for _, d := range drafts {
		if _, err := ps.Get(ctx, d.Name); err == nil {
			continue // never overwrite an existing agent
		}
		if _, err := ps.Save(ctx, agent.Profile{Name: d.Name, Icon: d.Icon, Group: d.Group, Description: d.Description, Soul: d.Soul, Traits: d.Traits, Tools: d.Tools,
			CanDelegate: d.CanDelegate, Role: agent.RoleWorker, AutoTools: true, Enabled: true}, "onboarding"); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
