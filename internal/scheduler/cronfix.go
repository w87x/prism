package scheduler

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var parenSuffix = regexp.MustCompile(`\s*[(\[].*[)\]]\s*$`)

// agentName returns the real name of the agent a job is meant for. A model copies what it saw ("Lyra (custom)", "lyra") rather than the
// name: such a job belonged to no agent, so not even Lyra could change it, and it could only fire through a guess at the nearest agent.
func (s *Service) agentName(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	for _, try := range []string{name, strings.TrimSpace(parenSuffix.ReplaceAllString(name, ""))} {
		if try == "" {
			continue
		}
		if p, err := s.Engine.Profiles.Get(ctx, try); err == nil {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("there is no agent named %q: use an agent's exact name (agent_find lists them)", name)
}

// checkDayFields refuses a cron expression that sets both the day of the month and the day of the week: in cron that means "on either",
// so "0 12 20,21,22 * 1" runs on every Monday as well as on the 20th to 22nd.
func checkDayFields(expr string) error {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil // the caller's own parsing reports a malformed expression
	}
	set := func(s string) bool { return s != "*" && s != "?" }
	if set(f[2]) && set(f[4]) {
		return fmt.Errorf("%q sets both the day of the month and the day of the week, and cron then runs on EITHER (every matching weekday as well as the listed dates). Use one of them; for 'only if the 20th is a Monday', run daily on that day and let the job check the weekday", expr)
	}
	return nil
}

// warnDayFields tells the user, once per schedule, about an existing one that sets both day fields (made before they were refused): it
// runs on more days than its name says. It is not rewritten — which of the two was meant is theirs to say.
func (s *Service) warnDayFields(ctx context.Context) {
	if s.Notify == nil || s.Settings == nil {
		return
	}
	cs, err := s.Crons(ctx)
	if err != nil {
		return
	}
	var told []int64
	_, _ = s.Settings.Get(ctx, "cron_dayfields_told", &told)
	seen := map[int64]bool{}
	for _, id := range told {
		seen[id] = true
	}
	changed := false
	for _, c := range cs {
		if !c.Enabled || seen[c.ID] || checkDayFields(c.Expr) == nil {
			continue
		}
		s.Notify("cron", "warning", "A schedule runs on more days than intended", fmt.Sprintf("“%s” (%s) sets both the day of the month and the day of the week, so it runs on either: every matching weekday as well as the listed dates. Ask %s to change it to one of them.", c.Name, c.Expr, c.Agent))
		told = append(told, c.ID)
		changed = true
	}
	if changed {
		_ = s.Settings.Set(ctx, "cron_dayfields_told", told)
	}
}
