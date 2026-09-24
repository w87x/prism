package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/tools"
)

type Server struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Enabled   bool              `json:"enabled"`
	Armed     bool              `json:"armed"`
	SignedIn  bool              `json:"signed_in"` // OAuth: tokens are stored (never sent to the UI)
}

// Status is the live state of a server for the UI.
type Status struct {
	ID    int64    `json:"id"`
	State string   `json:"state"` // connected | error | disabled | connecting
	Error string   `json:"error,omitempty"`
	Tools []string `json:"tools"`
}

type Manager struct {
	db  *pgxpool.Pool
	reg *tools.Registry

	mu      sync.Mutex
	clients map[int64]*Client
	status  map[int64]*Status
	OnState func()
	// AllowPrivate lets OAuth discovery reach local/LAN addresses (the Settings → Web choice); nil → never.
	AllowPrivate func() bool

	flows oauthFlows
	tokMu sync.Mutex
}

func NewManager(db *pgxpool.Pool, reg *tools.Registry) *Manager {
	return &Manager{db: db, reg: reg, clients: map[int64]*Client{}, status: map[int64]*Status{}}
}

func (m *Manager) List(ctx context.Context) ([]Server, error) {
	rows, err := m.db.Query(ctx, `SELECT id,name,transport,command,args,env,url,headers,enabled,armed,oauth FROM mcp_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		var s Server
		var env, hdr, oa []byte
		if err := rows.Scan(&s.ID, &s.Name, &s.Transport, &s.Command, &s.Args, &env, &s.URL, &hdr, &s.Enabled, &s.Armed, &oa); err != nil {
			return nil, err
		}
		var ost OAuthState
		_ = json.Unmarshal(oa, &ost)
		s.SignedIn = ost.signedIn()
		_ = json.Unmarshal(env, &s.Env)
		_ = json.Unmarshal(hdr, &s.Headers)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Get returns one server.
func (m *Manager) Get(ctx context.Context, id int64) (Server, error) {
	all, err := m.List(ctx)
	if err != nil {
		return Server{}, err
	}
	for _, s := range all {
		if s.ID == id {
			return s, nil
		}
	}
	return Server{}, fmt.Errorf("server not found")
}

func (m *Manager) Save(ctx context.Context, s Server) (int64, error) {
	if strings.TrimSpace(s.Name) == "" {
		return 0, fmt.Errorf("name is required")
	}
	if s.Transport == "" {
		s.Transport = "stdio"
	}
	if s.Args == nil {
		s.Args = []string{}
	}
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	if s.Headers == nil {
		s.Headers = map[string]string{}
	}
	env, _ := json.Marshal(s.Env)
	hdr, _ := json.Marshal(s.Headers)
	var id int64
	var err error
	if s.ID == 0 {
		err = m.db.QueryRow(ctx, `INSERT INTO mcp_servers(name,transport,command,args,env,url,headers,enabled,armed) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
			s.Name, s.Transport, s.Command, s.Args, env, s.URL, hdr, s.Enabled, s.Armed).Scan(&id)
	} else {
		id = s.ID
		_, err = m.db.Exec(ctx, `UPDATE mcp_servers SET name=$2,transport=$3,command=$4,args=$5,env=$6,oauth=CASE WHEN url<>$7 THEN '{}'::jsonb ELSE oauth END,url=$7,headers=$8,enabled=$9,armed=$10 WHERE id=$1`,
			s.ID, s.Name, s.Transport, s.Command, s.Args, env, s.URL, hdr, s.Enabled, s.Armed)
	}
	return id, err
}

func (m *Manager) Delete(ctx context.Context, id int64) error {
	m.disconnect(id)
	_, err := m.db.Exec(ctx, `DELETE FROM mcp_servers WHERE id=$1`, id)
	return err
}

func (m *Manager) Statuses() map[int64]Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]Status{}
	for id, s := range m.status {
		out[id] = *s
	}
	return out
}

