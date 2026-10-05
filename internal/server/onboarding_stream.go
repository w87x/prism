package server

import (
	"context"
	"time"

	"prism/internal/llm"
	"prism/internal/onboarding"
)

// onboardingHirer is who the team-writing appears as among the running agents: Forge hires agents, and onboarding is
// Forge's work done in front of the user.
const onboardingHirer = "Forge"

// onboardingCompleter runs onboarding's model calls as visible agent runs of Forge (depth 1, so the agents graph lights
// its line from Atlas): the tokens stream to the thinking panel, the active-agents list and the onboarding window, and
// the tail is kept on the job for a window opened later. Planning is one run, each agent's soul another.
func (s *Server) onboardingCompleter(job int64, j *obJob, model string) onboarding.Completer {
	a := s.App
	return func(ctx context.Context, title, system, user string, jsonOut bool) (string, error) {
		run := a.Engine.NewRunID()
		window := a.LLM.Window(ctx, model)
		a.Emit("run.start", map[string]any{"run": run, "agent": onboardingHirer, "depth": 1, "title": title, "onboarding": job})
		j.addThought("\n▸ " + title + "\n")
		msgs := []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
		estIn, estOut, last := (len(system)+len(user))/4, 0, time.Now()
		resp, err := a.LLM.Chat(ctx, model, llm.Request{Messages: msgs, JSON: jsonOut}, func(d llm.Delta) {
			estOut += (len(d.Content) + len(d.Reasoning)) / 4
			if time.Since(last) > 700*time.Millisecond { // a running estimate until the real usage arrives
				last = time.Now()
				a.Emit("run.usage", map[string]any{"run": run, "agent": onboardingHirer, "tokens_in": estIn, "tokens_out": estOut, "context": estIn + estOut, "window": window})
			}
			if d.Reasoning != "" {
				j.addThought(d.Reasoning)
				a.Emit("run.delta", map[string]any{"run": run, "agent": onboardingHirer, "kind": "thinking", "text": d.Reasoning})
			}
			if d.Content != "" {
				j.addThought(d.Content)
				a.Emit("run.delta", map[string]any{"run": run, "agent": onboardingHirer, "kind": "content", "text": d.Content})
			}
		})
		status, msg, in, out := "done", "", 0, 0
		if err != nil {
			status, msg = "failed", err.Error()
		} else {
			in, out = resp.Usage.Prompt, resp.Usage.Completion
			a.Emit("run.usage", map[string]any{"run": run, "agent": onboardingHirer, "tokens_in": in, "tokens_out": out, "context": in + out, "window": window})
		}
		j.addThought("\n")
		a.Emit("run.end", map[string]any{"run": run, "agent": onboardingHirer, "depth": 1, "status": status, "error": msg, "tokens_in": in, "tokens_out": out})
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}
}
