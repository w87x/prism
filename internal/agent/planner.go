package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"prism/internal/memory"
	"prism/internal/settings"
	"prism/internal/tools"
)

// The front door. A message to Atlas first passes through a quick structured pass (fast model): what kind of message is it,
// which project does it belong to, what real work does it contain (a short nested task list, with the memory each task
// needs), and what must the user decide before any work starts. PRISM then does the mechanical parts itself — looks up the
// memory, takes the message to the project's own chat, asks the open questions — and hands Atlas the prepared plan, so a
// weaker model spends its effort on the work, not on planning it.

// PlanTask is one step of the decomposition; Mem are keywords to look up in memory for it.
type PlanTask struct {
	Title string     `json:"title"`
	Mem   []string   `json:"mem,omitempty"`
	Sub   []PlanTask `json:"subtasks,omitempty"`
}

// PlanAsk is something only the user can decide.
type PlanAsk struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// Plan is the structured reading of one message.
type Plan struct {
	Class      string     `json:"class"`      // chat | question | task
	Project    string     `json:"project"`    // an existing project the message is about, "" when none
	Confidence string     `json:"confidence"` // high | low — how sure the project is
	Tasks      []PlanTask `json:"tasks"`
	Asks       []PlanAsk  `json:"asks"`
}

const planPrompt = `You are the front door of a personal assistant. You get a NEW message from the user together with their projects, the chat it arrived in and the last few messages of that chat. Classify and prepare it.
Answer JSON only: {"class":"chat|question|task","project":"","confidence":"high|low","tasks":[{"title":"","mem":[""],"subtasks":[]}],"asks":[{"question":"","options":[""]}]}
- class: "chat" = small talk, thanks, an acknowledgement; "question" = can be answered directly or from what is known; "task" = needs work (tools, research, downloads, changes, scheduling).
- project: the exact name of ONE project from the list that this message is about, or "" when it is not about a project. A message that merely continues the recent conversation belongs where that conversation is (the chat's own project, or ""). confidence "low" when it could belong to the project but could equally be general or about another one.
- tasks: only for class "task": 1-6 concrete steps, each one action starting with a verb, nested as subtasks where that is natural. For a step that depends on known facts (a server, a path, a preference, a person) put 1-5 search keywords in "mem"; leave it empty otherwise.
- asks: ONLY what you truly cannot decide or look up and a wrong guess would waste work (which of several options the user must choose, a missing detail no memory can supply). Each with 2-4 likely options. At most 3. None when a sensible default exists.`

type planProject struct {
	ID          int64
	Name        string
	Description string
}

// plannerOn reports whether the front door is enabled (settings.General: planner_off).
func (e *Engine) plannerOn(ctx context.Context) bool {
	return !settings.Load(ctx, e.Settings, settings.KeyGeneral, settings.General{}).PlannerOff && e.LLM != nil && e.LLM.RoleRef(ctx, "fast") != ""
}

func (e *Engine) routingOn(ctx context.Context) bool {
	return !settings.Load(ctx, e.Settings, settings.KeyGeneral, settings.General{}).RoutingOff
}

func (e *Engine) planProjects(ctx context.Context) []planProject {
	if e.Memory == nil {
		return nil
	}
	bs, err := e.Memory.Banks(ctx)
	if err != nil {
		return nil
	}
	var out []planProject
	for _, b := range bs {
		if b.Kind == memory.KindProject && b.Status == "active" {
			out = append(out, planProject{b.ID, b.Name, b.Description})
		}
	}
	return out
}

