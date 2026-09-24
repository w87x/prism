package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/llm"
	"prism/internal/textmatch"
)

// State is the user-controlled state of a tool.
type State struct {
	Enabled bool
	Armed   bool // armed tools run without confirmation in auto-mode
}

type Registry struct {
	master atomic.Bool // master arm: every enabled tool runs without asking (taint rules still apply)
	// ignoreTaint lets the master arm also skip the extra prompts that untrusted content (web pages, mail…)
	// causes. Off by default: it removes the main defence against prompt injection.
	ignoreTaint atomic.Bool
	db          *pgxpool.Pool
	mu          sync.RWMutex
	tools       map[string]*Tool
	state       map[string]stateRow
}

type stateRow struct {
	enabled bool
	armed   *bool
}

func NewRegistry(db *pgxpool.Pool) *Registry {
	return &Registry{db: db, tools: map[string]*Tool{}, state: map[string]stateRow{}}
}

// Load reads persisted tool settings.
func (r *Registry) Load(ctx context.Context) error {
	rows, err := r.db.Query(ctx, `SELECT name, enabled, armed FROM tool_settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	st := map[string]stateRow{}
	for rows.Next() {
		var n string
		var s stateRow
		if err := rows.Scan(&n, &s.enabled, &s.armed); err != nil {
			return err
		}
		st[n] = s
	}
	r.mu.Lock()
	r.state = st
	r.mu.Unlock()
	return rows.Err()
}

func (r *Registry) Register(ts ...*Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range ts {
		if t.Source == "" {
			t.Source = "builtin"
		}
		r.tools[t.Name] = t
	}
}

// UnregisterSource drops every tool registered from source (e.g. "mcp:github").
func (r *Registry) UnregisterSource(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for n, t := range r.tools {
		if t.Source == source {
			delete(r.tools, n)
		}
	}
}

func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) All() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) State(name string) State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t := r.tools[name]
	s := State{Enabled: true}
	if t != nil {
		s.Armed = t.Risk == RiskRead || t.Auto
	}
	if row, ok := r.state[name]; ok {
		s.Enabled = row.enabled
		if row.armed != nil {
			s.Armed = *row.armed
		}
	}
	if r.master.Load() {
		s.Armed = true
	}
	return s
}

// SetState persists on/off and safe/armed for a tool (nil leaves a field unchanged).
func (r *Registry) SetState(ctx context.Context, name string, enabled, armed *bool) error {
	cur := r.State(name)
	if enabled != nil {
		cur.Enabled = *enabled
	}
	if armed != nil {
		cur.Armed = *armed
	}
	if _, err := r.db.Exec(ctx, `INSERT INTO tool_settings(name,enabled,armed) VALUES($1,$2,$3)
		ON CONFLICT (name) DO UPDATE SET enabled=EXCLUDED.enabled, armed=EXCLUDED.armed`, name, cur.Enabled, cur.Armed); err != nil {
		return err
	}
	a := cur.Armed
	r.mu.Lock()
	r.state[name] = stateRow{enabled: cur.Enabled, armed: &a}
	r.mu.Unlock()
	return nil
}

func (t *Tool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.Params}
}

// Search ranks enabled tools against the query.
func (r *Registry) Search(query string, limit int) []*Tool {
	all := r.All()
	var cand []*Tool
	var docs []string
	for _, t := range all {
		if !r.State(t.Name).Enabled {
			continue
		}
		cand = append(cand, t)
		docs = append(docs, strings.ReplaceAll(t.Name, "_", " ")+" "+t.Category+" "+t.Description)
	}
	hits := textmatch.Rank(query, docs, limit)
	out := make([]*Tool, 0, len(hits))
	for _, h := range hits {
		out = append(out, cand[h.Index])
	}
	return out
}

// Describe renders a tool with its parameters for tool_search results.
func Describe(t *Tool) string {
	return fmt.Sprintf("- %s [%s]: %s\n  params: %s", t.Name, t.Risk, t.Description, string(t.Params))
}

// SetMaster switches the master arm (all tools armed) on or off.
func (r *Registry) SetMaster(on bool) { r.master.Store(on) }

// Master reports the master arm state.
func (r *Registry) Master() bool { return r.master.Load() }

// SetIgnoreTaint sets whether the master arm also skips taint prompts (only effective while the master arm is on).
func (r *Registry) SetIgnoreTaint(on bool) { r.ignoreTaint.Store(on) }

// IgnoreTaint reports whether untrusted content no longer causes prompts: master arm on AND the option chosen.
func (r *Registry) IgnoreTaint() bool { return r.master.Load() && r.ignoreTaint.Load() }
