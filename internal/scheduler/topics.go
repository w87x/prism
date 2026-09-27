package scheduler

import (
	"context"
	"strings"
)

// Telegram topic organisation for crons and intents. Each gets a resolved topic name: when its text plainly belongs
// to an existing project (memory bank), it shares that project's topic with everything else pointed at it —
// otherwise it gets one of its own, named after it. Briefings always go to a fixed "Briefings" topic instead (see
// AddBriefing); this file is only about crons and intents.

const projectMatchPrompt = `Decide whether this scheduled job or standing watch belongs to one of the user's existing projects, so its Telegram notifications can be grouped with that project's instead of getting their own.

Answer JSON only: {"project":"<exact name from the list, or empty>"}. Only name a project when the job is clearly about that project's subject matter — not a vague or generic association. Never invent a project not in the list.`

// resolveTopic works out the Telegram topic a cron or intent's notices should use: an existing project's name
// (bankID > 0) when the text is clearly about that project, otherwise its own name (bankID == 0, name == own).
// own should already be a short, human label (a cron's name, or an intent/watch's description clipped short).
func (s *Service) resolveTopic(ctx context.Context, own, text string) (name string, bankID int64) {
	name = clipTopic(own)
	if s.Engine == nil || s.Engine.Memory == nil || s.Engine.LLM == nil || s.Engine.LLM.RoleRef(ctx, "fast") == "" {
		return name, 0
	}
	banks, err := s.Engine.Memory.Banks(ctx)
	if err != nil {
		return name, 0
	}
	var sb strings.Builder
	n := 0
	for _, b := range banks {
		if b.Kind == "project" && b.Status == "active" {
			sb.WriteString("- " + b.Name)
			if b.Description != "" {
				sb.WriteString(": " + b.Description)
			}
			sb.WriteByte('\n')
			n++
		}
	}
	if n == 0 {
		return name, 0
	}
	var out struct {
		Project string `json:"project"`
	}
	prompt := "Existing projects:\n" + sb.String() + "\nJob: " + text
	if err := s.Engine.LLM.CompleteJSON(ctx, "role:fast", projectMatchPrompt, prompt, &out); err != nil {
		return name, 0
	}
	sugg := strings.TrimSpace(out.Project)
	if sugg == "" {
		return name, 0
	}
	b, err := s.Engine.Memory.BankBySpec(ctx, "project:"+sugg, "", false)
	if err != nil || b.Kind != "project" {
		return name, 0
	}
	return clipTopic(b.Name), b.ID
}

func clipTopic(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return s
}

// releaseTopic deletes a cron's or intent's own Telegram topic when it is removed — but only when the topic was
// solely its own (projectBankID == 0): a shared project topic stays, since other crons/intents may still use it.
func (s *Service) releaseTopic(ctx context.Context, projectBankID int64, topic string) {
	if projectBankID != 0 || topic == "" || s.DeleteTopic == nil {
		return
	}
	if err := s.DeleteTopic(ctx, topic); err != nil {
		s.logf("warn", "could not delete Telegram topic %q: %v", topic, err)
	}
}
