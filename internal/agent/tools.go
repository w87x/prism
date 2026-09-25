package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/tools"
)

// RegisterTools installs the agent-orchestration toolset.
func (e *Engine) RegisterTools(reg *tools.Registry) {
	reg.Register(
		e.toolDelegate(),
		e.toolAgentFind(),
		e.toolAgentRead(),
		e.toolAgentCreate(),
		e.toolAgentUpdate(),
		e.toolAskUser(),
		e.toolTaskStatus(),
		e.toolTaskSteer(),
		e.toolTaskCancel(),
		e.toolNotify(),
		e.toolToolSearch(),
		e.toolScratchRead(),
		e.toolScratchWrite(),
		e.toolEvolvePropose(),
		e.toolAskColleague(),
		e.toolAgentPerformance(),
		e.toolEvolutionAudit(),
		e.toolTaskTranscript(),
	)
}

func (e *Engine) toolDelegate() *tools.Tool {
	return &tools.Tool{
		Name: "delegate", Category: "agents", Risk: tools.RiskRead,
		Description: "Delegate work to specialist agents. Each item runs as its own task in an isolated context, in parallel; results come back together. " +
			"Give every instruction full context — the agent does not see this conversation. To answer an agent's question or continue its task, pass its task_id and your reply as the instruction.",
		Params: tools.Obj("tasks", tools.ObjList("tasks", "work items", "agent,instruction",
			tools.Str("agent", "agent name (see the specialists list or agent_find)"),
			tools.Str("instruction", "complete, self-contained instruction"),
			tools.Str("title", "short label"),
			tools.Int("task_id", "continue an earlier task that asked for input"))),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				Tasks []struct {
					Agent       string `json:"agent"`
					Instruction string `json:"instruction"`
					Title       string `json:"title"`
					TaskID      int64  `json:"task_id"`
				} `json:"tasks"`
			}](raw)
			if err != nil {
				return "", err
			}
			if len(a.Tasks) == 0 {
				return "", errors.New("no tasks given")
			}
			if len(a.Tasks) > 8 {
				return "", errors.New("at most 8 tasks per call")
			}
			if env.Depth >= MaxDepth {
				return "", fmt.Errorf("delegation depth limit (%d) reached: do this work yourself", MaxDepth)
			}
			out := make([]string, len(a.Tasks))
			tainted := make([]bool, len(a.Tasks))
			var wg sync.WaitGroup
			for i, it := range a.Tasks {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					t, tn, err := e.delegateOne(ctx, env, it.Agent, it.Instruction, it.Title, it.TaskID, false)
					tainted[i] = tn
					if err != nil {
						out[i] = fmt.Sprintf("## %s — error\n%s", it.Agent, err.Error())
						return
					}
					out[i] = formatTaskResult(t)
				}(i)
			}
			wg.Wait()
			for _, tn := range tainted {
				if tn && env.Taint != nil {
					env.Taint()
				}
			}
			return strings.Join(out, "\n\n"), nil
		},
	}
}

// toolAskColleague lets an agent ask ONE other specialist to do one thing it cannot do itself (fetch a web
// page, search the web…) and wait for the answer. Unlike delegate it never recurses: the colleague cannot
// delegate or ask anyone else, so it is allowed at any depth.
func (e *Engine) toolAskColleague() *tools.Tool {
	return &tools.Tool{
		Name: "ask_colleague", Category: "agents", Base: true, Risk: tools.RiskRead,
		Description: "Ask another specialist agent to do one thing you cannot do with your own tools (e.g. fetch a web page, search the web, read a PDF) and wait for the result. " +
			"Use this instead of improvising with shell commands such as curl. The colleague answers once and cannot pass the request on. Write a complete, self-contained request. " +
			"Name the agent if you know who (see agent_find), or leave it out and the best-matching specialist is asked.",
		Params: tools.Obj("request", tools.Str("request", "exactly what you need, with all context (URLs, queries, what to return)"),
			tools.Str("agent", "the specialist to ask (optional)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ Request, Agent string }](raw)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Request) == "" {
				return "", errors.New("say what you need")
			}
			name := strings.TrimSpace(a.Agent)
			if name == "" {
				ps, err := e.Profiles.Search(ctx, a.Request, 12)
				if err != nil {
					return "", err
				}
				for _, p := range ps {
					if p.Role == RoleWorker && p.Enabled && !strings.EqualFold(p.Name, env.Agent) {
						name = p.Name
						break
					}
				}
				if name == "" {
					return "", errors.New("no specialist matches that request; do it yourself, or say in your answer what is missing")
				}
			}
			t, tainted, err := e.delegateOne(ctx, env, name, a.Request, "help for "+env.Agent, 0, true)
			if err != nil {
				return "", err
			}
			if tainted && env.Taint != nil {
				env.Taint()
			}
			return formatTaskResult(t), nil
		},
	}
}

