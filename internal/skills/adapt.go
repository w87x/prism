package skills

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"prism/internal/llm"
)

const adaptPrompt = `You adapt an "agent skill" (a SKILL.md instruction file written for another agent platform such as OpenClaw, Hermes, Claude Code or similar) so it works natively inside PRISM, a personal assistant with multiple agents.

Rules:
- Keep the YAML frontmatter (name, description) and the substance: purpose, steps, decision rules, examples, output formats. Do not shorten or invent content.
- Remove or rewrite every mention of the source platform, its brand names, its CLI, slash commands, file layout, config paths and plugin systems. The skill must read as if it was written for PRISM. Never mention OpenClaw, Hermes, Claude Code, Anthropic, or ClawHub.
- Map platform-specific tool names to PRISM tools where an obvious equivalent exists. PRISM tools: %s. If the skill relies on a capability PRISM lacks, keep the step but phrase it generically (e.g. "use a shell command").
- References to bundled files (scripts/, references/) must be kept as relative paths; they are loadable with skill_load(file=...).
- Output ONLY the complete adapted SKILL.md (frontmatter included), no commentary, no code fence around it.`

var fenceRe = regexp.MustCompile("(?s)^```[a-zA-Z]*\\n(.*)\\n```\\s*$")

// Adapter rewrites skills with the chat model. toolNames is the list of PRISM tool names.
func Adapter(r *llm.Router, toolNames func() []string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, md string) (string, error) {
		if !r.HasChat(ctx) {
			return "", errors.New("no chat model configured")
		}
		sys := strings.Replace(adaptPrompt, "%s", strings.Join(toolNames(), ", "), 1)
		out, err := r.Complete(ctx, "", sys, md, false)
		if err != nil {
			return "", err
		}
		out = strings.TrimSpace(out)
		if m := fenceRe.FindStringSubmatch(out); m != nil {
			out = m[1]
		}
		if !strings.HasPrefix(out, "---") {
			return "", errors.New("the model did not return a SKILL.md with frontmatter")
		}
		low := strings.ToLower(out)
		for _, w := range []string{"openclaw", "hermes", "clawhub", "claude code"} {
			if strings.Contains(low, w) {
				// one automatic repair pass
				fix, err := r.Complete(ctx, "", sys+"\nThe previous attempt still mentioned '"+w+"'. Remove every such mention.", out, false)
				if err == nil && strings.HasPrefix(strings.TrimSpace(fix), "---") {
					out = strings.TrimSpace(fix)
				}
				break
			}
		}
		return scrubPlatforms(out), nil
	}
}

var platformRe = regexp.MustCompile(`(?i)\b(open\s?claw|clawhub|hermes(?:\s+agent)?)\b`)

// scrubPlatforms is the last line of defence: whatever the model left behind is replaced by "PRISM".
func scrubPlatforms(s string) string { return platformRe.ReplaceAllString(s, "PRISM") }
