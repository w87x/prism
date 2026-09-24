// Package tools defines the tool abstraction shared by built-ins, MCP servers and
// integrations, plus the registry that tracks on/off and safe/armed state.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Risk classifies side effects. Read-only tools may fan out concurrently;
// everything else is serialized and (unless armed) needs user confirmation.
type Risk int

const (
	RiskRead  Risk = iota // no side effects
	RiskWrite             // changes state we own (memory, files in data dir, notes)
	RiskExec              // runs code / reaches the outside world with side effects
)

func (r Risk) String() string { return [...]string{"read", "write", "exec"}[r] }

func (r Risk) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

type Tool struct {
	Name        string
	Description string
	Params      json.RawMessage
	Category    string
	Risk        Risk
	Base        bool   // part of every agent's base toolset
	Auto        bool   // low-risk write: armed by default
	Untrusted   bool   // output originates outside the trust boundary (web, mcp, mail…) → taints the turn
	Deferred    bool   // schema is only revealed by tool_search (typically MCP)
	Source      string // "builtin" or "mcp:<server>"
	// Only, when set, names the agents that may use the tool (memory maintenance belongs to Mnemosyne). Other agents
	// never see it, cannot load it with tool_search and are refused if they call it anyway.
	Only []string
	// Concurrent overrides the default (Risk==RiskRead ⇒ concurrent-safe).
	Concurrent *bool
	// Timeout bounds one call (0 → the runner's default of 9 minutes).
	Timeout time.Duration
	Run     func(ctx context.Context, env *Env, args json.RawMessage) (string, error)
}

// AllowedFor reports whether the named agent may use the tool.
func (t *Tool) AllowedFor(agent string) bool {
	if len(t.Only) == 0 {
		return true
	}
	for _, n := range t.Only {
		if strings.EqualFold(n, agent) {
			return true
		}
	}
	return false
}

// ConcurrentSafe reports whether calls may be fanned out in parallel.
func (t *Tool) ConcurrentSafe() bool {
	if t.Concurrent != nil {
		return *t.Concurrent
	}
	return t.Risk == RiskRead
}

// Question is what a tool (or the runner) asks the user.
type Question struct {
	Kind    string   `json:"kind"` // clarify | confirm
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
	Tool    string   `json:"tool,omitempty"`
	Args    string   `json:"args,omitempty"`
}

// Env carries per-call context. Tools treat it as read-only except via callbacks.
type Env struct {
	Agent     string
	Profile   any // *agent.Profile, opaque here
	SessionID int64
	TaskID    int64
	Depth     int
	Tainted   bool
	Channel   string
	Topic     string
	Emit      func(kind string, data any)
	// Ask blocks until the user answers (top-level agents) or returns ErrNeedsInput (sub-agents).
	Ask func(ctx context.Context, q Question) (string, error)
	// Activate adds tools to the running session (used by tool_search).
	Activate func(names ...string)
	// Taint marks this call's output as untrusted (e.g. a delegate result that
	// came from a tainted sub-agent).
	Taint func()
	// ParentRun links events of sub-runs to the calling run.
	RunID int64
	// Restricted runs (an agent on probation, and everyone it asks for help) get no exec tools.
	Restricted bool
	// Sources are the web sites seen in this run's untrusted output (see memory_store's source_url).
	Sources *Sources
}

// ErrNeedsInput is returned by Env.Ask in sub-agents: the run should end and the
// question bubbles up to the delegating agent.
type NeedsInput struct{ Question string }

func (e *NeedsInput) Error() string { return "needs input: " + e.Question }

// ── argument helpers ────────────────────────────────────────────────────────

// Decode unmarshals tool arguments into T, tolerating empty input.
func Decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(strings.TrimSpace(string(raw))) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("invalid arguments: %w", err)
	}
	return v, nil
}

// ── schema builders ─────────────────────────────────────────────────────────

type Prop struct {
	Name string
	Def  map[string]any
}

func Str(name, desc string) Prop {
	return Prop{name, map[string]any{"type": "string", "description": desc}}
}
func Int(name, desc string) Prop {
	return Prop{name, map[string]any{"type": "integer", "description": desc}}
}
func Num(name, desc string) Prop {
	return Prop{name, map[string]any{"type": "number", "description": desc}}
}
func Bool(name, desc string) Prop {
	return Prop{name, map[string]any{"type": "boolean", "description": desc}}
}
func Enum(name, desc string, vals ...string) Prop {
	return Prop{name, map[string]any{"type": "string", "description": desc, "enum": vals}}
}
func StrList(name, desc string) Prop {
	return Prop{name, map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "string"}}}
}
func IntList(name, desc string) Prop {
	return Prop{name, map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "integer"}}}
}
func Any(name, desc string) Prop { return Prop{name, map[string]any{"description": desc}} }

// ObjList is an array of objects with the given properties.
func ObjList(name, desc string, required string, props ...Prop) Prop {
	var inner map[string]any
	_ = json.Unmarshal(Obj(required, props...), &inner)
	return Prop{name, map[string]any{"type": "array", "description": desc, "items": inner}}
}

// Obj builds an object schema. required is a comma-separated list of property names.
func Obj(required string, props ...Prop) json.RawMessage {
	p := map[string]any{}
	for _, pr := range props {
		p[pr.Name] = pr.Def
	}
	s := map[string]any{"type": "object", "properties": p}
	if required != "" {
		var req []string
		for _, r := range strings.Split(required, ",") {
			if r = strings.TrimSpace(r); r != "" {
				req = append(req, r)
			}
		}
		s["required"] = req
	}
	b, _ := json.Marshal(s)
	return b
}

// ConfirmIfTainted asks the user before a read-only tool exposes private local data while untrusted
// content (web page, mail, MCP output) is in the turn's context: reads are otherwise auto-approved,
// which would let an injected instruction read a file and then send it out. what describes the access.
func ConfirmIfTainted(ctx context.Context, env *Env, tool, what string) error {
	if env == nil || !env.Tainted {
		return nil
	}
	return Confirm(ctx, env, tool, what,
		fmt.Sprintf("%s wants to read %s, and untrusted content (web/mail/mcp) is in this turn's context.", env.Agent, what))
}

// Confirm asks the user to approve one specific action (shown as text, with what as the detail) regardless of
// the tool's SAFE/ARMED state. It errors when the user declines or nobody can be asked.
func Confirm(ctx context.Context, env *Env, tool, what, text string) error {
	if env == nil || env.Ask == nil {
		return fmt.Errorf("%s: needs your approval (%s), but no one can be asked right now", tool, what)
	}
	ans, err := env.Ask(ctx, Question{Kind: "confirm", Tool: tool, Args: what, Options: []string{"allow", "deny"}, Text: text})
	if err != nil {
		return err
	}
	if a := strings.ToLower(strings.TrimSpace(ans)); a != "allow" && a != "yes" && a != "y" && a != "ok" {
		return fmt.Errorf("the user did not approve: %s", what)
	}
	return nil
}