func formatTaskResult(t tasks.Task) string {
	switch t.Status {
	case tasks.Done:
		return fmt.Sprintf("## Task #%d → %s [done]\n%s", t.ID, t.ToAgent, strings.TrimSpace(t.Result))
	case tasks.WaitingInput:
		return fmt.Sprintf("## Task #%d → %s [needs input]\nQuestion: %s\n(Answer it yourself if you can, else use ask_user; then continue with delegate(task_id=%d, instruction=<answer>).)", t.ID, t.ToAgent, t.Question, t.ID)
	case tasks.Partial:
		return fmt.Sprintf("## Task #%d → %s [partial — %s]\n%s\n(This ran out of budget before finishing; continue with delegate(task_id=%d, instruction=<what to do next>) if it should keep going.)", t.ID, t.ToAgent, t.Error, strings.TrimSpace(t.Result), t.ID)
	default:
		out := fmt.Sprintf("## Task #%d → %s [%s]\n%s", t.ID, t.ToAgent, t.Status, t.Error)
		if t.Status == tasks.Failed && strings.TrimSpace(t.Result) != "" {
			out += "\nLast report from it:\n" + brief(strings.TrimSpace(t.Result), 1200) + "\n(Decide whether to try another agent or approach, or tell the user plainly what could not be done and why.)"
		}
		return out
	}
}

func (e *Engine) delegateOne(ctx context.Context, env *tools.Env, agentName, instruction, title string, taskID int64, leaf bool) (tasks.Task, bool, error) {
	if strings.TrimSpace(instruction) == "" {
		return tasks.Task{}, false, errors.New("empty instruction")
	}
	opts := TaskOpts{Tainted: env.Tainted, ParentRun: env.RunID, Leaf: leaf, Restricted: env.Restricted}
	if taskID != 0 { // multi-turn continuation
		t, err := e.Tasks.Get(ctx, taskID)
		if err != nil {
			return t, false, err
		}
		if t.ParentID == nil || *t.ParentID != env.TaskID {
			return t, false, errors.New("that task does not belong to you")
		}
		if t.Status != tasks.WaitingInput && t.Status != tasks.Done && t.Status != tasks.Partial {
			return t, false, fmt.Errorf("task #%d is %s and cannot be continued", t.ID, t.Status)
		}
		t, err = e.Tasks.Resume(ctx, taskID)
		if err != nil {
			return t, false, err
		}
		opts.Input = instruction
		return e.finishSub(ctx, t, opts)
	}
	p, err := e.Profiles.Get(ctx, agentName)
	if err != nil {
		return tasks.Task{}, false, e.unknownAgent(ctx, agentName, err)
	}
	if p.Role == RoleEntry {
		return tasks.Task{}, false, errors.New("cannot delegate to the entry agent")
	}
	if !p.Enabled {
		return tasks.Task{}, false, fmt.Errorf("agent %s is disabled", p.Name)
	}
	if strings.EqualFold(p.Name, env.Agent) {
		return tasks.Task{}, false, errors.New("cannot delegate to yourself")
	}
	if !leaf {
		if me, merr := e.Profiles.Get(ctx, env.Agent); merr == nil && len(me.Team) > 0 && !inTeam(me.Team, p.Name) {
			return tasks.Task{}, false, fmt.Errorf("%s is not on your team — you lead: %s. Delegate only to them (use ask_colleague for a one-off request outside the team)", p.Name, strings.Join(me.Team, ", "))
		}
	}
	if leaf && p.Role != RoleWorker {
		return tasks.Task{}, false, fmt.Errorf("%s is part of PRISM's staff, not a specialist: ask a specialist instead", p.Name)
	}
	depth := env.Depth + 1
	if leaf && depth > MaxDepth {
		depth = MaxDepth // a leaf request never deepens the tree beyond its limit
	}
	pid := env.TaskID
	var parent *int64
	var root int64
	if pid != 0 {
		parent = &pid
		if pt, err := e.Tasks.Get(ctx, pid); err == nil {
			root = pt.RootID
		}
	}
	t, err := e.Tasks.Create(ctx, tasks.Task{ParentID: parent, RootID: root, FromKind: "agent", FromName: env.Agent, ToAgent: p.Name,
		Title: title, Input: instruction, Depth: depth}, true)
	if err != nil {
		return t, false, err
	}
	return e.finishSub(ctx, t, opts)
}

