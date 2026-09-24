package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"prism/internal/llm"
	"prism/internal/memory"
	"prism/internal/settings"
	"prism/internal/tasks"
)

// Command describes a slash command available in chat (web and Telegram).
type Command struct {
	Name string `json:"name"`
	Args string `json:"args"`
	Help string `json:"help"`
}

// Commands is the catalogue, borrowed from Claude Code / OpenClaw / Hermes where it fits a
// memory-centric assistant: context control (/new /compact), introspection (/status /agents
// /tasks /skills), memory (/memory /remember), model switching and export.
var Commands = []Command{
	{"help", "", "list commands"},
	{"new", "", "start fresh: forget this conversation's context (the log stays)"},
	{"clear", "", "wipe the conversation context and the chat log"},
	{"compact", "", "summarize old turns now to free context"},
	{"stop", "", "stop what Atlas is doing"},
	{"status", "", "model, context usage, queue and memory at a glance"},
	{"memory", "<query>", "search long-term memory"},
	{"remember", "<fact>", "store a fact about you in memory"},
	{"agents", "", "list agents"},
	{"tasks", "", "recent tasks"},
	{"skills", "", "installed skills"},
	{"model", "[name]", "show or switch the default chat model"},
	{"export", "", "save this conversation as a markdown artifact"},
}

// RunCommand executes a slash command line. handled is false when line is not a command.
func (e *Engine) RunCommand(ctx context.Context, channel, topic, line string) (reply string, handled bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return "", false
	}
	name, args, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	name, args = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(args)
	if i := strings.IndexByte(name, '@'); i > 0 { // Telegram: /status@botname
		name = name[:i]
	}
	known := false
	for _, c := range Commands {
		known = known || c.Name == name
	}
	if !known {
		return "", false // e.g. /pair, /start, /setgroup are handled by their channel
	}
	if channel == "" {
		channel = "web"
	}
	key := ChatKey(channel, topic)
	switch name {
	case "help":
		var sb strings.Builder
		sb.WriteString("Commands:\n")
		for _, c := range Commands {
			fmt.Fprintf(&sb, "/%s %s — %s\n", c.Name, c.Args, c.Help)
		}
		return sb.String(), true
	case "new":
		if err := e.ClearChat(ctx, channel, topic, false); err != nil {
			return "Error: " + err.Error(), true
		}
		return "Context cleared — starting fresh.", true
	case "clear":
		if err := e.ClearChat(ctx, channel, topic, true); err != nil {
			return "Error: " + err.Error(), true
		}
		return "Conversation wiped.", true
	case "compact":
		if err := e.CompactChat(ctx, channel, topic); err != nil {
			return "Compaction failed: " + err.Error(), true
		}
		return "Context compacted.", true
	case "stop":
		if e.StopChat(key) {
			return "Stopped.", true
		}
		return "Nothing is running.", true
	case "status":
		return e.statusText(ctx, key), true
	case "memory":
		if args == "" {
			return "Usage: /memory <query>", true
		}
		banks := []string{"user"}
		if bs, err := e.Memory.Banks(ctx); err == nil {
			for _, b := range bs {
				if b.Kind != memory.KindUser {
					banks = append(banks, b.Label())
				}
			}
		}
		fs, err := e.Memory.Find(ctx, memory.FindReq{Query: args, Banks: banks, K: 6})
		if err != nil {
			return "Error: " + err.Error(), true
		}
		if len(fs) == 0 {
			return "Nothing relevant in memory.", true
		}
		var sb strings.Builder
		for _, f := range fs {
			fmt.Fprintf(&sb, "• %s  (%s)\n", f.Text, f.Bank)
		}
		return sb.String(), true
	case "remember":
		if args == "" {
			return "Usage: /remember <fact>", true
		}
		r, err := e.Memory.Store(ctx, memory.StoreReq{Bank: "user", Text: args, Source: "user", Confidence: 0.95})
		if err != nil {
			return "Error: " + err.Error(), true
		}
		if r.Duplicate {
			return "Already known — reinforced.", true
		}
		return "Remembered.", true
	case "agents":
		ps, err := e.Profiles.List(ctx)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		var sb strings.Builder
		for _, p := range ps {
			state := ""
			if !p.Enabled {
				state = " (disabled)"
			}
			fmt.Fprintf(&sb, "• %s [%s]%s — %s\n", p.Name, p.Group, state, p.Description)
		}
		return sb.String(), true
	case "tasks":
		ts, err := e.Tasks.List(ctx, tasks.Filter{Limit: 8})
		if err != nil {
			return "Error: " + err.Error(), true
		}
		var sb strings.Builder
		for _, t := range ts {
			fmt.Fprintf(&sb, "#%d %s → %s [%s] %s\n", t.ID, t.FromKind, t.ToAgent, t.Status, brief(t.Title, 60))
		}
		if sb.Len() == 0 {
			return "No tasks yet.", true
		}
		return sb.String(), true
	case "skills":
		if e.Skills == nil {
			return "No skills.", true
		}
		ss := e.Skills.Summaries(ctx, nil)
		if len(ss) == 0 {
			return "No skills installed.", true
		}
		var sb strings.Builder
		for _, s := range ss {
			fmt.Fprintf(&sb, "• %s — %s\n", s.Name, s.Description)
		}
		return sb.String(), true
	case "model":
		return e.modelCommand(ctx, args), true
	case "export":
		if e.Export == nil {
			return "Export is not available.", true
		}
		msgs, err := e.ChatHistory(ctx, channel, topic, 500)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		var sb strings.Builder
		sb.WriteString("# PRISM conversation\n\n")
		for _, m := range msgs {
			who := m.Agent
			if m.Role == "user" {
				who = "You"
			} else if who == "" {
				who = m.Role
			}
			fmt.Fprintf(&sb, "**%s** · %s\n\n%s\n\n", who, m.CreatedAt.Format("2006-01-02 15:04"), m.Text)
		}
		res, err := e.Export(ctx, "conversation.md", sb.String())
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return res, true
	}
	return "", false
}

