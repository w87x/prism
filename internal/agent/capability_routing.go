package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"prism/internal/tools"
)

var namedHandoff = regexp.MustCompile(`(?i)^\s*(?:please\s+)?(?:use|ask|have)\s+(.{1,60}?)\s+(?:to|for)\b`)
var publicDomain = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9-]*\.(?:com|org|net|ru|io|ai|co|de|uk|fr|jp|app|dev)\b`)

// Some local models put the requested specialist in the instruction instead of the agent field.
// Only an unambiguous opening directive naming an existing specialist is interpreted this way.
func (e *Engine) namedSpecialist(ctx context.Context, instruction, caller string) string {
	m := namedHandoff.FindStringSubmatch(instruction)
	if len(m) != 2 {
		return ""
	}
	p, err := e.Profiles.Get(ctx, strings.TrimSpace(m[1]))
	if err != nil || !p.Enabled || (p.Role != RoleWorker && p.Role != RoleMaint) || strings.EqualFold(p.Name, caller) {
		return ""
	}
	return p.Name
}

func (e *Engine) profileHasTools(p Profile, required []string) bool {
	for _, name := range required {
		if name == "ask_colleague" {
			return false
		} // delegated runs route through their requester
		t, ok := e.Tools.Get(name)
		if !ok || !e.Tools.State(name).Enabled || !t.AllowedFor(p.Name) || (p.Probation && t.Risk == tools.RiskExec) {
			return false
		}
		if !inTeam(p.Tools, name) && !(t.Base && !t.Deferred && workerBase[name]) {
			return false
		}
	}
	return true
}

// inferredToolIntent only handles strong signals. It never grants a capability; it selects an existing holder.
func inferredToolIntent(request string) string {
	s := strings.ToLower(request)
	search := strings.Contains(s, "search") || strings.Contains(s, "look up") || strings.Contains(s, "lookup")
	if !search || strings.Contains(s, "jackett") || strings.Contains(s, "local file") || strings.Contains(s, "folder") || strings.Contains(s, "directory") {
		return ""
	}
	if strings.Contains(s, "schedule") || strings.Contains(s, "cron") || strings.Contains(s, "recurring") {
		return ""
	}
	if publicDomain.MatchString(s) || strings.Contains(s, "the web") || strings.Contains(s, "web search") || strings.Contains(s, "online") {
		return "web_search"
	}
	return ""
}

func (e *Engine) validateRequiredTools(ctx context.Context, agent string, required []string) error {
	if len(required) == 0 {
		return nil
	}
	p, err := e.Profiles.Get(ctx, agent)
	if err != nil {
		return err
	}
	if !e.profileHasTools(*p, required) {
		return fmt.Errorf("%s does not hold all the requested enabled tools (%s); pick a capable specialist or omit required_tools if they are not actually required", p.Name, strings.Join(required, ", "))
	}
	return nil
}

// A restricted caller cannot pass an exec requirement to an otherwise unrestricted child.
func (e *Engine) validateRequestedTools(required []string, restricted bool) error {
	if len(required) > 8 {
		return fmt.Errorf("at most 8 required tools")
	}
	for _, name := range required {
		t, ok := e.Tools.Get(name)
		if !ok || !e.Tools.State(name).Enabled {
			return fmt.Errorf("required tool %q is unavailable or disabled", name)
		}
		if restricted && t.Risk == tools.RiskExec {
			return fmt.Errorf("required tool %q is unavailable while restricted", name)
		}
	}
	return nil
}

// bestSpecialistForTools considers every worker, including capable agents whose profile wording did not
// rank in the lexical top twelve. An explicit requirement is never relaxed silently.
func (e *Engine) bestSpecialistForTools(ctx context.Context, request, caller string, required []string) (string, error) {
	if len(required) == 0 {
		return e.bestSpecialist(ctx, request, caller)
	}
	if len(required) > 8 {
		return "", fmt.Errorf("at most 8 required tools")
	}
	for _, name := range required {
		if _, ok := e.Tools.Get(name); !ok || !e.Tools.State(name).Enabled {
			return "", fmt.Errorf("required tool %q is unavailable or disabled", name)
		}
	}
	ranked, err := e.Profiles.Search(ctx, request, 0)
	if err != nil {
		return "", err
	}
	all, err := e.Profiles.List(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range append(ranked, all...) {
		if p.Role == RoleWorker && p.Enabled && !strings.EqualFold(p.Name, caller) && e.profileHasTools(p, required) {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no enabled specialist holds every required tool (%s)", strings.Join(required, ", "))
}
