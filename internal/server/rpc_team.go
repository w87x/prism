package server

import (
	"context"
	"time"

	"prism/internal/agent"
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
}
