package server

import (
	"context"
	"encoding/json"

	"prism/internal/plugins"
	"prism/internal/tools"
)

func (s *Server) registerTools() {
	a := s.App
	type toolInfo struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Category    string          `json:"category"`
		Risk        string          `json:"risk"`
		Base        bool            `json:"base"`
		Deferred    bool            `json:"deferred"`
		Untrusted   bool            `json:"untrusted"`
		Source      string          `json:"source"`
		Enabled     bool            `json:"enabled"`
		Armed       bool            `json:"armed"`
		Params      json.RawMessage `json:"params"`
		Only        []string        `json:"only,omitempty"`
	}
	rpc(s, "tools.list", func(ctx context.Context, _ none) ([]toolInfo, error) {
		var out []toolInfo
		for _, t := range a.Tools.All() {
			st := a.Tools.State(t.Name)
			out = append(out, toolInfo{t.Name, t.Description, t.Category, t.Risk.String(), t.Base, t.Deferred, t.Untrusted, t.Source, st.Enabled, st.Armed, t.Params, t.Only})
		}
		return out, nil
	})
	rpc(s, "tools.set", func(ctx context.Context, r struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
		Armed   *bool  `json:"armed"`
	}) (bool, error) {
		return true, a.Tools.SetState(ctx, r.Name, r.Enabled, r.Armed)
	})
	// tools.set_many applies a state to several tools (group toggles in the UI)
	rpc(s, "tools.set_many", func(ctx context.Context, r struct {
		Names   []string `json:"names"`
		Enabled *bool    `json:"enabled"`
		Armed   *bool    `json:"armed"`
	}) (bool, error) {
		for _, n := range r.Names {
			if err := a.Tools.SetState(ctx, n, r.Enabled, r.Armed); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	type masterState struct {
		Armed       bool `json:"armed"`
		IgnoreTaint bool `json:"ignore_taint"`
	}
	rpc(s, "tools.master", func(ctx context.Context, r struct {
		Set         bool  `json:"set"`
		Armed       bool  `json:"armed"`
		IgnoreTaint *bool `json:"ignore_taint"`
	}) (masterState, error) {
		state := func() masterState {
			return masterState{Armed: a.Tools.Master(), IgnoreTaint: a.Tools.IgnoreTaint()}
		}
		if r.Set {
			ign := a.Tools.IgnoreTaint()
			if r.IgnoreTaint != nil {
				ign = *r.IgnoreTaint
			}
			if !r.Armed { // the option only means something while the master arm is on
				ign = false
			}
			a.Tools.SetMaster(r.Armed)
			a.Tools.SetIgnoreTaint(ign)
			if err := a.Settings.Set(ctx, "tools_master", masterState{Armed: r.Armed, IgnoreTaint: ign}); err != nil {
				return state(), err
			}
		}
		return state(), nil
	})
	// ── runtime plugins ──
	rpc(s, "plugins.list", func(ctx context.Context, _ none) ([]plugins.Plugin, error) { return a.Ext.Plugins.List(ctx, false) })
	rpc(s, "plugins.get", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (plugins.Plugin, error) {
		return a.Ext.Plugins.Get(ctx, r.ID)
	})
	rpc(s, "plugins.status", func(ctx context.Context, r struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}) (bool, error) {
		return true, a.Ext.Plugins.SetStatus(ctx, r.ID, r.Status)
	})
	rpc(s, "plugins.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Ext.Plugins.Delete(ctx, r.ID)
	})
	rpc(s, "plugins.info", func(ctx context.Context, _ none) (map[string]any, error) {
		return map[string]any{"sandboxed": a.Ext.Plugins.Sandboxed()}, nil
	})
	_ = tools.RiskRead
}