func (e *Engine) finishSub(ctx context.Context, t tasks.Task, opts TaskOpts) (tasks.Task, bool, error) {
	out := e.RunTask(ctx, t, opts)
	if out.SessionID != nil && out.Status == tasks.Done { // the colleague's hand-offs, named so the parent can open them
		out.Result = strings.TrimSpace(out.Result) + e.artifactNote(ctx, *out.SessionID)
	}
	tainted := false
	if out.SessionID != nil {
		if msgs, err := e.Sessions.Messages(ctx, *out.SessionID); err == nil {
			for _, m := range msgs {
				tainted = tainted || m.Tainted
			}
		}
	}
	return out, tainted, nil
}

// artifactNote lists the artifacts an agent session saved, as a footer for its result.
func (e *Engine) artifactNote(ctx context.Context, session int64) string {
	rows, err := e.DB.Query(ctx, `SELECT id,name,expires_at FROM artifacts WHERE session_id=$1 AND (expires_at IS NULL OR expires_at>now()) ORDER BY id LIMIT 10`, session)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var id int64
		var name string
		var exp *time.Time
		if rows.Scan(&id, &name, &exp) != nil {
			continue
		}
		if sb.Len() == 0 {
			sb.WriteString("\n\nArtifacts saved by this agent (open with artifact_read):")
		}
		fmt.Fprintf(&sb, "\n- #%d %s", id, name)
		if exp != nil {
			sb.WriteString(" (temporary, expires " + exp.Format("15:04 on 2 Jan") + ")")
		}
	}
	return sb.String()
}

func (e *Engine) toolAgentFind() *tools.Tool {
	return &tools.Tool{
		Name: "agent_find", Category: "agents", Risk: tools.RiskRead,
		Description: "Search the agent catalog by purpose/traits (e.g. 'price comparison web scraping'). Returns matching agents with their groups and traits.",
		Params:      tools.Obj("query", tools.Str("query", "what the agent should be good at"), tools.Int("limit", "max results (default 5)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				Query string
				Limit int
			}](raw)
			if err != nil {
				return "", err
			}
			if a.Limit == 0 {
				a.Limit = 5
			}
			ps, err := e.Profiles.Search(ctx, a.Query, a.Limit)
			if err != nil {
				return "", err
			}
			if len(ps) == 0 {
				return "No matching agent. Consider asking Forge to create one.", nil
			}
			var sb strings.Builder
			for _, p := range ps {
				fmt.Fprintf(&sb, "- %s [%s] traits: %s — %s\n", p.Name, p.Group, strings.Join(p.Traits, ", "), p.Description)
			}
			return sb.String(), nil
		},
	}
}

func (e *Engine) toolAgentRead() *tools.Tool {
	return &tools.Tool{
		Name: "agent_read", Category: "agents", Risk: tools.RiskRead,
		Description: "Read an agent profile in full (soul, tools, skills, model, version).",
		Params:      tools.Obj("name", tools.Str("name", "agent name")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ Name string }](raw)
			if err != nil {
				return "", err
			}
			p, err := e.Profiles.Get(ctx, a.Name)
			if err != nil {
				return "", err
			}
			b, _ := json.MarshalIndent(p, "", "  ")
			return string(b), nil
		},
	}
}

type profileArgs struct {
	Name        string   `json:"name"`
	Icon        string   `json:"icon"`
	Group       string   `json:"group"`
	Description string   `json:"description"`
	Soul        string   `json:"soul"`
	Traits      []string `json:"traits"`
	Tools       []string `json:"tools"`
	Skills      []string `json:"skills"`
	Model       string   `json:"model"`
	CanDelegate *bool    `json:"can_delegate"`
	Team        []string `json:"team"`
	Enabled     *bool    `json:"enabled"`
	MaxIter     int      `json:"max_iterations"`
}

var profileProps = []tools.Prop{
	tools.Str("name", "unique short agent name"),
	tools.Str("group", "category, e.g. Web, Coding, Data Analysis"),
	tools.Str("description", "one line: what it is good for (shown in the catalog)"),
	tools.Str("soul", "the agent's system prompt (soul.md): role, method, output format, constraints"),
	tools.StrList("traits", "searchable keywords"),
	tools.StrList("tools", "toolset (tool names; keep minimal — others load on demand). MCP tools (mcp__<server>__<tool>) are allowed: find them with tool_search"),
	tools.StrList("skills", "skill names"),
	tools.Str("model", "model or model-list name (empty = default)"),
	tools.Bool("can_delegate", "may delegate subtasks (max depth 2)"),
	tools.StrList("team", "names of existing specialists this agent leads: it then delegates only to them, splitting work, waiting for results and consolidating them (implies can_delegate)"),
	tools.Bool("enabled", "enabled"),
	tools.Int("max_iterations", "tool-call budget per task (default 24, max 80): raise it (35-60) for agents that do long multi-step work such as coding, research or data processing; keep it low for quick lookups"),
}

