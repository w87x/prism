package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/tools"
)

func preview(p Plugin) string {
	code := p.Code
	if r := []rune(code); len(r) > 3000 {
		code = string(r[:3000]) + "\n…[truncated]"
	}
	net := "no network, no writes outside its own folder"
	if p.Network {
		net = "NEEDS NETWORK ACCESS"
	}
	return fmt.Sprintf("plugin_%s — %s\nRuns: %s, %ds limit\n\n%s", p.Name, p.Description, net, p.TimeoutS, code)
}

// RegisterTools installs the tools agents use to write and manage plugins. Each approved plugin then appears
// as its own tool (plugin_<name>, found with tool_search).
func RegisterTools(reg *tools.Registry, m *Manager) {
	reg.Register(
		&tools.Tool{
			Name: "plugin_create", Category: "plugins", Risk: tools.RiskWrite,
			Description: "Write a NEW tool as a small Python program when no existing tool does the job and you expect to need it again (a parser, a converter, a calculation). The user reads the code and must approve it before anything runs; it then appears as plugin_<name>. " +
				"Contract: define `def run(args):` — args is a dict; return a string or JSON-serialisable data. Standard library only. It runs offline in a sandbox and can write only inside its own folder (set network=true only if it truly needs the internet; the user is told). " +
				"Do not use this for one-off work: use the python tool for that.",
			Params: tools.Obj("name,description,code", tools.Str("name", "lowercase name, letters/digits/_ (the tool becomes plugin_<name>)"), tools.Str("description", "what it does and when to use it"),
				tools.Str("code", "the complete Python source, defining run(args)"), tools.Any("params", "JSON schema of args: {\"type\":\"object\",\"properties\":{\"x\":{\"type\":\"number\",\"description\":\"…\"}},\"required\":[\"x\"]}"),
				tools.Bool("network", "the plugin needs internet access"), tools.Int("timeout_s", "time limit in seconds (default 30, max 300)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Name, Description, Code string
					Params                  json.RawMessage
					Network                 bool
					TimeoutS                int `json:"timeout_s"`
				}](raw)
				if err != nil {
					return "", err
				}
				p, err := m.Create(ctx, Plugin{Name: a.Name, Description: a.Description, Code: a.Code, Params: a.Params, Network: a.Network, TimeoutS: a.TimeoutS, CreatedBy: env.Agent})
				if err != nil {
					return "", err
				}
				// code written while untrusted content (a web page, mail…) is in play is never approved from inside
				// that conversation: it waits for the user in Tools → Plugins
				if env.Tainted {
					return fmt.Sprintf("Plugin %q was saved as PENDING. Untrusted content was in this conversation, so it cannot be approved here: the user reviews the code in Tools → Plugins.", p.Name), nil
				}
				if err := tools.Confirm(ctx, env, "plugin_create", "plugin_"+p.Name, fmt.Sprintf("%s wrote a new tool. Read the code before you allow it to run:\n\n%s", env.Agent, preview(p))); err != nil {
					return fmt.Sprintf("Plugin %q was saved as PENDING (%v). The user can review and approve it in Tools → Plugins.", p.Name, err), nil
				}
				if err := m.SetStatus(ctx, p.ID, StatusApproved); err != nil {
					return "", err
				}
				return fmt.Sprintf("Plugin approved. Use tool_search for %q to load plugin_%s, then call it.", p.Name, p.Name), nil
			},
		},
		&tools.Tool{
			Name: "plugin_list", Category: "plugins", Risk: tools.RiskRead,
			Description: "List the runtime plugins (name, status, what they do).",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				ps, err := m.List(ctx, false)
				if err != nil {
					return "", err
				}
				if len(ps) == 0 {
					return "No plugins yet.", nil
				}
				var sb strings.Builder
				for _, p := range ps {
					fmt.Fprintf(&sb, "- plugin_%s [%s] %s\n", p.Name, p.Status, p.Description)
				}
				return strings.TrimSpace(sb.String()), nil
			},
		},
		&tools.Tool{
			Name: "plugin_delete", Category: "plugins", Risk: tools.RiskWrite,
			Description: "Delete a runtime plugin (the user is asked to confirm).",
			Params:      tools.Obj("name", tools.Str("name", "plugin name")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Name string }](raw)
				if err != nil {
					return "", err
				}
				ps, _ := m.List(ctx, false)
				for _, p := range ps {
					if p.Name == strings.TrimPrefix(strings.ToLower(strings.TrimSpace(a.Name)), "plugin_") {
						if err := tools.Confirm(ctx, env, "plugin_delete", "plugin_"+p.Name, fmt.Sprintf("%s wants to delete the plugin %s (%s).", env.Agent, p.Name, p.Description)); err != nil {
							return "", err
						}
						return "Deleted.", m.Delete(ctx, p.ID)
					}
				}
				return "", fmt.Errorf("no plugin called %q", a.Name)
			},
		},
	)
}
