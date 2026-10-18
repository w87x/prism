package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/app"
	"prism/internal/codexapp"
	"prism/internal/llm"
	"prism/internal/settings"
)

func (s *Server) registerLLM() {
	a := s.App
	st := func() *llm.Store { return a.LLM.Store() }

	rpc(s, "providers.list", func(ctx context.Context, _ none) (map[string]any, error) {
		ps, err := st().Providers(ctx)
		if err != nil {
			return nil, err
		}
		for i := range ps {
			for j := range ps[i].Keys {
				ps[i].Keys[j].APIKey = "" // secrets never leave the backend
			}
		}
		return map[string]any{"providers": ps, "presets": llm.ProviderPresets}, nil
	})
	rpc(s, "providers.save", func(ctx context.Context, p llm.Provider) (int64, error) {
		if strings.TrimSpace(p.Name) == "" {
			return 0, errors.New("name is required")
		}
		isNew := p.ID == 0
		id, err := st().SaveProvider(ctx, p)
		if err != nil {
			return 0, err
		}
		if isNew && p.Kind == llm.KindCodex { // one step: the plan's models are there as soon as the provider is
			importCodexModels(ctx, a, id)
		}
		return id, nil
	})
	rpc(s, "providers.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, st().DeleteProvider(ctx, r.ID)
	})
	rpc(s, "keys.save", func(ctx context.Context, k llm.Key) (int64, error) { return st().SaveKey(ctx, k) })
	rpc(s, "keys.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, st().DeleteKey(ctx, r.ID)
	})
	// discover lists the models a provider offers (LM Studio: with load state, context and tool support)
	rpc(s, "providers.discover", func(ctx context.Context, r struct {
		ProviderID int64  `json:"provider_id"`
		BaseURL    string `json:"base_url"`
		APIKey     string `json:"api_key"`
	}) ([]llm.Discovered, error) {
		base, key := r.BaseURL, r.APIKey
		if r.ProviderID != 0 {
			ps, err := st().Providers(ctx)
			if err != nil {
				return nil, err
			}
			for _, p := range ps {
				if p.ID == r.ProviderID {
					base = p.BaseURL
					for _, k := range p.Keys {
						if k.Enabled && key == "" {
							key = k.APIKey
						}
					}
				}
			}
		}
		if base == "" {
			return nil, errors.New("no base URL")
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return a.LLM.Client().ListModels(cctx, base, key)
	})
	// the behind-the-scenes actions whose model can be chosen (main / aux), with their defaults; the choices live in the "model_actions" setting
	// codex.status: whether the Codex CLI is installed and signed in, and the plan's usage windows (no credential is ever included)
	rpc(s, "codex.status", func(ctx context.Context, _ none) (codexapp.Status, error) {
		cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		return codexapp.Default.Status(cctx), nil
	})
	// codex.login starts signing in with ChatGPT through Codex itself; the UI opens the URL (or shows the code) and polls codex.login_status
	rpc(s, "codex.login", func(ctx context.Context, r struct {
		Device bool `json:"device"`
	}) (*codexapp.Login, error) {
		cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		return codexapp.Default.LoginStart(cctx, r.Device)
	})
	rpc(s, "codex.login_status", func(ctx context.Context, r struct {
		ID string `json:"id"`
	}) (codexapp.LoginResult, error) {
		return codexapp.Default.LoginStatus(r.ID), nil
	})
	// codex.import_models adds the models of the signed-in plan to a Codex provider (after signing in; adding the provider does it too)
	rpc(s, "codex.import_models", func(ctx context.Context, r struct {
		ProviderID int64 `json:"provider_id"`
	}) (int, error) {
		return importCodexModels(ctx, a, r.ProviderID), nil
	})
	rpc(s, "models.actions", func(ctx context.Context, _ none) ([]llm.Action, error) { return llm.Actions, nil })
	rpc(s, "models.list", func(ctx context.Context, _ none) ([]llm.Model, error) { return st().Models(ctx) })
	rpc(s, "models.save", func(ctx context.Context, m llm.Model) (int64, error) {
		if strings.TrimSpace(m.Name) == "" || m.ModelID == "" || m.ProviderID == 0 {
			return 0, errors.New("name, provider and model id are required")
		}
		return st().SaveModel(ctx, m)
	})
	rpc(s, "models.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, st().DeleteModel(ctx, r.ID)
	})
	// models.import turns discovered models into configured ones in one step
	rpc(s, "models.import", func(ctx context.Context, r struct {
		ProviderID int64            `json:"provider_id"`
		Items      []llm.Discovered `json:"items"`
	}) (int, error) {
		existing, _ := st().Models(ctx)
		names := map[string]bool{}
		for _, m := range existing {
			names[m.Name] = true
		}
		n := 0
		for _, d := range r.Items {
			name := d.ID
			if names[name] {
				continue
			}
			if _, err := st().SaveModel(ctx, llm.Model{Name: name, ProviderID: r.ProviderID, ModelID: d.ID, Kind: d.Kind,
				ContextWindow: d.Context, SupportsTools: d.Tools, Vision: d.Vision}); err != nil {
				return n, err
			}
			names[name] = true
			n++
		}
		return n, nil
	})
	rpc(s, "lists.list", func(ctx context.Context, _ none) ([]llm.List, error) { return st().Lists(ctx) })
	rpc(s, "lists.save", func(ctx context.Context, l llm.List) (int64, error) {
		if strings.TrimSpace(l.Name) == "" {
			return 0, errors.New("name is required")
		}
		return st().SaveList(ctx, l)
	})
	rpc(s, "lists.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, st().DeleteList(ctx, r.ID)
	})
	rpc(s, "roles.get", func(ctx context.Context, _ none) (settings.ModelRoles, error) {
		return settings.Load(ctx, a.Settings, settings.KeyModelRoles, settings.ModelRoles{}), nil
	})
	rpc(s, "roles.set", func(ctx context.Context, r settings.ModelRoles) (bool, error) {
		a.LLM.Invalidate()
		return true, a.Settings.Set(ctx, settings.KeyModelRoles, r)
	})
	// llm.check probes one model (reply, JSON, tool calls, silence) and remembers the report for the Models page
	rpc(s, "llm.check", func(ctx context.Context, r struct {
		Ref string `json:"ref"`
	}) (llm.CheckReport, error) {
		if strings.TrimSpace(r.Ref) == "" {
			return llm.CheckReport{}, fmt.Errorf("pick a model")
		}
		rep := a.LLM.CheckModel(ctx, r.Ref)
		var saved struct {
			Checks map[string]llm.CheckReport `json:"checks"`
		}
		saved = settings.Load(ctx, a.Settings, settings.KeyModelChecks, saved)
		if saved.Checks == nil {
			saved.Checks = map[string]llm.CheckReport{}
		}
		saved.Checks[r.Ref] = rep
		_ = a.Settings.Set(ctx, settings.KeyModelChecks, saved)
		return rep, nil
	})
	// llm.test sends a tiny prompt through the full routing path (fallback, key rotation included)
	rpc(s, "llm.test", func(ctx context.Context, r struct {
		Ref  string `json:"ref"`
		Kind string `json:"kind"`
	}) (map[string]any, error) {
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		start := time.Now()
		if r.Kind == "embedding" {
			v, err := a.LLM.Embed(cctx, r.Ref, []string{"hello world"})
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "dim": len(v[0]), "ms": time.Since(start).Milliseconds()}, nil
		}
		res, err := a.LLM.Chat(cctx, r.Ref, llm.Request{Messages: []llm.Message{{Role: "user", Content: "Reply with exactly: pong"}}, MaxTokens: 400}, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "model": res.Model, "reply": strings.TrimSpace(res.Content), "ms": time.Since(start).Milliseconds(),
			"tokens_in": res.Usage.Prompt, "tokens_out": res.Usage.Completion}, nil
	})

	// ── generic settings ──
	rpc(s, "settings.get", func(ctx context.Context, r struct {
		Key string `json:"key"`
	}) (json.RawMessage, error) {
		var raw json.RawMessage
		ok, err := a.Settings.Get(ctx, r.Key, &raw)
		if err != nil || !ok {
			return json.RawMessage("null"), err
		}
		return raw, nil
	})
	rpc(s, "settings.set", func(ctx context.Context, r struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}) (bool, error) {
		if strings.TrimSpace(r.Key) == "" {
			return false, fmt.Errorf("key required")
		}
		var v any
		if err := json.Unmarshal(r.Value, &v); err != nil {
			return false, err
		}
		if err := a.Settings.Set(ctx, r.Key, v); err != nil {
			return false, err
		}
		if r.Key == settings.KeyRuntime { // takes effect immediately
			a.Engine.SetLLMConcurrency(settings.Load(ctx, a.Settings, settings.KeyRuntime, settings.DefaultRuntime()).LLMConcurrency)
		}
		return true, nil
	})
}

// importCodexModels adds the models the signed-in ChatGPT plan offers as configured models of the provider (best effort: a plan that is
// not signed in yet simply has none until "Discover models" is used after signing in).
func importCodexModels(ctx context.Context, a *app.App, providerID int64) int {
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	found, err := a.LLM.Client().ListModels(cctx, llm.CodexBaseURL, "")
	if err != nil {
		return 0
	}
	existing, _ := a.LLM.Store().Models(ctx)
	names := map[string]bool{}
	for _, m := range existing {
		names[m.Name] = true
	}
	n := 0
	for _, d := range found {
		if names[d.ID] {
			continue
		}
		if _, err := a.LLM.Store().SaveModel(ctx, llm.Model{Name: d.ID, ProviderID: providerID, ModelID: d.ID, Kind: d.Kind, ContextWindow: d.Context, SupportsTools: d.Tools, Vision: d.Vision}); err == nil {
			n++
		}
	}
	return n
}
