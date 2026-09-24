package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"prism/internal/tools"
)

// RegisterHubTools lets agents discover and install skills from TRUSTED hubs on their own.
// Installing always adapts the skill for PRISM. adapt and token are supplied by the app.
func RegisterHubTools(reg *tools.Registry, s *Store, adapt func(context.Context, string) (string, error), token func(context.Context) string) {
	reg.Register(
		&tools.Tool{
			Name: "skill_hub_search", Category: "skills", Risk: tools.RiskRead, Untrusted: true,
			Description: "Search the skill hubs the user marked as trusted for a skill you could install (name + description only). Install with skill_hub_install.",
			Params:      tools.Obj("query", tools.Str("query", "what capability you need"), tools.Int("limit", "max results (default 6)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Query string
					Limit int
				}](raw)
				if err != nil {
					return "", err
				}
				if a.Limit <= 0 {
					a.Limit = 6
				}
				hits, err := s.SearchHubs(ctx, a.Query, token(ctx), true, a.Limit)
				if err != nil {
					return "", err
				}
				if len(hits) == 0 {
					return "No matching skills in trusted hubs (or no hub is marked trusted).", nil
				}
				var sb strings.Builder
				for _, h := range hits {
					fmt.Fprintf(&sb, "- %s [hub %q, path %s]: %s\n", h.Name, h.Hub, h.Path, clip(h.Description, 160))
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "skill_hub_install", Category: "skills", Risk: tools.RiskWrite,
			Description: "Install (and adapt for PRISM) a skill from a trusted hub, found with skill_hub_search. Afterwards load it with skill_load.",
			Params:      tools.Obj("hub,path", tools.Str("hub", "hub name from the search result"), tools.Str("path", "skill path from the search result")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Hub, Path string }](raw)
				if err != nil {
					return "", err
				}
				hubs, err := s.Hubs(ctx)
				if err != nil {
					return "", err
				}
				for _, h := range hubs {
					if strings.EqualFold(h.Name, a.Hub) {
						if !h.Trusted || !h.Enabled {
							return "", errors.New("that hub is not trusted; ask the user to install the skill from the Tools page")
						}
						k, err := s.Import(ctx, h, a.Path, token(ctx), adapt)
						if err != nil {
							return "", err
						}
						return fmt.Sprintf("Installed skill %q (adapted). Load it with skill_load.", k.Name), nil
					}
				}
				return "", fmt.Errorf("unknown hub %q", a.Hub)
			},
		},
	)
}
