package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/cron"
	"prism/internal/tools"
)

var predicateProps = []tools.Prop{
	tools.Enum("kind", "what to check", "time", "http", "file", "process", "rss", "llm", "download", "tool"),
	tools.Str("at", "kind=time: when to fire, RFC3339 with timezone (use the clock tool to compute)"),
	tools.Str("url", "kind=http|rss|llm: page or feed URL"),
	tools.Int("status", "kind=http: expected HTTP status"),
	tools.Str("contains", "kind=http|rss: text that must appear (rss: in the item title)"),
	tools.Str("not_contains", "kind=http: text that must be gone"),
	tools.Bool("changed", "kind=http: fire when the page content changes"),
	tools.Str("path", "kind=file: file path"),
	tools.Int("stable_s", "kind=file: fire when size is unchanged for this many seconds (finished copy/download)"),
	tools.Int("pid", "kind=process: process id"),
	tools.Str("pattern", "kind=process: command-line pattern (pgrep -f)"),
	tools.Str("question", "kind=llm: the condition to judge, e.g. 'Is version 2.0 released?'"),
	tools.Str("query", "kind=llm: web search query used as evidence when no url is given"),
	tools.Int("download_id", "kind=download: id from download_start"),
	tools.Str("tool", "kind=tool: name of a READ-ONLY tool to poll — an MCP tool of the service being watched (e.g. mcp__downloadstation__list_tasks) is the right way to watch that service; do not guess its web address with curl"),
	tools.Str("args", "kind=tool: JSON object with the tool's arguments, e.g. {\"id\":\"dbid_764\"}"),
	tools.Str("field", "kind=tool: dot path into the tool's JSON output to judge, e.g. data.task.status ('*' = every array element)"),
	tools.Str("expect", "kind=tool: regex on that value (or the whole output) meaning 'done', e.g. finished|seeding|100"),
}