func (e *Engine) unknownTools(names []string) []string {
	var bad []string
	for _, n := range names {
		if _, ok := e.Tools.Get(n); !ok {
			bad = append(bad, n)
		}
	}
	return bad
}

func (e *Engine) toolAgentCreate() *tools.Tool {
	return &tools.Tool{
		Name: "agent_create", Category: "agents", Risk: tools.RiskWrite,
		Description: "Create a new agent profile (hire an agent). Choose a fitting unique name, a precise soul and a minimal toolset.",
		Params:      tools.Obj("name,soul,description", profileProps...),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[profileArgs](raw)
			if err != nil {
				return "", err
			}
			if bad := e.unknownTools(a.Tools); len(bad) > 0 {
				return "", fmt.Errorf("unknown tools: %s (use tool_search to see real names)", strings.Join(bad, ", "))
			}
			if _, err := e.Profiles.Get(ctx, a.Name); err == nil {
				return "", fmt.Errorf("agent %s already exists", a.Name)
			}
			lim := settings.Load(ctx, e.Settings, settings.KeyAutonomy, settings.DefaultAutonomy()).HireLimit
			if n, _ := e.Profiles.AgentHiresSince(ctx, time.Now().AddDate(0, 0, -7)); n >= lim {
				return "", fmt.Errorf("the hiring limit is reached (%d new agents in the last 7 days; the user sets it in Autonomy). Do the work with existing agents, or tell the user what specialist is missing", lim)
			}
			p := Profile{Name: a.Name, Icon: a.Icon, Group: a.Group, Description: a.Description, Soul: a.Soul, Traits: a.Traits, Tools: a.Tools, Skills: a.Skills,
				Model: a.Model, Role: RoleWorker, AutoTools: true, Enabled: true}
			if a.CanDelegate != nil {
				p.CanDelegate = *a.CanDelegate
			}
			p.Team = a.Team
			p.MaxIterations = a.MaxIter
			saved, err := e.Profiles.Save(ctx, p, "hired by "+env.Agent)
			if err != nil {
				return "", err
			}
			// a hire made by an agent is on probation until the user confirms it: it may work, but without exec tools
			if err := e.Profiles.SetProbation(ctx, saved.ID, true); err != nil {
				return "", err
			}
			e.Emit("agent.hired", map[string]any{"id": saved.ID, "name": saved.Name, "by": env.Agent, "description": saved.Description})
			return fmt.Sprintf("Agent %s created (id %d) and put on probation: it can start working now, without shell/exec tools, until the user confirms the hire.", saved.Name, saved.ID), nil
		},
	}
}

func (e *Engine) toolAgentUpdate() *tools.Tool {
	return &tools.Tool{
		Name: "agent_update", Category: "agents", Risk: tools.RiskWrite,
		Description: "Update fields of an existing agent (only the fields you pass change). Souls of well-known agents cannot be changed here; use evolve_propose.",
		Params:      tools.Obj("name", profileProps...),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[profileArgs](raw)
			if err != nil {
				return "", err
			}
			p, err := e.Profiles.Get(ctx, a.Name)
			if err != nil {
				return "", err
			}
			if a.Soul != "" {
				if p.System {
					return "", errors.New("well-known agents' souls change only through evolve_propose")
				}
				p.Soul = a.Soul
			}
			if a.Group != "" {
				p.Group = a.Group
			}
			if a.Description != "" {
				p.Description = a.Description
			}
			if a.Traits != nil {
				p.Traits = a.Traits
			}
			if a.Tools != nil {
				if bad := e.unknownTools(a.Tools); len(bad) > 0 {
					return "", fmt.Errorf("unknown tools: %s", strings.Join(bad, ", "))
				}
				p.Tools = a.Tools
			}
			if a.Skills != nil {
				p.Skills = a.Skills
			}
			if a.Model != "" {
				p.Model = a.Model
			}
			if a.CanDelegate != nil {
				p.CanDelegate = *a.CanDelegate
			}
			if a.Team != nil {
				p.Team = a.Team
			}
			if a.MaxIter > 0 {
				p.MaxIterations = a.MaxIter
			}
			if a.Enabled != nil && !p.System {
				p.Enabled = *a.Enabled
			}
			if _, err := e.Profiles.Save(ctx, *p, "updated by "+env.Agent); err != nil {
				return "", err
			}
			return "Agent " + p.Name + " updated.", nil
		},
	}
}

