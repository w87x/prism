package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/agent"
	"prism/internal/app"
	"prism/internal/db"
	"prism/internal/metrics"
	"prism/internal/settings"
	"prism/internal/tasks"
)

func (s *Server) registerCore() {
	a := s.App

	rpcOpen(s, "app.state", func(ctx context.Context, _ none) (app.Status, error) { return a.Status(ctx), nil })

	// ── setup (works without a database) ──
	rpcOpen(s, "setup.info", func(ctx context.Context, _ none) (map[string]any, error) {
		return map[string]any{"dsn": db.Redact(a.Cfg.DSN), "has_dsn": a.Cfg.DSN != "", "data_dir": a.Cfg.DataDir, "ready": a.Ready()}, nil
	})
	type dsnReq struct {
		DSN      string `json:"dsn"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		User     string `json:"user"`
		Password string `json:"password"`
		Database string `json:"database"`
		Create   bool   `json:"create"`
	}
	resolveDSN := func(r dsnReq) string {
		if r.DSN != "" {
			return r.DSN
		}
		if r.Port == 0 {
			r.Port = 5432
		}
		if r.Database == "" {
			r.Database = "prism"
		}
		return db.BuildDSN(r.Host, r.Port, r.User, r.Password, r.Database)
	}
	rpcOpen(s, "setup.test", func(ctx context.Context, r dsnReq) (map[string]any, error) {
		dsn := resolveDSN(r)
		cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		info, err := db.Probe(cctx, dsn)
		if err != nil {
			return nil, err
		}
		return map[string]any{"dsn": db.Redact(dsn), "server": info.Version, "database_exists": info.Exists, "database": info.Database, "pgvector_available": info.Vector}, nil
	})
	rpcOpen(s, "setup.connect", func(ctx context.Context, r dsnReq) (map[string]any, error) {
		dsn := resolveDSN(r)
		if a.Ready() {
			return nil, errors.New("already connected")
		}
		created := false
		if r.Create {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			c, err := db.EnsureDatabase(cctx, dsn)
			if err != nil {
				return nil, err
			}
			created = c
		}
		if err := a.Connect(context.WithoutCancel(ctx), dsn); err != nil {
			return nil, err
		}
		if err := a.Cfg.SetDSN(dsn); err != nil {
			return nil, err
		}
		a.Emit("status", a.Status(ctx))
		return map[string]any{"created": created, "pgvector": a.DB.Vector}, nil
	})

	// ── chat ──
	type chatReq struct {
		Channel string         `json:"channel"`
		Topic   string         `json:"topic"`
		Limit   int            `json:"limit"`
		Text    string         `json:"text"`
		Purge   bool           `json:"purge"`
		Images  []agent.Upload `json:"images"`
		Files   []agent.Upload `json:"files"`  // uploaded from the browser: saved in the workspace
		Paths   []string       `json:"paths"`  // picked on this Mac: read in place
		Hidden  bool           `json:"hidden"` // fs.browse: show dotfiles
		Path    string         `json:"path"`   // fs.browse
	}
	rpc(s, "chat.history", func(ctx context.Context, r chatReq) ([]agent.ChatMsg, error) {
		if r.Channel == "" {
			r.Channel = "web"
		}
		return a.Engine.ChatHistory(ctx, r.Channel, r.Topic, r.Limit)
	})
	rpc(s, "chat.send", func(ctx context.Context, r chatReq) (bool, error) {
		notes, err := s.saveUploads(r.Files)
		if err != nil {
			return false, err
		}
		pn, err := s.pathNotes(ctx, r.Paths)
		if err != nil {
			return false, err
		}
		text := strings.TrimSpace(r.Text)
		notes = append(append(notes, pn...), s.mapFolders(ctx, r.Paths)...)
		if note := attachNote(notes); note != "" {
			if text == "" {
				text = "Please have a look at this."
			}
			text += "\n\n" + note
		}
		return true, a.Engine.UserMessage(ctx, agent.UserMsg{Text: text, Images: r.Images, Channel: "web", Topic: r.Topic})
	})
	rpc(s, "fs.browse", func(ctx context.Context, r chatReq) (*dirListing, error) { return s.browse(ctx, r.Path, r.Hidden) })
	rpc(s, "chat.commands", func(ctx context.Context, _ none) ([]agent.Command, error) { return agent.Commands, nil })
	// chat.command runs a slash command; the result is posted to the conversation as a system message.
	rpc(s, "chat.command", func(ctx context.Context, r chatReq) (bool, error) {
		out, handled := a.Engine.RunCommand(ctx, "web", r.Topic, r.Text)
		if !handled {
			return false, nil
		}
		if strings.TrimSpace(out) != "" {
			a.Engine.PostSystem(ctx, "web", r.Topic, out)
		}
		return true, nil
	})
	rpc(s, "chat.stop", func(ctx context.Context, r chatReq) (bool, error) {
		return a.Engine.StopChat(agent.ChatKey("web", r.Topic)), nil
	})
	rpc(s, "chat.clear", func(ctx context.Context, r chatReq) (bool, error) {
		return true, a.Engine.ClearChat(ctx, "web", r.Topic, r.Purge)
	})
	rpc(s, "chat.compact", func(ctx context.Context, r chatReq) (bool, error) {
		return true, a.Engine.CompactChat(ctx, "web", r.Topic)
	})
	// A small, self-contained request run outside the main conversation: a fresh one-off session, no
	// automatic memory recall — cheaper than a normal chat turn for something like "create an agent that
	// uses the tracker tools" that doesn't need the user's history pulled in. Blocks for the answer.
	rpc(s, "chat.quick", func(ctx context.Context, r chatReq) (string, error) {
		return a.Engine.QuickAsk(ctx, r.Text)
	})
	rpc(s, "ask.answer", func(ctx context.Context, r struct {
		ID     int64  `json:"id"`
		Answer string `json:"answer"`
	}) (bool, error) {
		if !a.Engine.AnswerAsk(r.ID, r.Answer) {
			return false, errors.New("that question is no longer pending")
		}
		return true, nil
	})
	rpc(s, "runs.snapshot", func(ctx context.Context, _ none) (map[string]any, error) {
		return map[string]any{"runs": a.Engine.ActiveRuns(), "asks": a.Engine.PendingAsks(), "busy": a.Engine.ChatBusy("web")}, nil
	})
	// the user's web chats
	rpc(s, "chats.list", func(ctx context.Context, r struct {
		Archived bool `json:"archived"`
	}) ([]agent.Chat, error) {
		return a.Engine.Chats(ctx, r.Archived)
	})
	rpc(s, "chats.create", func(ctx context.Context, r struct {
		Title         string `json:"title"`
		ProjectBankID int64  `json:"project_bank_id"`
	}) (agent.Chat, error) {
		return a.Engine.CreateChat(ctx, r.Title, r.ProjectBankID)
	})
	rpc(s, "chats.update", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
		agent.ChatPatch
	}) (agent.Chat, error) {
		return a.Engine.UpdateChat(ctx, r.ID, r.ChatPatch)
	})
	rpc(s, "chats.merge", func(ctx context.Context, r struct {
		Sources       []int64 `json:"sources"`
		Target        int64   `json:"target"`
		ProjectBankID *int64  `json:"project_bank_id"`
	}) (*agent.MergeResult, error) {
		return a.Engine.MergeChats(ctx, r.Sources, r.Target, r.ProjectBankID)
	})
	rpc(s, "chats.unmerge", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Engine.UnmergeChat(ctx, r.ID)
	})
	rpc(s, "chats.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Engine.DeleteChat(ctx, r.ID)
	})

	// ── agents ──
	rpc(s, "agents.list", func(ctx context.Context, _ none) ([]agent.Profile, error) { return a.Profiles.List(ctx) })
	rpc(s, "agents.get", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (*agent.Profile, error) {
		return a.Profiles.GetID(ctx, r.ID)
	})
	rpc(s, "agents.save", func(ctx context.Context, r struct {
		Profile agent.Profile `json:"profile"`
		Reason  string        `json:"reason"`
	}) (*agent.Profile, error) {
		if r.Profile.ID == 0 { // new agents are never "system"
			r.Profile.System = false
			if r.Profile.Role == agent.RoleEntry {
				r.Profile.Role = agent.RoleWorker
			}
		}
		return a.Profiles.Save(ctx, r.Profile, r.Reason)
	})
	rpc(s, "agents.delete", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Profiles.Delete(ctx, r.ID)
	})
	rpc(s, "agents.history", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) ([]agent.SoulVersion, error) {
		return a.Profiles.History(ctx, r.ID)
	})
	rpc(s, "agents.proposals", func(ctx context.Context, r struct {
		Status string `json:"status"`
	}) ([]agent.Proposal, error) {
		return a.Profiles.Proposals(ctx, r.Status)
	})
	rpc(s, "agents.decide", func(ctx context.Context, r struct {
		ID     int64  `json:"id"`
		Apply  bool   `json:"apply"`
		Edited string `json:"edited"` // the reviewer's own wording, applied instead of the proposal
	}) (bool, error) {
		if err := a.Profiles.DecideEdited(ctx, r.ID, r.Apply, r.Edited); err != nil {
			return false, err
		}
		a.Emit("evolution.decided", map[string]any{"id": r.ID, "applied": r.Apply})
		return true, nil
	})
	rpc(s, "ops.dashboard", func(ctx context.Context, r struct {
		Days int `json:"days"`
	}) (*metrics.Dashboard, error) {
		tz := settings.Load(ctx, a.Settings, settings.KeyGeneral, settings.General{}).Timezone
		return a.Metrics.Dashboard(ctx, r.Days, tz)
	})
	// how much of a delegated request's total LLM time was the entry agent's own routing/synthesis versus
	// the specialist actually doing the work — see internal/metrics.DelegationReport.
	rpc(s, "ops.delegation", func(ctx context.Context, r struct {
		Days int `json:"days"`
	}) (*metrics.DelegationReport, error) {
		days := r.Days
		if days <= 0 || days > 30 {
			days = 7
		}
		return a.Metrics.DelegationReport(ctx, time.Duration(days)*24*time.Hour)
	})
	rpc(s, "today.get", func(ctx context.Context, _ none) (app.Today, error) { return a.GetToday(ctx), nil })
	// a hire made by an agent: confirm it (probation ends) or let the agent go (it is switched off)
	rpc(s, "agents.probation", func(ctx context.Context, r struct {
		ID   int64 `json:"id"`
		Keep bool  `json:"keep"`
	}) (bool, error) {
		p, err := a.Profiles.GetID(ctx, r.ID)
		if err != nil {
			return false, err
		}
		if r.Keep {
			return true, a.Profiles.SetProbation(ctx, r.ID, false)
		}
		p.Enabled = false
		_, err = a.Profiles.Save(ctx, *p, "let go during probation")
		return true, err
	})
	rpc(s, "agents.review", func(ctx context.Context, r struct {
		Name string `json:"name"`
	}) (tasks.Task, error) {
		return a.Engine.Enqueue(ctx, tasks.Task{FromKind: "system", FromName: "user", ToAgent: "Metis",
			Input: "Review the agent " + r.Name + " and propose a soul improvement if the evidence justifies it."})
	})

	// ── tasks ──
	rpc(s, "tasks.list", func(ctx context.Context, f tasks.Filter) ([]tasks.Task, error) { return a.Tasks.List(ctx, f) })
	rpc(s, "tasks.get", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (map[string]any, error) {
		t, err := a.Tasks.Get(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"task": t}
		if t.SessionID != nil {
			if ms, err := a.Sessions.Messages(ctx, *t.SessionID); err == nil {
				type m struct {
					Role, Content, Name, Provenance string
					Tainted                         bool
					ToolCalls                       any
				}
				var tr []m
				for _, x := range ms {
					c := x.Content
					if len(c) > 4000 {
						c = c[:4000] + "…"
					}
					var tc any
					if len(x.ToolCalls) > 0 {
						tc = x.ToolCalls
					}
					tr = append(tr, m{x.Role, c, x.Name, x.Provenance, x.Tainted, tc})
				}
				out["transcript"] = tr
			}
		}
		if sm, err := a.TaskSum.Get(ctx, r.ID); err == nil {
			out["summary"] = sm
		}
		return out, nil
	})
	// partial/failed tasks: acknowledge (dismiss), get an AI review with options, or resolve with one
	rpc(s, "tasks.ack", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Tasks.Acknowledge(ctx, r.ID)
	})
	rpc(s, "tasks.review", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (*agent.TaskReview, error) {
		return a.Engine.ReviewTask(ctx, r.ID)
	})
	rpc(s, "tasks.resolve", func(ctx context.Context, r struct {
		ID     int64  `json:"id"`
		Action string `json:"action"`
		Note   string `json:"note"`
	}) (*tasks.Task, error) {
		return a.Engine.ResolveTask(ctx, r.ID, r.Action, r.Note)
	})
	rpc(s, "tasks.tree", func(ctx context.Context, r struct {
		RootID int64 `json:"root_id"`
	}) ([]tasks.Task, error) {
		return a.Tasks.List(ctx, tasks.Filter{RootID: r.RootID, Limit: 200})
	})
	rpc(s, "tasks.cancel", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (bool, error) {
		return true, a.Engine.CancelTask(ctx, r.ID)
	})
	// Re-run a cancelled request as a fresh top-level task. Restricted to the user's own direct requests to
	// Atlas (never a delegated sub-task, and never something another agent started) — those are not the
	// user's to independently restart.
	rpc(s, "tasks.rerun", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (tasks.Task, error) {
		t, err := a.Tasks.Get(ctx, r.ID)
		if err != nil {
			return tasks.Task{}, err
		}
		if !t.Rerunnable() {
			if t.Status != tasks.Cancelled {
				return tasks.Task{}, fmt.Errorf("task #%d is %s, not cancelled", t.ID, t.Status)
			}
			return tasks.Task{}, errors.New("only a cancelled request you sent Atlas directly can be rerun")
		}
		return a.Engine.Enqueue(ctx, tasks.Task{FromKind: "user", FromName: "user", ToAgent: t.ToAgent, Input: t.Input, Title: t.Title})
	})
	// Hand a finished task to Daedalus to distill into a reusable skill. Any genuinely completed task
	// qualifies, including a delegated sub-task — a specialist's own procedure can be worth saving too.
	rpc(s, "tasks.save_as_routine", func(ctx context.Context, r struct {
		ID int64 `json:"id"`
	}) (tasks.Task, error) {
		t, err := a.Tasks.Get(ctx, r.ID)
		if err != nil {
			return tasks.Task{}, err
		}
		if t.Status != tasks.Done {
			return tasks.Task{}, fmt.Errorf("task #%d is %s, not done", t.ID, t.Status)
		}
		return a.Engine.Enqueue(ctx, tasks.Task{FromKind: "user", FromName: "user", ToAgent: "Daedalus",
			Input: fmt.Sprintf("Turn task #%d into a reusable skill. Start with task_transcript(id=%d).", t.ID, t.ID),
			Title: "Save as routine: " + t.Title})
	})
	rpc(s, "tasks.create", func(ctx context.Context, r struct {
		Agent string `json:"agent"`
		Input string `json:"input"`
		Title string `json:"title"`
	}) (tasks.Task, error) {
		if strings.TrimSpace(r.Agent) == "" || strings.TrimSpace(r.Input) == "" {
			return tasks.Task{}, errors.New("agent and input are required")
		}
		if _, err := a.Profiles.Get(ctx, r.Agent); err != nil {
			return tasks.Task{}, err
		}
		return a.Engine.Enqueue(ctx, tasks.Task{FromKind: "user", FromName: "user", ToAgent: r.Agent, Input: r.Input, Title: r.Title})
	})
	rpc(s, "tasks.prune", func(ctx context.Context, r struct {
		Days int `json:"days"`
	}) (int, error) {
		if r.Days <= 0 {
			r.Days = 7
		}
		return a.Tasks.Prune(ctx, time.Duration(r.Days)*24*time.Hour)
	})
}