func (e *Engine) statusText(ctx context.Context, key string) string {
	roles := settings.Load(ctx, e.Settings, settings.KeyModelRoles, settings.ModelRoles{})
	ref := roles.Chat
	window := e.LLM.Window(ctx, "")
	var sb strings.Builder
	fmt.Fprintf(&sb, "Chat model: %s · fast: %s · embeddings: %s\n", orDash(ref), orDash(roles.Fast), orDash(roles.Embedding))
	if atlas, err := e.Profiles.Get(ctx, "Atlas"); err == nil {
		if sess, err := e.Sessions.Chat(ctx, atlas.Name, key); err == nil {
			if ms, err := e.Sessions.Messages(ctx, sess.ID); err == nil {
				used := llm.EstimateMessages(msgsOf(ms))
				fmt.Fprintf(&sb, "Context: ~%s of %s tokens (%d%%), %d messages\n", kfmt(used), kfmt(window), used*100/max(window, 1), len(ms))
			}
		}
	}
	c := e.Tasks.Counts(ctx)
	fmt.Fprintf(&sb, "Tasks: %d queued · %d running · %d waiting for input · %d agents active\n", c.Queued, c.Running, c.Waiting, len(e.ActiveRuns()))
	st := e.Memory.Stats(ctx)
	fmt.Fprintf(&sb, "Memory: %d facts in %d banks · %d raw messages pending", st.Facts, st.Banks, st.Raw)
	return sb.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func kfmt(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprint(n)
}

func (e *Engine) modelCommand(ctx context.Context, name string) string {
	ms, err := e.LLM.Store().Models(ctx)
	if err != nil {
		return "Error: " + err.Error()
	}
	ls, _ := e.LLM.Store().Lists(ctx)
	roles := settings.Load(ctx, e.Settings, settings.KeyModelRoles, settings.ModelRoles{})
	if name == "" {
		var names []string
		for _, l := range ls {
			names = append(names, "⛓ "+l.Name)
		}
		for _, m := range ms {
			if m.Kind == "chat" {
				names = append(names, m.Name)
			}
		}
		sort.Strings(names)
		return fmt.Sprintf("Current: %s\nAvailable:\n• %s\nSwitch with /model <name>", orDash(roles.Chat), strings.Join(names, "\n• "))
	}
	ok := false
	for _, m := range ms {
		ok = ok || (m.Name == name && m.Kind == "chat")
	}
	for _, l := range ls {
		ok = ok || l.Name == name
	}
	if !ok {
		return fmt.Sprintf("Unknown model %q — see /model", name)
	}
	roles.Chat = name
	if err := e.Settings.Set(ctx, settings.KeyModelRoles, roles); err != nil {
		return "Error: " + err.Error()
	}
	e.LLM.Invalidate()
	return "Default chat model is now " + name + "."
}
