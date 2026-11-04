package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"prism/internal/agent"
	"prism/internal/llm"
	"prism/internal/tasks"
	"prism/internal/testutil"
	"prism/internal/tools"
)

func TestCronPreviewReportsNextRunsAndAmbiguousDayFields(t *testing.T) {
	s, _ := setup(t)
	p, err := s.PreviewCron(context.Background(), "0 12 20,21,22 * 1")
	if err != nil || len(p.Next) != 3 || !strings.Contains(p.Warning, "EITHER") {
		t.Fatalf("preview: %+v err=%v", p, err)
	}
	if !p.Next[0].Before(p.Next[1]) || !p.Next[1].Before(p.Next[2]) {
		t.Fatalf("dates not increasing: %+v", p.Next)
	}
}

func TestScheduleListSurfacesLegacyProblems(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	_, err := s.DB.Exec(ctx, `INSERT INTO crons(name,agent,expr,prompt,enabled,system) VALUES('legacy','missing agent','0 12 20,21,22 * 1','check',true,false)`)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := s.Crons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == "legacy" {
			if len(c.Issues) != 2 {
				t.Fatalf("issues: %+v", c.Issues)
			}
			return
		}
	}
	t.Fatal("legacy schedule not found")
}

func TestNewCronDefaultsToAtlasAndKeepsExplicitAgent(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Engine.Profiles.Save(ctx, agent.Profile{Name: "Scout", Role: agent.RoleWorker, Enabled: true}, "test"); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(s.DB)
	s.RegisterTools(reg)
	create, _ := reg.Get("cron_create")
	run := func(args map[string]any) {
		t.Helper()
		b, _ := json.Marshal(args)
		if _, err := create.Run(ctx, &tools.Env{Agent: "Scout"}, b); err != nil {
			t.Fatal(err)
		}
	}
	run(map[string]any{"name": "default", "expr": "@daily", "prompt": "Check this"})
	run(map[string]any{"name": "pinned", "expr": "@daily", "prompt": "Check that", "agent": "Scout"})
	uiID, err := s.SaveCron(ctx, Cron{Name: "ui default", Expr: "@daily", Prompt: "Check a third item", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := s.Crons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		want := map[string]string{"default": "Atlas", "pinned": "Scout", "ui default": "Atlas"}[c.Name]
		if want == "" {
			continue
		}
		if c.Agent != want {
			t.Fatalf("%s agent = %q, want %q", c.Name, c.Agent, want)
		}
		if err := s.RunCronNow(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	queued, err := s.Engine.Tasks.List(ctx, tasks.Filter{Agent: "Atlas"})
	if err != nil {
		t.Fatal(err)
	}
	var uiTask *tasks.Task
	for i := range queued {
		if queued[i].FromName == "ui default" {
			uiTask = &queued[i]
		}
	}
	if uiTask == nil || !strings.Contains(uiTask.Input, "watch_stop") || !strings.Contains(uiTask.Input, "cron:") {
		t.Fatalf("Atlas task missing completion instruction for cron %d: %+v", uiID, uiTask)
	}
	pinned, err := s.Engine.Tasks.List(ctx, tasks.Filter{Agent: "Scout"})
	if err != nil || len(pinned) != 1 || pinned[0].FromName != "pinned" {
		t.Fatalf("pinned dispatch: %+v %v", pinned, err)
	}
}

func TestAtlasCanFinishItsCompletedScheduledJob(t *testing.T) {
	s, _, fake := setupFake(t)
	ctx := context.Background()
	s.RegisterTools(s.Engine.Tools)
	s.Engine.RegisterTools(s.Engine.Tools)
	id, err := s.SaveCron(ctx, Cron{Name: "one-shot completion", Expr: "@daily", Prompt: "Finish after the event occurs", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunCronNow(ctx, id); err != nil {
		t.Fatal(err)
	}
	task, err := s.Engine.Tasks.ClaimNext(ctx)
	if err != nil || task == nil {
		t.Fatalf("claim: %+v %v", task, err)
	}
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		messages := req["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		if last["role"] == "tool" {
			return testutil.Reply{Content: "NO_REPLY"}
		}
		args, _ := json.Marshal(map[string]any{"refs": []string{fmt.Sprintf("cron:%d", id)}, "note": "event completed"})
		return testutil.Reply{Tools: []llm.ToolCall{{ID: "done", Name: "watch_stop", Arguments: string(args)}}}
	}
	out := s.Engine.RunTask(ctx, *task, agent.TaskOpts{})
	var enabled bool
	var note string
	if err := s.DB.QueryRow(ctx, `SELECT enabled,finished_note FROM crons WHERE id=$1`, id).Scan(&enabled, &note); err != nil {
		t.Fatal(err)
	}
	if out.Status != tasks.Done || enabled || note != "event completed" {
		t.Fatalf("job completion: task=%+v enabled=%v note=%q", out, enabled, note)
	}
}

func TestIntentDefaultsToAtlasButWatchesStayDirect(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Engine.Profiles.Save(ctx, agent.Profile{Name: "Scout", Role: agent.RoleWorker, Enabled: true}, "test"); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(s.DB)
	s.RegisterTools(reg)
	create, _ := reg.Get("intent_create")
	at := time.Now().Add(-time.Minute).Format(time.RFC3339)
	for _, args := range []map[string]any{
		{"description": "default intent", "kind": "time", "at": at},
		{"description": "pinned intent", "kind": "time", "at": at, "agent": "Scout"},
		{"description": "direct watch", "kind": "time", "at": at, "type": "watch"},
	} {
		b, _ := json.Marshal(args)
		if _, err := create.Run(ctx, &tools.Env{Agent: "Scout"}, b); err != nil {
			t.Fatal(err)
		}
	}
	intents, err := s.Intents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range intents {
		want := map[string]string{"default intent": "Atlas", "pinned intent": "Scout", "direct watch": "Scout"}[i.Description]
		if want == "" {
			continue
		}
		if i.Owner != want {
			t.Fatalf("%s owner = %q, want %q", i.Description, i.Owner, want)
		}
		s.check(ctx, i)
	}
	for _, name := range []string{"Atlas", "Scout"} {
		ts, err := s.Engine.Tasks.List(ctx, tasks.Filter{Agent: name})
		if err != nil || len(ts) != 1 || ts[0].FromKind != "intent" || !strings.Contains(ts[0].Input, "Evidence:") {
			t.Fatalf("%s action-intent dispatch (watch must not enqueue a task): %+v %v", name, ts, err)
		}
	}
}