func (m *Manager) setStatus(id int64, st Status) {
	m.mu.Lock()
	st.ID = id
	m.status[id] = &st
	cb := m.OnState
	m.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (m *Manager) disconnect(id int64) {
	m.mu.Lock()
	c := m.clients[id]
	delete(m.clients, id)
	old := m.status[id]
	delete(m.status, id)
	m.mu.Unlock()
	if c != nil {
		c.Close()
	}
	if old != nil {
		m.reg.UnregisterSource(fmt.Sprintf("mcp:%d", id))
	}
}

// Start connects every enabled server in the background.
func (m *Manager) Start(ctx context.Context) {
	srvs, err := m.List(ctx)
	if err != nil {
		return
	}
	for _, s := range srvs {
		if s.Enabled {
			go func(s Server) { _ = m.Connect(ctx, s) }(s)
		} else {
			m.setStatus(s.ID, Status{State: "disabled"})
		}
	}
}

// Stop closes all connections.
func (m *Manager) Stop() {
	m.mu.Lock()
	ids := make([]int64, 0, len(m.clients))
	for id := range m.clients {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.disconnect(id)
	}
}

var nonIdent = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func toolName(server, tool string) string {
	n := "mcp__" + nonIdent.ReplaceAllString(server, "_") + "__" + nonIdent.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// Connect (re)connects one server and registers its tools.
func (m *Manager) Connect(ctx context.Context, s Server) error {
	m.disconnect(s.ID)
	m.setStatus(s.ID, Status{State: "connecting"})
	cfg := Config{Name: s.Name, Transport: s.Transport, Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL, Headers: s.Headers}
	if s.Transport == "http" {
		cfg.Token = func(ctx context.Context, force bool) (string, error) { return m.tokenFor(ctx, s.ID, force) }
	}
	c, err := Connect(ctx, cfg)
	if err != nil {
		m.setAuthOrError(s.ID, err)
		return err
	}
	lctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ts, err := c.ListTools(lctx)
	if err != nil {
		c.Close()
		m.setAuthOrError(s.ID, err)
		return err
	}
	source := fmt.Sprintf("mcp:%d", s.ID)
	var names []string
	for _, t := range ts {
		t := t
		full := toolName(s.Name, t.Name)
		params := t.InputSchema
		if len(params) == 0 || !strings.Contains(string(params), `"type"`) {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		risk := tools.RiskExec
		if t.Annotations.ReadOnly {
			risk = tools.RiskRead
		}
		desc := strings.TrimSpace(t.Description)
		if len(desc) > 600 {
			desc = desc[:600] + "…"
		}
		m.reg.Register(&tools.Tool{
			Name: full, Description: fmt.Sprintf("[MCP %s] %s", s.Name, desc), Params: params, Category: "mcp:" + s.Name,
			Risk: risk, Deferred: true, Untrusted: true, Auto: s.Armed, Source: source,
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				m.mu.Lock()
				cl := m.clients[s.ID]
				m.mu.Unlock()
				if cl == nil || !cl.Alive() {
					return "", fmt.Errorf("MCP server %s is not connected", s.Name)
				}
				cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				defer cancel()
				return cl.CallTool(cctx, t.Name, raw)
			},
		})
		names = append(names, full)
	}
	m.mu.Lock()
	m.clients[s.ID] = c
	m.mu.Unlock()
	m.setStatus(s.ID, Status{State: "connected", Tools: names})
	return nil
}

// setAuthOrError records why a connection failed: a server that wants a sign-in is not an "error" the user must debug.
func (m *Manager) setAuthOrError(id int64, err error) {
	var ae *AuthRequiredError
	if errors.As(err, &ae) {
		m.setStatus(id, Status{State: "needs_auth", Error: "sign in to use this server"})
		return
	}
	m.setStatus(id, Status{State: "error", Error: err.Error()})
}

// Reload reconnects one server by id.
func (m *Manager) Reload(ctx context.Context, id int64) error {
	srvs, err := m.List(ctx)
	if err != nil {
		return err
	}
	for _, s := range srvs {
		if s.ID == id {
			if !s.Enabled {
				m.disconnect(id)
				m.setStatus(id, Status{State: "disabled"})
				return nil
			}
			return m.Connect(ctx, s)
		}
	}
	return fmt.Errorf("server not found")
}

// Connected counts servers by state for the status bar.
func (m *Manager) Counts() (connected, total int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.status {
		if s.State == "disabled" {
			continue
		}
		total++
		if s.State == "connected" {
			connected++
		}
	}
	return
}