// MakePlan reads one message. It returns nil when the pass is off, the message is not worth one, or the model fails — the message
// then simply goes to Atlas as it is.
func (e *Engine) MakePlan(ctx context.Context, text, topic string, projects []planProject) *Plan {
	text = strings.TrimSpace(text)
	if len([]rune(text)) < 14 || strings.HasPrefix(text, "/") || !e.plannerOn(ctx) {
		return nil
	}
	var sb strings.Builder
	sb.WriteString("PROJECTS:\n")
	for _, p := range projects {
		fmt.Fprintf(&sb, "- %s", p.Name)
		if d := strings.TrimSpace(p.Description); d != "" {
			fmt.Fprintf(&sb, " — %s", brief(d, 140))
		}
		sb.WriteString("\n")
	}
	if len(projects) == 0 {
		sb.WriteString("(none)\n")
	}
	cur := "none (the main chat)"
	if c, err := e.GetChat(ctx, topic); err == nil && c.ProjectBankID != nil && c.Project != "" {
		cur = c.Project
	}
	fmt.Fprintf(&sb, "\nTHE CHAT'S OWN PROJECT: %s\nRECENT MESSAGES (oldest first):\n", cur)
	if hist, err := e.ChatHistory(ctx, "web", topic, 6); err == nil {
		for _, h := range hist {
			if h.Role == "user" || h.Role == "agent" {
				fmt.Fprintf(&sb, "%s: %s\n", h.Role, brief(h.Text, 200))
			}
		}
	}
	fmt.Fprintf(&sb, "\nNEW MESSAGE:\n%s", brief(text, 2000))
	var p Plan
	pctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := e.LLM.CompleteJSON(pctx, e.LLM.ActionRef(pctx, "plan", "fast"), planPrompt, sb.String(), &p); err != nil {
		return nil
	}
	p.Class = strings.ToLower(strings.TrimSpace(p.Class))
	if p.Class != "chat" && p.Class != "question" && p.Class != "task" {
		p.Class = "task"
	}
	p.Confidence = strings.ToLower(strings.TrimSpace(p.Confidence))
	if p.Class != "task" {
		p.Tasks = nil
	}
	if len(p.Tasks) > 6 {
		p.Tasks = p.Tasks[:6]
	}
	if len(p.Asks) > 3 {
		p.Asks = p.Asks[:3]
	}
	return &p
}

func (p Plan) taskLines(sb *strings.Builder, ts []PlanTask, prefix string) {
	for i, t := range ts {
		n := fmt.Sprintf("%s%d", prefix, i+1)
		fmt.Fprintf(sb, "%s. %s\n", n, t.Title)
		p.taskLines(sb, t.Sub, n+".")
	}
}

func memKeywords(ts []PlanTask) [][]string {
	var out [][]string
	for _, t := range ts {
		if len(t.Mem) > 0 {
			out = append(out, t.Mem)
		}
		out = append(out, memKeywords(t.Sub)...)
	}
	return out
}

// planFacts looks up, in the user's bank and the project's, the memory the plan said its steps need.
func (e *Engine) planFacts(ctx context.Context, p *Plan, projectLabel string) []string {
	if e.Memory == nil || p == nil {
		return nil
	}
	banks := []string{"user"}
	if projectLabel != "" {
		banks = append(banks, projectLabel)
	}
	seen := map[int64]bool{}
	var out []string
	for _, kws := range memKeywords(p.Tasks) {
		fs, err := e.Memory.Find(ctx, memory.FindReq{Query: strings.Join(kws, " "), Banks: banks, K: 3, NoReinforce: true, NoLinks: true})
		if err != nil {
			continue
		}
		for _, f := range fs {
			if !seen[f.ID] && len(out) < 12 {
				seen[f.ID] = true
				out = append(out, fmt.Sprintf("%s (%s)", f.Text, f.Bank))
			}
		}
	}
	return out
}

// planNote is what Atlas is handed: the plan, the facts already looked up and the user's answers to the planner's questions.
func planNote(p *Plan, facts []string, answers string) string {
	if p == nil || p.Class == "chat" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n[Plan prepared for you before you started — use it; do not plan again from scratch]\nClass: " + p.Class + "\n")
	if len(p.Tasks) > 0 {
		sb.WriteString("Steps:\n")
		p.taskLines(&sb, p.Tasks, "")
	}
	if len(facts) > 0 {
		sb.WriteString("Facts from memory that apply:\n")
		for _, f := range facts {
			sb.WriteString("- " + f + "\n")
		}
	}
	if strings.TrimSpace(answers) != "" {
		sb.WriteString("The user was asked before you started, and answered:\n" + answers + "\n")
	}
	if len(p.Tasks) == 0 && len(facts) == 0 && strings.TrimSpace(answers) == "" {
		return ""
	}
	return sb.String()
}

// projectChat finds (or creates) the chat that belongs to a project.
func (e *Engine) projectChat(ctx context.Context, pr planProject) (Chat, error) {
	var topic string
	err := e.DB.QueryRow(ctx, `SELECT topic FROM chats WHERE project_bank_id=$1 AND NOT archived ORDER BY last_at DESC LIMIT 1`, pr.ID).Scan(&topic)
	if err == nil && topic != "" {
		return e.GetChat(ctx, topic)
	}
	return e.CreateChat(ctx, pr.Name, pr.ID)
}