func (e *Engine) toolAskUser() *tools.Tool {
	return &tools.Tool{
		Name: "ask_user", Category: "agents", Base: true, Risk: tools.RiskRead,
		Description: "Ask the user a clarifying question when you are genuinely blocked (never for things you can decide or look up). Optionally offer choices. Blocks until answered.",
		Params:      tools.Obj("question", tools.Str("question", "precise question"), tools.StrList("options", "optional short choices")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				Question string   `json:"question"`
				Options  []string `json:"options"`
			}](raw)
			if err != nil {
				return "", err
			}
			ans, err := env.Ask(ctx, tools.Question{Kind: "clarify", Text: a.Question, Options: a.Options})
			if err != nil {
				return "", err
			}
			return "User answered: " + ans, nil
		},
	}
}

func (e *Engine) toolTaskStatus() *tools.Tool {
	return &tools.Tool{
		Name: "task_status", Category: "agents", Risk: tools.RiskRead,
		Description: "Look up a task by id: status, result or error.",
		Params:      tools.Obj("id", tools.Int("id", "task id")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ ID int64 }](raw)
			if err != nil {
				return "", err
			}
			t, err := e.Tasks.Get(ctx, a.ID)
			if err != nil {
				return "", err
			}
			return formatTaskResult(t), nil
		},
	}
}

// toolTaskTranscript reads back the full step-by-step record of a finished task — every tool call, its
// result, and the reasoning in between — not just the final answer task_status gives. Restricted to
// Daedalus: it is meaningfully more revealing than task_status (raw tool arguments, intermediate content),
// and its only current job is distilling a finished task into a reusable skill.
func (e *Engine) toolTaskTranscript() *tools.Tool {
	return &tools.Tool{
		Name: "task_transcript", Category: "agents", Risk: tools.RiskRead, Only: []string{"Daedalus"},
		Description: "Full step-by-step transcript of a task: every message, tool call and result, not just the final answer. Use this to see exactly how a finished task was actually done.",
		Params:      tools.Obj("id", tools.Int("id", "task id")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ ID int64 }](raw)
			if err != nil {
				return "", err
			}
			_, text, _, tainted, err := e.transcript(ctx, a.ID)
			if err != nil {
				return "", err
			}
			if tainted && env.Taint != nil { // untrusted content shaped how this task went; a skill built from it inherits that
				env.Taint()
			}
			return text, nil
		},
	}
}

var notifyMu sync.Mutex
var notifyLog = map[string][]time.Time{}

func (e *Engine) toolNotify() *tools.Tool {
	return &tools.Tool{
		Name: "notify_user", Category: "agents", Risk: tools.RiskWrite, Auto: true,
		Description: "Proactively send the user a message outside the normal reply flow (result of a background job, something they asked to be told about). " +
			"Optionally route it to a named Telegram topic so the main chat stays clean; if the user replies there, that topic's context is used. Rate-limited.",
		Params: tools.Obj("text",
			tools.Str("text", "the message"),
			tools.Str("topic", "optional Telegram topic name to route to (created on demand)"),
			tools.Enum("level", "importance", "info", "attention", "warning", "error")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ Text, Topic, Level string }](raw)
			if err != nil {
				return "", err
			}
			notifyMu.Lock()
			now := time.Now()
			var keep []time.Time
			for _, t := range notifyLog[env.Agent] {
				if now.Sub(t) < time.Hour {
					keep = append(keep, t)
				}
			}
			if len(keep) >= 12 {
				notifyMu.Unlock()
				return "", errors.New("notification rate limit reached (12/hour); batch your messages")
			}
			notifyLog[env.Agent] = append(keep, now)
			notifyMu.Unlock()
			if a.Level == "" {
				a.Level = "info"
			}
			e.Notify(ctx, Notice{Agent: env.Agent, Text: a.Text, Level: a.Level, Topic: a.Topic})
			return "Delivered.", nil
		},
	}
}

func (e *Engine) toolToolSearch() *tools.Tool {
	return &tools.Tool{
		Name: "tool_search", Category: "agents", Base: true, Risk: tools.RiskRead,
		Description: "Find and load tools by capability (e.g. 'download file', 'obsidian note', 'telegram'). Matching tools become callable immediately; their parameters are returned.",
		Params:      tools.Obj("query", tools.Str("query", "capability keywords"), tools.Int("limit", "max tools to load (default 5)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				Query string
				Limit int
			}](raw)
			if err != nil {
				return "", err
			}
			if a.Limit <= 0 || a.Limit > 10 {
				a.Limit = 5
			}
			var found []*tools.Tool
			for _, t := range e.Tools.Search(a.Query, a.Limit*3) { // tools reserved for other agents are not offered
				if t.AllowedFor(env.Agent) && len(found) < a.Limit {
					found = append(found, t)
				}
			}
			if len(found) == 0 {
				return "No matching tools.", nil
			}
			var sb strings.Builder
			var names []string
			for _, t := range found {
				sb.WriteString(tools.Describe(t) + "\n")
				names = append(names, t.Name)
			}
			if env.Activate != nil {
				env.Activate(names...)
			}
			return "Loaded: " + strings.Join(names, ", ") + "\n" + sb.String(), nil
		},
	}
}

