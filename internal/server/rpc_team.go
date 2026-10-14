package server

import (
	"context"
	"time"

	"prism/internal/agent"
	"prism/internal/scheduler"
)

// Team practices: runbooks, decisions, the reviewer's log, standups, ratings and model health (see internal/agent/runbooks.go,
// decisions.go, review.go, standup.go, incident.go).
func (s *Server) registerTeam() {
	a := s.App

	rpc(s, "runbooks.list", func(ctx context.Context, _ none) ([]agent.Runbook, error) { return a.Engine.Runbooks(ctx) })
	rpc(s, "runbooks.save", func(ctx context.Context, r struct {
		ID     int64  `json:"id"`
		Steps  string `json:"steps"`
		Status string `json:"status"`
	}) (bool, error) {
		return true, a.Engine.SaveRunbook(ctx, r.ID, r.Steps, r.Status)
	})
	rpc(s, "runbooks.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Engine.DeleteRunbook(ctx, r.ID)
	})

	rpc(s, "decisions.list", func(ctx context.Context, r struct {
		Status string `json:"status"`
	}) ([]agent.Decision, error) {
		return a.Engine.Decisions(ctx, r.Status)
	})
	rpc(s, "decisions.add", func(ctx context.Context, r struct {
		Title    string `json:"title"`
		Decision string `json:"decision"`
		Why      string `json:"why"`
		Days     int    `json:"review_in_days"`
	}) (int64, error) {
		d := agent.Decision{Title: r.Title, Decision: r.Decision, Rationale: r.Why, By: "user"}
		if r.Days > 0 {
			t := time.Now().AddDate(0, 0, r.Days)
			d.ReviewAt = &t
		}
		return a.Engine.RecordDecision(ctx, d)
	})
	rpc(s, "decisions.status", func(ctx context.Context, r struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}) (bool, error) {
		return true, a.Engine.SetDecisionStatus(ctx, r.ID, r.Status)
	})
	rpc(s, "decisions.extend", func(ctx context.Context, r struct {
		ID   int64 `json:"id"`
		Days int   `json:"days"`
	}) (bool, error) {
		return true, a.Engine.ExtendDecision(ctx, r.ID, r.Days)
	})
	rpc(s, "decisions.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Engine.DeleteDecision(ctx, r.ID)
	})

	rpc(s, "review.log", func(ctx context.Context, _ none) ([]agent.ReviewLogRow, error) { return a.Engine.ReviewLog(ctx, 60) })

	rpc(s, "standup.list", func(ctx context.Context, _ none) ([]agent.Standup, error) { return a.Engine.Standups(ctx, 12) })
	rpc(s, "standup.run", func(ctx context.Context, _ none) (*agent.Standup, error) {
		now := time.Now()
		return a.Engine.RunStandup(ctx, now.AddDate(0, 0, -7), now, false)
	})

	rpc(s, "tasks.rate", func(ctx context.Context, r struct {
		ID     int64  `json:"id"`
		Rating int    `json:"rating"`
		Note   string `json:"note"`
	}) (bool, error) {
		return true, a.Engine.RateTask(ctx, r.ID, r.Rating, r.Note)
	})

	rpc(s, "llm.health", func(ctx context.Context, _ none) (map[string]any, error) {
		down, why, since := a.LLM.Down()
		out := map[string]any{"down": down}
		if down {
			out["reason"], out["since"] = why, since
		}
		return out, nil
	})

	// pain reflex, ant trails, trust, immune memory, person model
	rpc(s, "breaker.list", func(ctx context.Context, _ none) ([]agent.BreakerRow, error) { return a.Engine.PausedTools(), nil })
	rpc(s, "breaker.resume", func(ctx context.Context, r struct {
		Tool string `json:"tool"`
	}) (bool, error) {
		a.Engine.ResumeTool(r.Tool)
		return true, nil
	})
	rpc(s, "trails.list", func(ctx context.Context, _ none) ([]agent.TrailRow, error) { return a.Engine.Trails(ctx), nil })
	rpc(s, "trust.list", func(ctx context.Context, _ none) ([]agent.TrustRow, error) { return a.Engine.TrustTable(ctx), nil })
	rpc(s, "trust.reset", func(ctx context.Context, r struct {
		Agent string `json:"agent"`
		Tool  string `json:"tool"`
	}) (bool, error) {
		a.Engine.ResetTrust(ctx, r.Agent, r.Tool)
		return true, nil
	})
	rpc(s, "antibodies.list", func(ctx context.Context, _ none) ([]agent.Antibody, error) { return a.Engine.Antibodies(ctx) })
	rpc(s, "antibodies.add", func(ctx context.Context, r struct {
		Agent    string   `json:"agent"`
		Triggers []string `json:"triggers"`
		Rule     string   `json:"rule"`
	}) (bool, error) {
		return true, a.Engine.AddAntibody(ctx, r.Agent, r.Triggers, r.Rule, "added by the user")
	})
	rpc(s, "antibodies.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Engine.DeleteAntibody(ctx, r.ID)
	})
	rpc(s, "person.refresh", func(ctx context.Context, _ none) (string, error) { return a.Engine.RefreshPersonModel(ctx) })

	// the autonomy dial: what PRISM changed in itself, and the proposals waiting
	rpc(s, "evolution.watches", func(ctx context.Context, _ none) ([]agent.EvolutionWatchRow, error) {
		return a.Engine.EvolutionWatches(ctx), nil
	})
	rpc(s, "evolution.undo", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Engine.UndoEvolution(ctx, r.ID)
	})
	jobs := func() *scheduler.Service { return a.Ext.Sched }
	rpc(s, "jobs.proposals", func(ctx context.Context, r struct {
		Status string `json:"status"`
	}) ([]scheduler.JobProposal, error) {
		return jobs().JobProposals(ctx, r.Status)
	})
	rpc(s, "jobs.review", func(ctx context.Context, _ none) (int, error) { return jobs().ReviewJobs(ctx) })
	rpc(s, "jobs.apply", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, jobs().ApplyJobProposal(ctx, r.ID, false)
	})
	rpc(s, "jobs.dismiss", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, jobs().DismissJobProposal(ctx, r.ID)
	})
	rpc(s, "jobs.undo", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, jobs().UndoJobProposal(ctx, r.ID)
	})
}