// frontDoorResult is what the front door decided for one message.
type frontDoorResult struct {
	Topic string // the chat the message is handled in (changed when it was routed to a project chat)
	Note  string // the plan note to append to Atlas's input
}

// frontDoor runs the structured pass for a message that arrived in chat topic: it may take the message to a project's chat (the
// message row is moved there and the chat's run registration with it), ask the user what only they can decide, and returns the plan
// note for Atlas. Everything is best effort: on any trouble the message is handled where it is, unplanned.
func (e *Engine) frontDoor(ctx context.Context, m UserMsg, msgID int64, ar *activeRun) frontDoorResult {
	res := frontDoorResult{Topic: m.Topic}
	if m.Channel != "web" {
		return res
	}
	projects := e.planProjects(ctx)
	plan := e.MakePlan(ctx, m.Text, m.Topic, projects)
	if plan == nil {
		return res
	}
	label := ""
	// ── routing ──
	if e.routingOn(ctx) && plan.Project != "" {
		var target *planProject
		for i := range projects {
			if strings.EqualFold(projects[i].Name, strings.TrimSpace(plan.Project)) {
				target = &projects[i]
			}
		}
		cur, _ := e.GetChat(ctx, m.Topic)
		if target != nil && (cur.ProjectBankID == nil || *cur.ProjectBankID != target.ID) {
			go_ := plan.Confidence != "low"
			if !go_ {
				q := tools.Question{Kind: "clarify", Text: fmt.Sprintf("This looks like it belongs to the project “%s”.", target.Name), Items: []tools.QItem{{
					Header: "Project", Text: fmt.Sprintf("This looks like it belongs to the project “%s”. Where should it go?", target.Name), Kind: "single",
					Options: []string{fmt.Sprintf("Open the “%s” chat", target.Name), "Stay in this chat"}}}}
				if ans, err := e.ask(ctx, ar.Info, q); err == nil && strings.HasPrefix(strings.ToLower(ans), "open") {
					go_ = true
				}
			}
			if go_ {
				if dest, err := e.projectChat(ctx, *target); err == nil && dest.Topic != m.Topic && !e.ChatBusy(ChatKey("web", dest.Topic)) {
					if e.moveToChat(ctx, msgID, m.Topic, dest.Topic, ar) {
						res.Topic = dest.Topic
						label = dest.Project
						e.Emit("chat.routed", map[string]any{"from": m.Topic, "to": dest.Topic, "id": msgID, "project": target.Name})
					}
				}
			}
		}
	}
	if label == "" {
		if c, err := e.GetChat(ctx, res.Topic); err == nil {
			label = c.Project
		}
	}
	// ── what only the user can decide, asked before any work starts ──
	answers := ""
	if len(plan.Asks) > 0 {
		var items []tools.QItem
		for _, a := range plan.Asks {
			if strings.TrimSpace(a.Question) == "" {
				continue
			}
			kind := "single"
			if len(a.Options) == 0 {
				kind = "text"
			}
			items = append(items, tools.QItem{Text: strings.TrimSpace(a.Question), Kind: kind, Options: tools.QOptions(a.Options)})
		}
		if len(items) > 0 {
			q := tools.Question{Kind: "clarify", Items: items, Text: items[0].Text}
			if len(items) > 1 {
				var sb strings.Builder
				for i, it := range items {
					fmt.Fprintf(&sb, "%d. %s\n", i+1, it.Text)
				}
				q.Text = strings.TrimSpace(sb.String())
			}
			if ans, err := e.ask(ctx, ar.Info, q); err == nil {
				answers = ans
			}
		}
	}
	res.Note = planNote(plan, e.planFacts(ctx, plan, label), answers)
	return res
}

// moveToChat takes the just-logged user message from one chat to another and moves the in-flight run's registration with it.
func (e *Engine) moveToChat(ctx context.Context, msgID int64, from, to string, ar *activeRun) bool {
	tag, err := e.DB.Exec(ctx, `UPDATE chat_messages SET topic=$2 WHERE id=$1 AND channel='web' AND topic=$3`, msgID, to, from)
	if err != nil || tag.RowsAffected() == 0 {
		return false
	}
	oldKey, newKey := ChatKey("web", from), ChatKey("web", to)
	e.mu.Lock()
	if e.chatRun[oldKey] == ar {
		delete(e.chatRun, oldKey)
	}
	e.chatRun[newKey] = ar
	e.mu.Unlock()
	e.touchChat(ctx, "web", to)
	return true
}