const scratchCap = 4000

func (e *Engine) toolScratchRead() *tools.Tool {
	return &tools.Tool{
		Name: "scratchpad_read", Category: "core", Base: true, Risk: tools.RiskRead,
		Description: "Read your private scratchpad (persists across turns of this session).",
		Params:      tools.Obj(""),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			t := e.Sessions.Scratch(ctx, env.SessionID)
			if t == "" {
				return "(empty)", nil
			}
			return t, nil
		},
	}
}

func (e *Engine) toolScratchWrite() *tools.Tool {
	return &tools.Tool{
		Name: "scratchpad_write", Category: "core", Base: true, Risk: tools.RiskWrite, Auto: true,
		Description: "Write notes to your private scratchpad (plans, intermediate results). It is shown to you on every turn, so keep it short (≤4000 chars).",
		Params: tools.Obj("text", tools.Str("text", "note text"),
			tools.Enum("mode", "replace (default) or append", "replace", "append")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ Text, Mode string }](raw)
			if err != nil {
				return "", err
			}
			text := a.Text
			if a.Mode == "append" {
				text = strings.TrimSpace(e.Sessions.Scratch(ctx, env.SessionID) + "\n" + a.Text)
			}
			if r := []rune(text); len(r) > scratchCap {
				text = string(r[len(r)-scratchCap:])
			}
			return "saved", e.Sessions.SetScratch(ctx, env.SessionID, text)
		},
	}
}

func (e *Engine) toolEvolvePropose() *tools.Tool {
	return &tools.Tool{
		Name: "evolve_propose", Category: "evolution", Risk: tools.RiskWrite, Auto: true,
		Description: "Propose a change to an agent for the user's review (never applied silently unless auto-evolve is on). kind=soul (default): pass the COMPLETE revised soul. kind=tools: pass the complete new toolset in `tools`. kind=traits: pass the complete new keyword list in `traits`. Always give a rationale grounded in observed facts.",
		Params: tools.Obj("agent,rationale",
			tools.Str("agent", "agent name"),
			tools.Enum("kind", "what to change (default soul)", "soul", "tools", "traits"),
			tools.Str("soul", "complete revised soul (kind=soul)"),
			tools.StrList("tools", "complete new toolset (kind=tools)"),
			tools.StrList("traits", "complete new trait list (kind=traits)"),
			tools.Str("rationale", "why, citing facts/performance")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				Agent, Kind, Soul, Rationale string
				Tools, Traits                []string
			}](raw)
			if err != nil {
				return "", err
			}
			p, err := e.Profiles.Get(ctx, a.Agent)
			if err != nil {
				return "", err
			}
			kind := firstNonEmpty(a.Kind, KindSoul)
			var body string
			switch kind {
			case KindSoul:
				body = strings.TrimSpace(a.Soul)
				if body == "" || body == strings.TrimSpace(p.Soul) {
					return "", errors.New("proposal must be a non-empty, changed soul")
				}
			case KindTools:
				if bad := e.unknownTools(a.Tools); len(bad) > 0 {
					return "", fmt.Errorf("unknown tools: %s (use tool_search to see real names)", strings.Join(bad, ", "))
				}
				want := norm(a.Tools)
				if sameSet(want, p.Tools) {
					return "", errors.New("proposal must change the toolset")
				}
				body = strings.Join(want, "\n")
			case KindTraits:
				want := norm(a.Traits)
				if len(want) == 0 || sameSet(want, p.Traits) {
					return "", errors.New("proposal must be a non-empty, changed trait list")
				}
				body = strings.Join(want, "\n")
			default:
				return "", fmt.Errorf("unknown kind %q (soul, tools or traits)", kind)
			}
			id, err := e.Profiles.AddProposal(ctx, p.ID, kind, body, a.Rationale)
			if err != nil {
				return "", err
			}
			ev := map[string]any{"id": id, "agent": p.Name, "kind": kind, "rationale": brief(a.Rationale, 300)}
			var auto struct {
				AutoEvolve bool `json:"auto_evolve"`
			}
			_, _ = e.Settings.Get(ctx, "autonomy", &auto)
			if auto.AutoEvolve {
				if err := e.Profiles.Decide(ctx, id, true); err != nil {
					return "", err
				}
				ev["applied"] = true
				e.Emit("evolution.proposal", ev)
				return fmt.Sprintf("Proposal #%d (%s) applied automatically (auto-evolve on); the previous soul is kept in history.", id, kind), nil
			}
			e.Emit("evolution.proposal", ev)
			return fmt.Sprintf("Proposal #%d (%s) recorded and awaiting the user's review.", id, kind), nil
		},
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range b {
		m[x] = true
	}
	for _, x := range a {
		if !m[x] {
			return false
		}
	}
	return true
}

