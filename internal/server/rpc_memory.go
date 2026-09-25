package server

import (
	"context"
	"errors"
	"time"

	"prism/internal/memory"
	"prism/internal/settings"
	"prism/internal/tasks"
)

func (s *Server) registerMemory() {
	a := s.App
	rpc(s, "memory.stats", func(ctx context.Context, _ none) (memory.Stats, error) { return a.Memory.Stats(ctx), nil })
	rpc(s, "memory.banks", func(ctx context.Context, _ none) ([]memory.Bank, error) { return a.Memory.Banks(ctx) })
	rpc(s, "memory.bank_save", func(ctx context.Context, b memory.Bank) (int64, error) {
		if b.Kind == "" || b.Name == "" {
			return 0, errors.New("kind and name are required")
		}
		return a.Memory.SaveBank(ctx, b)
	})
	rpc(s, "memory.bank_delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Memory.DeleteBank(ctx, r.ID)
	})
	rpc(s, "memory.facts", func(ctx context.Context, r struct {
		BankID  int64  `json:"bank_id"`
		Kind    string `json:"kind"`
		Q       string `json:"q"`
		History bool   `json:"history"`
		Limit   int    `json:"limit"`
		Offset  int    `json:"offset"`
	}) ([]memory.Fact, error) {
		return a.Memory.FactsKind(ctx, r.BankID, r.Kind, r.Q, r.History, r.Limit, r.Offset)
	})
	rpc(s, "memory.fact_update", func(ctx context.Context, r struct {
		ID   int64    `json:"id"`
		Text *string  `json:"text"`
		Tags []string `json:"tags"`
		Rank *float64 `json:"rank"`
	}) (*memory.Fact, error) {
		return a.Memory.UpdateFact(ctx, r.ID, r.Text, r.Tags, r.Rank)
	})
	rpc(s, "memory.fact_delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Memory.DeleteFact(ctx, r.ID)
	})
	rpc(s, "memory.fact_pin", func(ctx context.Context, r struct {
		ID     int64 `json:"id"`
		Pinned bool  `json:"pinned"`
	}) (bool, error) {
		return true, a.Memory.SetPinned(ctx, r.ID, r.Pinned)
	})
	// "mark outdated": retire a fact with no replacement, distinct from correcting it (fact_update with new
	// text, which supersedes) — this is "no longer true/relevant", not "here's what's true instead".
	rpc(s, "memory.fact_outdate", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Memory.MarkOutdated(ctx, r.ID)
	})
	rpc(s, "memory.fact_move", func(ctx context.Context, r struct {
		ID     int64 `json:"id"`
		BankID int64 `json:"bank_id"`
	}) (bool, error) {
		return true, a.Memory.MoveFact(ctx, r.ID, r.BankID)
	})
	rpc(s, "memory.find", func(ctx context.Context, r struct {
		Query   string   `json:"query"`
		Banks   []string `json:"banks"`
		K       int      `json:"k"`
		History bool     `json:"history"`
		Deep    bool     `json:"deep"`
	}) ([]memory.Fact, error) {
		if len(r.Banks) == 0 {
			bs, err := a.Memory.Banks(ctx)
			if err != nil {
				return nil, err
			}
			for _, b := range bs {
				r.Banks = append(r.Banks, b.Label())
			}
		}
		return a.Memory.Find(ctx, memory.FindReq{Query: r.Query, Banks: r.Banks, K: r.K, History: r.History, Deep: r.Deep})
	})
	rpc(s, "memory.store", func(ctx context.Context, r struct {
		Bank string   `json:"bank"`
		Text string   `json:"text"`
		Tags []string `json:"tags"`
	}) (*memory.StoreResult, error) {
		return a.Memory.Store(ctx, memory.StoreReq{Bank: r.Bank, Text: r.Text, Tags: r.Tags, Source: "user", Confidence: 0.95})
	})
	rpc(s, "memory.links", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) ([]memory.Linked, error) {
		return a.Memory.Links(ctx, r.ID)
	})
	rpc(s, "memory.link", func(ctx context.Context, r struct {
		A    int64  `json:"a"`
		B    int64  `json:"b"`
		Kind string `json:"kind"`
		Note string `json:"note"`
	}) (bool, error) {
		return true, a.Memory.Link(ctx, r.A, r.B, r.Kind, r.Note, "user", 0.9)
	})
	rpc(s, "memory.unlink", func(ctx context.Context, r struct {
		A int64 `json:"a"`
		B int64 `json:"b"`
	}) (bool, error) {
		return true, a.Memory.Unlink(ctx, r.A, r.B)
	})
	rpc(s, "memory.bank_merge", func(ctx context.Context, r struct {
		Sources []int64 `json:"sources"`
		Into    int64   `json:"into"`
		Name    string  `json:"name"`
	}) (*memory.MergeResult, error) {
		return a.Memory.MergeBanks(ctx, r.Sources, r.Into, r.Name)
	})
	rpc(s, "memory.bank_split", func(ctx context.Context, r struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		Description string  `json:"description"`
		FactIDs     []int64 `json:"fact_ids"`
	}) (*memory.SplitResult, error) {
		return a.Memory.SplitBank(ctx, r.ID, r.Name, r.Description, r.FactIDs)
	})
	rpc(s, "memory.bank_suggest_split", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) ([]memory.SplitGroup, error) {
		return a.Memory.SuggestSplit(ctx, r.ID)
	})
	rpc(s, "memory.auto_merge", func(ctx context.Context, _ none) ([]memory.MergeResult, error) {
		out, err := a.Memory.DedupeBanks(ctx)
		if err != nil {
			return out, err
		}
		more, err := a.Memory.AutoMergeBanks(ctx)
		return append(out, more...), err
	})
	// reflection: bank_id 0 = every bank that has gathered enough new facts
	rpc(s, "memory.reflect", func(ctx context.Context, r struct {
		BankID int64 `json:"bank_id"`
	}) ([]memory.ReflectResult, error) {
		var rs []memory.ReflectResult
		var err error
		if r.BankID != 0 {
			var res memory.ReflectResult
			res, err = a.Memory.Reflect(ctx, r.BankID, true, 0)
			rs = []memory.ReflectResult{res}
		} else {
			rs, err = a.Memory.ReflectAll(ctx, true, 6)
		}
		for _, x := range rs {
			a.Logf("info", "memory", "reflection (manual) — %s", x)
		}
		return rs, err
	})
	// deep analysis: bank_id 0 = every bank with enough new facts
	rpc(s, "memory.analyze", func(ctx context.Context, r struct {
		BankID int64 `json:"bank_id"`
	}) ([]memory.AnalyzeResult, error) {
		var rs []memory.AnalyzeResult
		var err error
		if r.BankID != 0 {
			var res memory.AnalyzeResult
			res, err = a.Memory.Analyze(ctx, r.BankID, true, 0)
			rs = []memory.AnalyzeResult{res}
		} else {
			rs, err = a.Memory.AnalyzeAll(ctx, 3)
		}
		for _, x := range rs {
			a.Logf("info", "memory", "analysis (manual) — %s", x)
		}
		return rs, err
	})
	// levels of thinking: level 2 (synthesis across banks) and 3 (principles); 0 = both, in order
	rpc(s, "memory.synthesize", func(ctx context.Context, r struct {
		Level int `json:"level"`
	}) ([]memory.SynthResult, error) {
		var rs []memory.SynthResult
		levels := []int{2, 3}
		if r.Level == 2 || r.Level == 3 {
			levels = []int{r.Level}
		}
		for _, lv := range levels {
			x, err := a.Memory.Synthesize(ctx, lv, true, 0)
			if err != nil {
				return rs, err
			}
			a.Logf("info", "memory", "synthesis (manual) — %s", x)
			rs = append(rs, x)
		}
		return rs, nil
	})
	rpc(s, "memory.confirm", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Memory.ConfirmFact(ctx, r.ID)
	})
	rpc(s, "memory.resolve_insight", func(ctx context.Context, r struct {
		ID      int64  `json:"id"`
		Verdict string `json:"verdict"`
		Answer  string `json:"answer"`
	}) (bool, error) {
		_, err := a.Memory.ResolveInsight(ctx, r.ID, r.Verdict, r.Answer)
		return err == nil, err
	})
	rpc(s, "memory.models", func(ctx context.Context, _ none) ([]memory.Model, error) { return a.Memory.Models(ctx) })
	rpc(s, "memory.model_save", func(ctx context.Context, r struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Query  string `json:"query"`
		BankID *int64 `json:"bank_id"`
	}) (int64, error) {
		if r.BankID != nil && *r.BankID == 0 {
			r.BankID = nil
		}
		id, err := a.Memory.SaveModel(ctx, r.ID, r.Name, r.Query, r.BankID)
		if err == nil && r.ID == 0 {
			_, _ = a.Memory.RefreshModel(ctx, id) // first answer straight away; it may fail while memory is empty
		}
		return id, err
	})
	rpc(s, "memory.model_delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Memory.DeleteModel(ctx, r.ID)
	})
	rpc(s, "memory.model_refresh", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (*memory.Model, error) {
		return a.Memory.RefreshModel(ctx, r.ID)
	})
	rpc(s, "memory.status", func(ctx context.Context, _ none) (map[string]any, error) {
		type ev struct {
			ID      int64  `json:"id"`
			TS      string `json:"ts"`
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		rows, err := a.DB.Query(ctx, `SELECT id, to_char(ts,'YYYY-MM-DD"T"HH24:MI:SSTZH:TZM'), level, message FROM logs
			WHERE source='memory' ORDER BY id DESC LIMIT 60`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		events := []ev{}
		for rows.Next() {
			var e ev
			if err := rows.Scan(&e.ID, &e.TS, &e.Level, &e.Message); err != nil {
				return nil, err
			}
			events = append(events, e)
		}
		return map[string]any{"status": a.MemoryStatus(), "events": events}, rows.Err()
	})
	// semi-automatic enrichment: hand a research task about a fact, entity, bank or topic to an agent
	rpc(s, "memory.enrich", func(ctx context.Context, r struct {
		memory.EnrichReq
		Agent string `json:"agent"`
	}) (*tasks.Task, error) {
		title, input, err := a.Memory.EnrichBrief(ctx, r.EnrichReq)
		if err != nil {
			return nil, err
		}
		if r.Agent == "" {
			r.Agent = "Atlas"
		}
		t, err := a.Engine.Enqueue(ctx, tasks.Task{FromKind: "user", FromName: "user", ToAgent: r.Agent, Title: title, Input: input})
		if err != nil {
			return nil, err
		}
		return &t, nil
	})
	rpc(s, "memory.verify_now", func(ctx context.Context, r struct {
		IDs []int64 `json:"ids"`
	}) (int, error) {
		return a.VerifyMemory(ctx, r.IDs, false)
	})
	// memory an agent used: the facts its memory_find / memory_models calls returned during a task
	rpc(s, "memory.used", func(ctx context.Context, r struct {
		TaskID int64 `json:"task_id"`
	}) ([]memory.Fact, error) {
		t, err := a.Tasks.Get(ctx, r.TaskID)
		if err != nil {
			return nil, err
		}
		out := []memory.Fact{}
		if t.SessionID == nil {
			return out, nil
		}
		ms, err := a.Sessions.Messages(ctx, *t.SessionID)
		if err != nil {
			return nil, err
		}
		seen := map[int64]bool{}
		for _, m := range ms {
			if m.Role != "tool" || (m.Name != "memory_find" && m.Name != "memory_models") {
				continue
			}
			for _, id := range memory.UsedFactIDs(m.Content) {
				if seen[id] || len(out) >= 40 {
					continue
				}
				seen[id] = true
				if f, err := a.Memory.GetFact(ctx, id); err == nil {
					out = append(out, f)
				}
			}
		}
		return out, nil
	})
	rpc(s, "memory.feedback", func(ctx context.Context, r struct {
		ID     int64 `json:"id"`
		Useful bool  `json:"useful"`
	}) (bool, error) {
		return true, a.Memory.Feedback(ctx, r.ID, r.Useful)
	})
	rpc(s, "memory.provenance", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (*memory.Provenance, error) {
		return a.Memory.Provenance(ctx, r.ID)
	})
	// time view: what memory believed at a date, and what changed day by day
	rpc(s, "memory.at", func(ctx context.Context, r struct {
		At     time.Time `json:"at"`
		BankID int64     `json:"bank_id"`
		Q      string    `json:"q"`
		Limit  int       `json:"limit"`
		Offset int       `json:"offset"`
	}) ([]memory.Fact, error) {
		return a.Memory.FactsAt(ctx, r.At, r.BankID, r.Q, r.Limit, r.Offset)
	})
	rpc(s, "memory.timeline", func(ctx context.Context, r struct {
		Days   int   `json:"days"`
		BankID int64 `json:"bank_id"`
	}) ([]memory.TimelineDay, error) {
		if r.Days <= 0 || r.Days > 730 {
			r.Days = 30
		}
		return a.Memory.Timeline(ctx, time.Now().AddDate(0, 0, -r.Days), r.BankID)
	})
	rpc(s, "memory.digest_now", func(ctx context.Context, _ none) (bool, error) {
		return a.MemoryDigest(ctx, settings.Load(ctx, a.Settings, settings.KeyMemory, settings.Memory{}), true)
	})
	rpc(s, "memory.health", func(ctx context.Context, _ none) ([]memory.BankHealth, error) {
		return a.Memory.Health(ctx)
	})
	rpc(s, "memory.graph", func(ctx context.Context, r struct {
		BankID  int64 `json:"bank_id"`
		History bool  `json:"history"`
		Limit   int   `json:"limit"`
	}) (*memory.Graph, error) {
		return a.Memory.Graph(ctx, r.BankID, r.History, r.Limit)
	})
	rpc(s, "memory.fact", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (memory.Fact, error) {
		return a.Memory.GetFact(ctx, r.ID)
	})
	rpc(s, "memory.export", func(ctx context.Context, r struct {
		BankIDs []int64 `json:"bank_ids"`
	}) (*memory.Dump, error) {
		return a.Memory.Export(ctx, r.BankIDs)
	})
	rpc(s, "memory.import", func(ctx context.Context, r struct {
		Dump *memory.Dump `json:"dump"`
	}) (*memory.ImportResult, error) {
		return a.Memory.Import(ctx, r.Dump)
	})
	rpc(s, "memory.ops", func(ctx context.Context, _ none) ([]memory.Op, error) { return a.Memory.Ops(ctx) })
	rpc(s, "memory.undo", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (string, error) {
		return a.Memory.Undo(ctx, r.ID)
	})
	rpc(s, "memory.reindex", func(ctx context.Context, _ none) (int, error) { return a.Memory.Reindex(ctx) })
	rpc(s, "memory.process", func(ctx context.Context, _ none) (int, error) { return a.Memory.Process(ctx, 60, true) })
	rpc(s, "memory.prune", func(ctx context.Context, _ none) (map[string]int, error) {
		x, y, err := a.Memory.Prune(ctx, 180*24*3600*1e9)
		return map[string]int{"archived": x, "purged": y}, err
	})
	rpc(s, "memory.review", func(ctx context.Context, r struct {
		Limit int `json:"limit"`
	}) (*memory.Review, error) {
		return a.Memory.Review(ctx, r.Limit)
	})
	// entity graph: bank_id 0 = every bank that has gathered new facts since its last pass
	rpc(s, "memory.entities_extract", func(ctx context.Context, r struct {
		BankID int64 `json:"bank_id"`
	}) ([]memory.EntityExtractResult, error) {
		if r.BankID != 0 {
			res, err := a.Memory.ExtractEntities(ctx, r.BankID, true)
			return []memory.EntityExtractResult{res}, err
		}
		bs, err := a.Memory.Banks(ctx)
		if err != nil {
			return nil, err
		}
		var out []memory.EntityExtractResult
		for _, b := range bs {
			res, err := a.Memory.ExtractEntities(ctx, b.ID, false)
			if err != nil {
				return out, err
			}
			if res.Skipped == "" {
				out = append(out, res)
			}
		}
		return out, nil
	})
	rpc(s, "memory.entity_graph", func(ctx context.Context, r struct {
		BankID  int64 `json:"bank_id"`
		History bool  `json:"history"`
		Limit   int   `json:"limit"`
	}) (*memory.EntityGraphResult, error) {
		return a.Memory.EntityGraph(ctx, r.BankID, r.History, r.Limit)
	})
	rpc(s, "memory.entity_facts", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) ([]memory.Fact, error) {
		return a.Memory.EntityFacts(ctx, r.ID)
	})
	rpc(s, "memory.entity_link_history", func(ctx context.Context, r struct {
		A int64 `json:"a"`
		B int64 `json:"b"`
	}) ([]memory.EntityLinkState, error) {
		return a.Memory.EntityLinkHistory(ctx, r.A, r.B)
	})
	rpc(s, "memory.full_graph", func(ctx context.Context, r struct {
		BankID  int64 `json:"bank_id"`
		History bool  `json:"history"`
		Limit   int   `json:"limit"`
	}) (*memory.FullGraphResult, error) {
		return a.Memory.FullGraph(ctx, r.BankID, r.History, r.Limit)
	})
}
