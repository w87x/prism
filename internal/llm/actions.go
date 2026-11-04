package llm

import (
	"context"
	"strings"

	"prism/internal/settings"
)

// Action is something PRISM asks a model to do behind the scenes, with the model class it uses unless the user chose otherwise:
// "main" (the chat model) or "aux" (the fast model). The Settings page lists them; ActionRef is how each call site resolves its model.
type Action struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Group   string `json:"group"`
	Default string `json:"default"` // main | aux
}

// Actions is every behind-the-scenes model use that can be re-assigned. The defaults are what the code used before the list existed.
var Actions = []Action{
	{"plan", "Classify and decompose each message (the front door)", "Chat", "aux"},
	{"chat_naming", "Name chats, suggest projects, summarise merged chats", "Chat", "aux"},
	{"background_summary", "Summarise finished background work", "Chat", "aux"},
	{"compaction", "Compact a long conversation", "Chat", "aux"},
	{"icons", "Choose agents' icons", "Agents", "aux"},
	{"routine", "Routine jobs (cron, standing intents)", "Autonomy", "aux"},
	{"escalate", "Answer an escalation from a routine job", "Autonomy", "main"},
	{"notice_filter", "Judge whether a background report is worth telling you", "Autonomy", "aux"},
	{"promise_review", "Check that cron runs did what they said", "Autonomy", "aux"},
	{"acceptance", "Check a delegated result against its acceptance criteria", "Autonomy", "main"},
	{"audit", "Sampled re-check of a cheap model's routine runs", "Autonomy", "main"},
	{"runbook", "Write a runbook for a routine job from its successful runs", "Autonomy", "main"},
	{"tool_review", "Pre-approve or refuse a tool call before asking you", "Autonomy", "main"},
	{"standup", "Write the weekly team standup", "Autonomy", "aux"},
	{"jobs_review", "Review scheduled jobs: merge near-duplicates, rewrite one that keeps failing", "Autonomy", "main"},
	{"handover", "Write a handover note when a task stops", "Tasks", "aux"},
	{"person_model", "Update the working-with-you profile from how you actually use PRISM", "Memory", "main"},
	{"restart_review", "Judge a task resumed after a restart", "Tasks", "aux"},
	{"task_review", "Review a task that stopped early", "Tasks", "aux"},
	{"recover", "Plan the retry of a failed task", "Tasks", "aux"},
	{"task_summary", "Summarise finished tasks", "Tasks", "aux"},
	{"memory_extract", "Distil raw messages into facts", "Memory", "aux"},
	{"memory_dedupe", "Decide whether a new fact repeats or corrects an old one", "Memory", "aux"},
	{"skill_tidy", "Merge skills that teach the same procedure", "Autonomy", "main"},
	{"memory_sweep", "Judge look-alike facts across the whole memory and merge true duplicates", "Memory", "main"},
	{"memory_rerank", "Re-rank memory search results", "Memory", "aux"},
	{"memory_reflect", "Draw conclusions from facts (reflection)", "Memory", "aux"},
	{"memory_analyze", "Deep analysis: insights, questions, contradictions", "Memory", "main"},
	{"memory_banks", "Suggest merging and splitting banks", "Memory", "aux"},
	{"entities", "Extract and merge entities", "Memory", "aux"},
	{"web_guard", "Check a web search for private details before it is sent, and rewrite or hold it back", "Web", "aux"},
	{"web_extract", "Pull data out of web pages", "Web", "aux"},
}

// ActionRef is the model reference an action runs on: the user's choice for it (settings.ModelActions) or def ("chat" for the main
// model, "fast" for the auxiliary one) when they made none.
func (r *Router) ActionRef(ctx context.Context, id, def string) string {
	choice := settings.Load(ctx, r.settings, settings.KeyModelActions, settings.ModelActions{}).Choice[id]
	switch choice {
	case "main":
		def = "chat"
	case "aux":
		def = "fast"
	}
	return "action:" + id + ":role:" + def
}

// splitActionRef keeps the action attached through all normal model resolution and fallback paths.
func splitActionRef(ref string) (string, string) {
	if s, ok := strings.CutPrefix(ref, "action:"); ok {
		if action, model, ok := strings.Cut(s, ":"); ok && action != "" && model != "" {
			return action, model
		}
	}
	return "", ref
}

// ModelRef returns the underlying model reference without its action label.
func ModelRef(ref string) string { _, model := splitActionRef(ref); return model }

// Background operations need room for small-model reasoning, but should not generate indefinitely.
// Complex analysis is intentionally left to the model's own configured limit.
func actionOutputLimit(action string) int {
	switch action {
	case "plan", "memory_extract", "memory_reflect", "person_model", "chat_naming", "background_summary", "acceptance", "task_summary":
		return 8192
	default:
		return 0
	}
}