func (e *Engine) toolAgentPerformance() *tools.Tool {
	return &tools.Tool{
		Name: "agent_performance", Category: "evolution", Risk: tools.RiskRead,
		Description: "Task statistics and recent failures for an agent over the last 30 days (input for evolution decisions).",
		Params:      tools.Obj("agent", tools.Str("agent", "agent name")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ Agent string }](raw)
			if err != nil {
				return "", err
			}
			var total, done, failed, waiting int
			var avgIn, avgOut float64
			_ = e.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status='done'), count(*) FILTER (WHERE status='failed'),
				count(*) FILTER (WHERE status='waiting_input'), COALESCE(avg(tokens_in),0), COALESCE(avg(tokens_out),0)
				FROM tasks WHERE lower(to_agent)=lower($1) AND created_at > now()-interval '30 days'`, a.Agent).Scan(&total, &done, &failed, &waiting, &avgIn, &avgOut)
			var sb strings.Builder
			fmt.Fprintf(&sb, "%s — last 30 days: %d tasks, %d done, %d failed, %d asked for input; avg tokens in/out %.0f/%.0f\n", a.Agent, total, done, failed, waiting, avgIn, avgOut)
			rows, err := e.DB.Query(ctx, `SELECT id, left(input,120), left(error,200) FROM tasks WHERE lower(to_agent)=lower($1) AND status='failed' ORDER BY id DESC LIMIT 5`, a.Agent)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var id int64
					var in, er string
					_ = rows.Scan(&id, &in, &er)
					fmt.Fprintf(&sb, "failed #%d: %s → %s\n", id, in, er)
				}
			}
			return sb.String(), nil
		},
	}
}

// unknownAgent turns a bare "agent not found" into something the model can act on: it tends to pass a role
// description ("web specialist") instead of a name, so list the names that do exist.
func (e *Engine) unknownAgent(ctx context.Context, name string, err error) error {
	if !strings.Contains(err.Error(), "not found") {
		return err
	}
	ps, lerr := e.Profiles.List(ctx)
	if lerr != nil {
		return err
	}
	var names []string
	for _, p := range ps {
		if p.Enabled && p.Role != RoleEntry {
			names = append(names, p.Name+" ("+p.Description+")")
		}
	}
	return fmt.Errorf("agent %q not found — use an exact agent name, not a role description. Available: %s", name, strings.Join(names, "; "))
}

func inTeam(team []string, name string) bool {
	for _, m := range team {
		if strings.EqualFold(m, name) {
			return true
		}
	}
	return false
}

// ownedRunningTask loads a task the caller delegated (directly or through its own delegates) that is still going.
func (e *Engine) ownedRunningTask(ctx context.Context, env *tools.Env, id int64) (tasks.Task, error) {
	t, err := e.Tasks.Get(ctx, id)
	if err != nil {
		return t, err
	}
	owned := false
	for cur, hops := t, 0; env.TaskID != 0 && cur.ParentID != nil && hops < 5; hops++ { // a task below one of the caller's own delegates counts too
		if *cur.ParentID == env.TaskID {
			owned = true
			break
		}
		parent, perr := e.Tasks.Get(ctx, *cur.ParentID)
		if perr != nil {
			break
		}
		cur = parent
	}
	if !owned {
		return t, errors.New("that task is not one you delegated")
	}
	if t.Status != tasks.Running && t.Status != tasks.Queued {
		return t, fmt.Errorf("task #%d is already %s", t.ID, t.Status)
	}
	return t, nil
}

func (e *Engine) toolTaskSteer() *tools.Tool {
	return &tools.Tool{
		Name: "task_steer", Category: "agents", Base: true, Risk: tools.RiskWrite, Auto: true,
		Description: "Redirect a specialist you delegated to while it is still working: your message reaches it at its next step and is read as a refinement or replacement of its instruction. Use it when new information (for example something the user just said) changes what it should do. Use task_cancel to stop it instead.",
		Params:      tools.Obj("id,message", tools.Int("id", "the delegated task id"), tools.Str("message", "what it should do differently — complete and self-contained")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct {
				ID      int64  `json:"id"`
				Message string `json:"message"`
			}](raw)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Message) == "" {
				return "", errors.New("say what it should do differently")
			}
			t, err := e.ownedRunningTask(ctx, env, a.ID)
			if err != nil {
				return "", err
			}
			e.mu.Lock()
			ch := e.taskSteer[t.ID]
			e.mu.Unlock()
			if ch == nil {
				return "", fmt.Errorf("task #%d (%s) is queued or cannot be steered right now; cancel it and delegate again with the new instruction", t.ID, t.ToAgent)
			}
			select {
			case ch <- strings.TrimSpace(a.Message):
				return fmt.Sprintf("Delivered to %s (task #%d): it will read it at its next step. Check on it later with task_status(%d).", t.ToAgent, t.ID, t.ID), nil
			default:
				return "", errors.New("it already has several unread messages; wait a moment")
			}
		},
	}
}

func (e *Engine) toolTaskCancel() *tools.Tool {
	return &tools.Tool{
		Name: "task_cancel", Category: "agents", Base: true, Risk: tools.RiskWrite, Auto: true,
		Description: "Stop a specialist you delegated to that is still working (its partial results are kept). Use it when the user's new message makes its work pointless; redirect it with task_steer instead when it should continue differently.",
		Params:      tools.Obj("id", tools.Int("id", "the delegated task id")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ ID int64 }](raw)
			if err != nil {
				return "", err
			}
			t, err := e.ownedRunningTask(ctx, env, a.ID)
			if err != nil {
				return "", err
			}
			if err := e.CancelTask(ctx, t.ID); err != nil {
				return "", err
			}
			return fmt.Sprintf("Cancelled task #%d (%s). What it had produced so far is kept.", t.ID, t.ToAgent), nil
		},
	}
}

// toolEvolutionAudit lists recent evolution proposals with what happened to the agent's tasks before and after
// each applied change, so the evolvers themselves can be checked.
func (e *Engine) toolEvolutionAudit() *tools.Tool {
	return &tools.Tool{
		Name: "evolution_audit", Category: "evolution", Risk: tools.RiskRead, Deferred: true,
		Description: "Audit of evolution proposals from the last N days (default 7): who they were for, kind, status, rationale, and for applied ones the agent's task success rate in the 14 days before vs after. Use it to judge whether evolutions helped.",
		Params:      tools.Obj("", tools.Int("days", "look back this many days (default 7)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, _ := tools.Decode[struct{ Days int }](raw)
			if a.Days <= 0 {
				a.Days = 7
			}
			rows, err := e.DB.Query(ctx, `SELECT e.id, p.name, e.kind, e.status, left(e.rationale,240), e.created_at, e.decided_at,
				(SELECT count(*) FILTER (WHERE t.status='done') FROM tasks t WHERE lower(t.to_agent)=lower(p.name) AND t.created_at BETWEEN e.decided_at-interval '14 days' AND e.decided_at),
				(SELECT count(*) FROM tasks t WHERE lower(t.to_agent)=lower(p.name) AND t.status IN ('done','failed','partial') AND t.created_at BETWEEN e.decided_at-interval '14 days' AND e.decided_at),
				(SELECT count(*) FILTER (WHERE t.status='done') FROM tasks t WHERE lower(t.to_agent)=lower(p.name) AND t.created_at > e.decided_at),
				(SELECT count(*) FROM tasks t WHERE lower(t.to_agent)=lower(p.name) AND t.status IN ('done','failed','partial') AND t.created_at > e.decided_at)
				FROM evolution_proposals e JOIN agent_profiles p ON p.id=e.profile_id
				WHERE e.created_at > now()-make_interval(days=>$1) ORDER BY e.id`, a.Days)
			if err != nil {
				return "", err
			}
			defer rows.Close()
			var sb strings.Builder
			for rows.Next() {
				var id int64
				var agent, kind, status, why string
				var created time.Time
				var decided *time.Time
				var bd, bt, ad, at int
				if err := rows.Scan(&id, &agent, &kind, &status, &why, &created, &decided, &bd, &bt, &ad, &at); err != nil {
					return "", err
				}
				fmt.Fprintf(&sb, "#%d %s/%s [%s] %s — %s", id, agent, kind, status, created.Format("2006-01-02"), why)
				if status == "applied" && decided != nil {
					fmt.Fprintf(&sb, "\n   tasks done before: %d/%d, after: %d/%d", bd, bt, ad, at)
				}
				sb.WriteString("\n")
			}
			if sb.Len() == 0 {
				return "No evolution proposals in that period.", nil
			}
			return sb.String(), nil
		},
	}
}