// RegisterTools installs intent, watch, cron and briefing tools.
func (s *Service) RegisterTools(reg *tools.Registry) {
	reg.Register(
		&tools.Tool{
			Name: "intent_create", Category: "autonomy", Risk: tools.RiskWrite, Auto: true,
			Description: "Register a standing intent (prospective memory): 'tell me when X happens'. It is checked on a cadence; when the predicate holds you are woken with the evidence and decide what the user should hear. Set repeat for recurring triggers. Use type=watch for pure progress tracking (the user is notified directly, no model call).",
			Params: tools.Obj("description,kind", append([]tools.Prop{
				tools.Str("description", "what this intent is for, in words the user would use"),
				tools.Enum("type", "intent (wake me to decide) | watch (just notify)", "intent", "watch"),
				tools.Int("cadence_s", "seconds between checks (default 300, min 30)"),
				tools.Bool("repeat", "keep watching after it fires"),
			}, predicateProps...)...),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				var a struct {
					Description string `json:"description"`
					Type        string `json:"type"`
					CadenceS    int    `json:"cadence_s"`
					Repeat      bool   `json:"repeat"`
					Predicate
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", fmt.Errorf("invalid arguments: %w", err)
				}
				if a.Kind == "shell" {
					return "", errors.New("shell checks need the watch_command tool")
				}
				if a.Type == "watch" {
					if ex, err := s.watchGuard(ctx, env.Agent, a.Predicate); err != nil {
						return "", err
					} else if ex != 0 {
						return fmt.Sprintf("Already watching this: watch #%d.", ex), nil
					}
				}
				id, err := s.CreateIntent(ctx, Intent{Owner: env.Agent, Description: a.Description, Type: a.Type, CadenceS: a.CadenceS, Repeat: a.Repeat, Notify: true}, a.Predicate)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Intent #%d registered (%s check every %ds).", id, a.Kind, max(a.CadenceS, 30)), nil
			},
		},
		&tools.Tool{
			Name: "watch_command", Category: "autonomy", Risk: tools.RiskExec,
			Description: "Watch a long-running thing by polling a shell command (e.g. an rsync log's last percentage). Never guess the address of a service with curl: if the service has an MCP tool, watch it with monitor_start kind=tool instead. The watch is complete when the output matches the 'expect' regex (or, without it, when the command exits 0). Progress is recorded and the user is notified on completion. The command runs unattended on every poll — keep it read-only.",
			Params: tools.Obj("description,command", tools.Str("description", "what is being watched"), tools.Str("command", "read-only shell command to poll"),
				tools.Str("expect", "regex on stdout meaning 'done', e.g. 100%"), tools.Int("cadence_s", "poll interval seconds (default 30, min 15)"),
				tools.Int("for_minutes", "give up after this many minutes (default 120, max 1440); a percentage or n/m in the output gives the user a finish-time estimate")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Description string `json:"description"`
					Command     string `json:"command"`
					Expect      string `json:"expect"`
					CadenceS    int    `json:"cadence_s"`
					ForMinutes  int    `json:"for_minutes"`
				}](raw)
				if err != nil {
					return "", err
				}
				if a.CadenceS == 0 {
					a.CadenceS = 30
				}
				pred := Predicate{Kind: "shell", Command: a.Command, Expect: a.Expect}
				if ex, err := s.watchGuard(ctx, env.Agent, pred); err != nil {
					return "", err
				} else if ex != 0 {
					return fmt.Sprintf("Already watching this: watch #%d.", ex), nil
				}
				id, err := s.CreateIntent(ctx, Intent{Owner: env.Agent, Type: "watch", Description: a.Description, CadenceS: a.CadenceS, Notify: true, ExpiresAt: budget(a.ForMinutes, 120)}, pred)
				return fmt.Sprintf("Watch #%d started.", id), err
			},
		},
		&tools.Tool{
			Name: "intent_list", Category: "autonomy", Risk: tools.RiskRead,
			Description: "List standing intents and watches with their status and last progress.",
			Params:      tools.Obj("", tools.Enum("status", "filter", "active", "fired", "cancelled", "error")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, _ := tools.Decode[struct{ Status string }](raw)
				is, err := s.Intents(ctx, a.Status)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				for _, i := range is {
					fmt.Fprintf(&sb, "#%d [%s/%s] %s (owner %s, every %ds) — %s %s%s\n", i.ID, i.Type, i.Status, i.Description, i.Owner, i.CadenceS, i.Progress, i.LastError, monitorNote(i))
				}
				if sb.Len() == 0 {
					return "None.", nil
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "intent_cancel", Category: "autonomy", Risk: tools.RiskWrite, Auto: true,
			Description: "Cancel an intent or watch by id.",
			Params:      tools.Obj("id", tools.Int("id", "intent id")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ ID int64 }](raw)
				if err != nil {
					return "", err
				}
				return "cancelled", s.UpdateIntent(ctx, a.ID, "cancelled", nil)
			},
		},
		&tools.Tool{
			Name: "cron_create", Category: "autonomy", Risk: tools.RiskWrite,
			Description: "Schedule a recurring job: at each firing a fresh session of the agent runs the standing prompt. Cron syntax 'min hour day month weekday' (e.g. '0 8 * * 1-5'), or @daily, @hourly, '@every 2h'. Times are in the user's timezone.",
			Params: tools.Obj("name,expr,prompt", tools.Str("name", "short label"), tools.Str("expr", "cron expression"),
				tools.Str("prompt", "standing prompt, self-contained; tell the agent to reply NO_REPLY when there is nothing to report"),
				tools.Str("agent", "agent to run it (default: you)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Name, Expr, Prompt, Agent string }](raw)
				if err != nil {
					return "", err
				}
				if a.Agent == "" {
					a.Agent = env.Agent
				}
				id, err := s.SaveCron(ctx, Cron{Name: a.Name, Agent: a.Agent, Expr: a.Expr, Prompt: a.Prompt, Enabled: true})
				if err != nil {
					return "", err
				}
				sched, _ := cron.Parse(a.Expr)
				return fmt.Sprintf("Cron #%d created; next run %s.", id, sched.Next(time.Now().In(s.loc(ctx))).Format("Mon 2006-01-02 15:04 MST")), nil
			},
		},
		&tools.Tool{
			Name: "cron_list", Category: "autonomy", Risk: tools.RiskRead,
			Description: "List scheduled jobs.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				cs, err := s.Crons(ctx)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				for _, c := range cs {
					nx := "-"
					if c.NextRun != nil {
						nx = c.NextRun.Format("2006-01-02 15:04")
					}
					fmt.Fprintf(&sb, "#%d %q [%s] agent=%s enabled=%v next=%s\n", c.ID, c.Name, c.Expr, c.Agent, c.Enabled, nx)
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "cron_delete", Category: "autonomy", Risk: tools.RiskWrite,
			Description: "Delete a scheduled job by id.",
			Params:      tools.Obj("id", tools.Int("id", "cron id")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ ID int64 }](raw)
				if err != nil {
					return "", err
				}
				return "deleted", s.DeleteCron(ctx, a.ID)
			},
		},
		&tools.Tool{
			Name: "monitor_start", Category: "autonomy", Risk: tools.RiskWrite, Auto: true,
			Description: "Start a MONITOR: a short-lived watch checked every minute or so (default 60s, min 15s) that ends by itself after a time budget (default 60 min, max 24 h) and tells the user when the condition holds. " +
				"Use it for 'keep an eye on X for the next hour' — a page changing, a download or process finishing, a file appearing, a question about a page. If the watched thing reports a percentage or n/m, the user gets a finish-time estimate. " +
				"Set announce to also report progress changes while it runs (at most every 5 minutes). Shell polling needs watch_command instead. Adjust later with monitor_adjust.",
			Params: tools.Obj("description,kind", append([]tools.Prop{
				tools.Str("description", "what is being monitored, in words the user would use"),
				tools.Int("every_s", "seconds between checks (default 60, min 15)"),
				tools.Int("for_minutes", "time budget in minutes (default 60, max 1440)"),
				tools.Bool("announce", "report progress changes to the user while running"),
			}, predicateProps...)...),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				var a struct {
					Description string `json:"description"`
					EveryS      int    `json:"every_s"`
					ForMinutes  int    `json:"for_minutes"`
					Announce    bool   `json:"announce"`
					Predicate
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", fmt.Errorf("invalid arguments: %w", err)
				}
				if a.Kind == "shell" {
					return "", errors.New("shell checks need the watch_command tool")
				}
				if a.EveryS == 0 {
					a.EveryS = 60
				}
				if ex, err := s.watchGuard(ctx, env.Agent, a.Predicate); err != nil {
					return "", err
				} else if ex != 0 {
					return fmt.Sprintf("Already watching this: monitor #%d.", ex), nil
				}
				id, err := s.CreateIntent(ctx, Intent{Owner: env.Agent, Type: "watch", Description: a.Description, CadenceS: max(a.EveryS, 15), Notify: true, Announce: a.Announce,
					ExpiresAt: budget(a.ForMinutes, 60)}, a.Predicate)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Monitor #%d started: %s check every %ds for %d min.", id, a.Kind, max(a.EveryS, 15), clampMinutes(a.ForMinutes, 60)), nil
			},
		},
		&tools.Tool{
			Name: "monitor_adjust", Category: "autonomy", Risk: tools.RiskWrite, Auto: true,
			Description: "Change a running monitor: check more or less often (every_s, 15–86400), give it more time or less (extend_minutes; negative shortens, and an expired monitor with a positive value wakes up again), or switch progress announcements (announce).",
			Params: tools.Obj("id", tools.Int("id", "monitor (intent) id"), tools.Int("every_s", "new interval between checks, seconds"),
				tools.Int("extend_minutes", "minutes to add to (or, if negative, remove from) its time budget"), tools.Bool("announce", "report progress changes while running")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					ID            int64 `json:"id"`
					EveryS        int   `json:"every_s"`
					ExtendMinutes int   `json:"extend_minutes"`
					Announce      *bool `json:"announce"`
				}](raw)
				if err != nil {
					return "", err
				}
				if a.EveryS != 0 {
					if a.EveryS < 15 || a.EveryS > 86400 {
						return "", errors.New("every_s must be between 15 and 86400")
					}
					if err := s.UpdateIntent(ctx, a.ID, "", &a.EveryS); err != nil {
						return "", err
					}
				}
				if err := s.ExtendIntent(ctx, a.ID, a.ExtendMinutes); err != nil {
					return "", err
				}
				if a.Announce != nil {
					if err := s.SetAnnounce(ctx, a.ID, *a.Announce); err != nil {
						return "", err
					}
				}
				return "Monitor #" + fmt.Sprint(a.ID) + " adjusted.", nil
			},
		},
		&tools.Tool{
			Name: "monitor_list", Category: "autonomy", Risk: tools.RiskRead,
			Description: "List monitors (active by default) with how often they check, how much time is left, the latest progress and the estimated finish time.",
			Params:      tools.Obj("", tools.Enum("status", "filter", "active", "expired", "fired", "cancelled", "error")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, _ := tools.Decode[struct{ Status string }](raw)
				if a.Status == "" {
					a.Status = "active"
				}
				is, err := s.Intents(ctx, a.Status)
				if err != nil {
					return "", err
				}
				var sb strings.Builder
				for _, i := range is {
					if i.Type != "watch" || i.ExpiresAt == nil {
						continue
					}
					fmt.Fprintf(&sb, "#%d [%s] %s — every %ds, %s — %s%s\n", i.ID, i.Status, i.Description, i.CadenceS, timeLeft(i), firstNonEmpty(i.Progress, "no progress yet"), monitorNote(i))
				}
				if sb.Len() == 0 {
					return "No monitors.", nil
				}
				return sb.String(), nil
			},
		},
		&tools.Tool{
			Name: "briefing_add", Category: "autonomy", Risk: tools.RiskWrite, Auto: true,
			Description: "Add a briefing for the user (importance 1–5; 4+ is delivered to them immediately, lower ones wait in the briefing list). " +
				"If the user already dismissed a similar briefing this is refused unless you pass new_info stating what is materially new.",
			Params: tools.Obj("title,body", tools.Str("title", "headline"), tools.Str("body", "specific, grounded, actionable text"),
				tools.Int("importance", "1–5"), tools.Str("new_info", "only when repeating a topic the user dismissed: what changed since")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Title, Body string
					Importance  int
					NewInfo     string `json:"new_info"`
				}](raw)
				if err != nil {
					return "", err
				}
				if d := s.SimilarDismissed(ctx, a.Title, a.Body); d != nil {
					if strings.TrimSpace(a.NewInfo) == "" {
						return "", fmt.Errorf("the user already dismissed a similar briefing (#%d “%s”). Do not repeat it — unless something is materially new; then call again with new_info saying what changed", d.ID, d.Title)
					}
					a.Body = strings.TrimSpace(a.Body) + "\n\n(New since the dismissed briefing #" + fmt.Sprint(d.ID) + ": " + strings.TrimSpace(a.NewInfo) + ")"
				}
				id, err := s.AddBriefing(ctx, env.Agent, a.Title, a.Body, a.Importance)
				return fmt.Sprintf("Briefing #%d added.", id), err
			},
		},
	)
}
