package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/consult"
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

// consultPlanner sends one planning call to an outside model through the consult service (Codex CLI or a chat website on
// the user's own subscription). It is slow — minutes — and the prompt leaves this machine, so it is opt-in. The answer is
// parsed like any model reply (the reply may carry prose around the JSON; llm.ExtractJSON copes).
func (s *Server) consultPlanner(j *obJob, provider string) onboarding.Completer {
	return func(ctx context.Context, title, system, user string, _ bool) (string, error) {
		svc := s.App.Ext.Consult
		if svc == nil {
			return "", errors.New("consult is not available")
		}
		if provider == "" || provider == "default" {
			provider = svc.DefaultProvider(ctx)
		}
		j.addThought("\n▸ " + title + " — asking " + provider + " (this can take several minutes)…\n")
		prompt := system + "\n\n--- the user's request ---\n" + user + "\n\nReply with the JSON object only, no commentary."
		job, err := svc.Start(ctx, provider, onboardingHirer, prompt, 30*time.Minute, consult.Options{})
		if err != nil {
			return "", err
		}
		snap, err := svc.Wait(ctx, job.ID, 30*time.Minute)
		if err != nil {
			return "", err
		}
		if snap.Status == "running" {
			_ = svc.Cancel(job.ID)
			return "", errors.New("no answer in time")
		}
		if snap.Status != "done" || strings.TrimSpace(snap.Answer) == "" {
			return "", fmt.Errorf("%s: %s", provider, strings.TrimSpace(snap.Error+" "+snap.Status))
		}
		j.addThought(snap.Answer + "\n")
		return snap.Answer, nil
	}
}

// planOptions are the Propose options for one onboarding job: the streaming completer for every call, plus an outside
// planner for the team-planning call when the user picked one.
func planOptions(s *Server, j *obJob, model string, job int64, planWith string) []onboarding.Option {
	opts := []onboarding.Option{onboarding.WithCompleter(s.onboardingCompleter(job, j, model))}
	if planWith != "" && s.App.Ext.Consult != nil {
		opts = append(opts, onboarding.WithPlanner(s.consultPlanner(j, planWith)))
	}
	return opts
}
