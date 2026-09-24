package memory

import (
	"context"
	"fmt"
	"strings"

	"prism/internal/settings"
)

// guidance is the "who is who" block prepended to every model call that reads conversations or facts
// (distillation, reflection, entity extraction). Without it the clerk only sees "Atlas → Sherpa: …" lines and
// happily invents things like "Danil (also known as Sherpa)" — it cannot tell an agent name from a person.
// It carries the user's own name, the roster of software agents (never people, never aliases of the user),
// and the user's free-text hints from Settings → Advanced → Memory pipeline.
func (s *Service) guidance(ctx context.Context) string {
	var sb strings.Builder
	sb.WriteString("WHO IS WHO (authoritative — never contradict it):\n")
	if s.settings != nil {
		if g := settings.Load(ctx, s.settings, settings.KeyGeneral, settings.General{}); strings.TrimSpace(g.UserName) != "" {
			fmt.Fprintf(&sb, "- The user is the human %s. Lines from \"user\" are theirs.\n", strings.TrimSpace(g.UserName))
		} else {
			sb.WriteString("- The user is the human owner of this assistant. Lines from \"user\" are theirs.\n")
		}
	}
	if rows, err := s.db.Query(ctx, `SELECT name FROM agent_profiles ORDER BY name`); err == nil {
		var names []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				names = append(names, n)
			}
		}
		rows.Close()
		if len(names) > 0 {
			fmt.Fprintf(&sb, "- These are software agents inside the assistant, NOT people and never aliases, nicknames or roles of the user: %s. Never write things like \"<user> (also known as <agent>)\", never treat an agent as a person, friend or contact of the user.\n", strings.Join(names, ", "))
		}
	}
	if s.settings != nil {
		if m := settings.Load(ctx, s.settings, settings.KeyMemory, settings.Memory{}); strings.TrimSpace(m.Hints) != "" {
			fmt.Fprintf(&sb, "\nThe user's own instructions for memory (follow them):\n%s\n", strings.TrimSpace(m.Hints))
		}
	}
	if c := s.userCard(ctx); c != "" {
		fmt.Fprintf(&sb, "\nBackground profile of the user, derived earlier (context only; it may be incomplete or wrong, never store it back as new facts):\n%s\n", c)
	}
	sb.WriteString("\n")
	return sb.String()
}

// Guidance is the who-is-who and user-hints block memory jobs prepend to their prompts.
func (s *Service) Guidance(ctx context.Context) string { return s.guidance(ctx) }
