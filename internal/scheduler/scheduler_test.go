package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/agent"
	"prism/internal/memory"
	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/testutil"
	"prism/internal/tools"
	"prism/internal/tracker"
)

type rec struct {
	mu sync.Mutex
	ev []string
}

func (r *rec) emit(typ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := data.(agent.ChatMsg); ok {
		typ += ":" + m.Text
	}
	r.ev = append(r.ev, typ)
}
func (r *rec) has(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.ev {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func setup(t *testing.T) (*Service, *rec) {
	s, rc, _ := setupFake(t)
	return s, rc
}

func setupFake(t *testing.T) (*Service, *rec, *testutil.FakeLLM) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fake)
	reg := tools.NewRegistry(d.Pool)
	rc := &rec{}
	e := agent.NewEngine(agent.Deps{DB: d.Pool, LLM: r, Tools: reg, Profiles: agent.NewProfileStore(d.Pool), Sessions: agent.NewSessionStore(d.Pool),
		Tasks: tasks.NewStore(d.Pool), Memory: memory.New(d.Pool, r, st), Settings: st, Emit: rc.emit})
	if err := e.Profiles.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = st.Set(context.Background(), settings.KeyAutonomy, settings.Autonomy{Enabled: true, DreamEnabled: true})
	return &Service{DB: d.Pool, Engine: e, Settings: st, Emit: rc.emit, Env: Env{LLM: r}}, rc, fake
}

func TestCronEnqueuesAndWatchesNotify(t *testing.T) {
	s, rc := setup(t)
	ctx := context.Background()
	if err := s.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	cs, _ := s.Crons(ctx)
	if len(cs) != 4 {
		t.Fatalf("expected 4 default crons, got %d", len(cs))
	}
	// make the memory cron due
	if _, err := s.DB.Exec(ctx, `UPDATE crons SET next_run = now() - interval '1 minute' WHERE agent='Mnemosyne'`); err != nil {
		t.Fatal(err)
	}
	s.tick(ctx)
	ts, _ := s.Engine.Tasks.List(ctx, tasks.Filter{Agent: "Mnemosyne"})
	if len(ts) != 1 || ts[0].FromKind != "cron" || ts[0].Status != tasks.Queued {
		t.Fatalf("cron did not enqueue: %+v", ts)
	}
	cs, _ = s.Crons(ctx)
	for _, c := range cs {
		if c.Agent == "Mnemosyne" && (c.NextRun == nil || !c.NextRun.After(time.Now())) {
			t.Fatalf("next_run not advanced: %+v", c)
		}
	}
	// disabled dreaming suppresses Oneiros
	_ = s.Settings.Set(ctx, settings.KeyAutonomy, settings.Autonomy{Enabled: true, DreamEnabled: false})
	_, _ = s.DB.Exec(ctx, `UPDATE crons SET next_run = now() - interval '1 minute' WHERE agent='Oneiros'`)
	s.tick(ctx)
	if ts, _ := s.Engine.Tasks.List(ctx, tasks.Filter{Agent: "Oneiros"}); len(ts) != 0 {
		t.Fatal("dream cron fired although dreaming is disabled")
	}

	// time watch fires and notifies directly
	at := time.Now().Add(-time.Minute).Format(time.RFC3339)
	id, err := s.CreateIntent(ctx, Intent{Owner: "Atlas", Type: "watch", Description: "coffee break", CadenceS: 30, Notify: true}, Predicate{Kind: "time", At: at})
	if err != nil {
		t.Fatal(err)
	}
	s.tick(ctx)
	is, _ := s.Intents(ctx, "")
	var got Intent
	for _, i := range is {
		if i.ID == id {
			got = i
		}
	}
	if got.Status != "fired" || !rc.has("coffee break") {
		t.Fatalf("watch: %+v events=%v", got, rc.ev)
	}

	// file stability: growing → waiting, then stable → fires
	f := filepath.Join(t.TempDir(), "big.bin")
	_ = os.WriteFile(f, []byte("aaaa"), 0o644)
	p := &Predicate{Kind: "file", Path: f, StableS: 1}
	env := Env{}
	r1, _ := p.Eval(context.Background(), env)
	if r1.Fired {
		t.Fatal("first observation must not fire")
	}
	time.Sleep(1100 * time.Millisecond)
	r2, _ := p.Eval(context.Background(), env)
	if !r2.Fired {
		t.Fatalf("stable file should fire: %+v", r2)
	}

	// shell predicate with expect regex
	sp := &Predicate{Kind: "shell", Command: "printf '10%%\\r55%%\\r100%%'", Expect: `100%`}
	if r, err := sp.Eval(context.Background(), env); err != nil || !r.Fired {
		t.Fatalf("shell predicate: %+v %v", r, err)
	}
	// validation
	if err := (&Predicate{Kind: "time", At: "tomorrow"}).Validate(); err == nil {
		t.Fatal("bad time must be rejected")
	}
}

func TestBriefingLifecycle(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	id, err := s.AddBriefing(ctx, "Oneiros", "Weekly digest", "Here is what happened.", 2)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Briefing(ctx, id)
	if err != nil || b.Title != "Weekly digest" || b.Status != "new" || b.Agent != "Oneiros" {
		t.Fatalf("briefing: %+v err=%v", b, err)
	}
	if _, err := s.Briefing(ctx, id+9999); err == nil {
		t.Fatal("missing briefing accepted")
	}
	if err := s.SetBriefingStatus(ctx, id, "dismissed"); err != nil {
		t.Fatal(err)
	}
	if b, _ = s.Briefing(ctx, id); b.Status != "dismissed" {
		t.Fatalf("status not updated: %+v", b)
	}
	// importance >= 4 is delivered immediately (not left "new")
	hi, err := s.AddBriefing(ctx, "Oneiros", "Urgent", "act now", 5)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ = s.Briefing(ctx, hi); b.Status != "delivered" {
		t.Fatalf("high-importance briefing should auto-deliver: %+v", b)
	}
}

// A cron/intent whose agent got renamed or replaced (typically by "Regenerate profiles") used to fail every
// fire with a buried "agent not found" task error. It should instead redispatch to the closest current
// match and heal its own stored agent name, so it doesn't repeat the same failure forever.
func TestCronRedispatchesAndHealsWhenAgentIsGone(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Engine.Profiles.Save(ctx, agent.Profile{Name: "Builder", Group: "Construction", Description: "Builds things", Role: agent.RoleWorker, Enabled: true}, "test"); err != nil {
		t.Fatal(err)
	}
	id, err := s.SaveCron(ctx, Cron{Name: "nightly build", Agent: "old_builder", Expr: "@daily", Prompt: "build it", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunCronNow(ctx, id); err != nil {
		t.Fatalf("cron should have redispatched, not failed: %v", err)
	}
	ts, _ := s.Engine.Tasks.List(ctx, tasks.Filter{Agent: "Builder"})
	if len(ts) != 1 {
		t.Fatalf("expected the task to land on Builder: %+v", ts)
	}
	cs, _ := s.Crons(ctx)
	var healed Cron
	for _, c := range cs {
		if c.ID == id {
			healed = c
		}
	}
	if healed.Agent != "Builder" {
		t.Fatalf("cron's stored agent should have healed to Builder: %+v", healed)
	}
}

// When nothing remotely matches the missing agent, firing must not keep guaranteeing a failed task forever:
// the cron is disabled instead, with a clear notice, rather than left silently retrying every tick.
func TestCronDisabledWhenNoReplacementFits(t *testing.T) {
	s, rc := setup(t)
	ctx := context.Background()
	id, err := s.SaveCron(ctx, Cron{Name: "orphaned", Agent: "zzz_totally_unrelated_zzz", Expr: "@daily", Prompt: "do the thing", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunCronNow(ctx, id); err == nil {
		t.Fatal("expected an error when no replacement agent can be found")
	}
	cs, _ := s.Crons(ctx)
	var healed Cron
	for _, c := range cs {
		if c.ID == id {
			healed = c
		}
	}
	if healed.Enabled {
		t.Fatalf("an unresolvable cron should be disabled, not left firing forever: %+v", healed)
	}
	if !rc.has("cron.fired") {
		t.Fatalf("expected the UI to be told about the change: %v", rc.ev)
	}
}

// Same self-healing for intents/watches (Intent.Owner), whose dispatch path is separate from crons.
func TestIntentRedispatchesWhenAgentIsGone(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Engine.Profiles.Save(ctx, agent.Profile{Name: "Builder", Group: "Construction", Description: "Builds things", Role: agent.RoleWorker, Enabled: true}, "test"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).Format(time.RFC3339)
	id, err := s.CreateIntent(ctx, Intent{Owner: "old_builder", Type: "watch", Description: "build finished", CadenceS: 30, Notify: true}, Predicate{Kind: "time", At: at})
	if err != nil {
		t.Fatal(err)
	}
	s.tick(ctx)
	is, _ := s.Intents(ctx, "")
	var got Intent
	for _, i := range is {
		if i.ID == id {
			got = i
		}
	}
	if got.Owner != "Builder" || got.Status != "fired" {
		t.Fatalf("intent should have redispatched to Builder and fired: %+v", got)
	}
}

// The heartbeat is a system cron kept in line with a checklist setting: created on first save, rescheduled, disabled with the switch,
// and each beat is told what the previous ones already reported.
func TestHeartbeatKeepsItsCronInLineWithTheChecklist(t *testing.T) {
	ctx := context.Background()
	svc, _ := setup(t)
	if err := svc.SaveHeartbeat(ctx, settings.Heartbeat{Enabled: true, EveryMin: 15, Checklist: "- is the NAS reachable?\n- new episode of Foo?"}); err != nil {
		t.Fatal(err)
	}
	var expr, prompt, topic string
	var enabled, system bool
	read := func() {
		if err := svc.DB.QueryRow(ctx, `SELECT expr, prompt, enabled, system, topic FROM crons WHERE name=$1`, HeartbeatName).Scan(&expr, &prompt, &enabled, &system, &topic); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if expr != "*/15 * * * *" || !enabled || !system || topic != "" || !strings.Contains(prompt, "new episode of Foo") || !strings.Contains(prompt, "NO_REPLY") {
		t.Fatalf("the cron must mirror the setting: %q enabled=%v system=%v topic=%q", expr, enabled, system, topic)
	}
	if err := svc.SaveHeartbeat(ctx, settings.Heartbeat{Enabled: true, EveryMin: 120, Checklist: "- one item"}); err != nil {
		t.Fatal(err)
	}
	read()
	if expr != "0 */2 * * *" || strings.Contains(prompt, "Foo") {
		t.Fatalf("a changed interval and checklist are applied: %q", expr)
	}
	if err := svc.SaveHeartbeat(ctx, settings.Heartbeat{Enabled: true, Checklist: "  "}); err == nil {
		t.Fatal("an empty checklist cannot be enabled")
	}
	if err := svc.SaveHeartbeat(ctx, settings.Heartbeat{Enabled: false, EveryMin: 30, Checklist: "- one item"}); err != nil {
		t.Fatal(err)
	}
	read()
	if enabled {
		t.Fatal("switching it off disables the cron")
	}
	if heartbeatExpr(0) != "*/30 * * * *" || heartbeatExpr(60) != "0 * * * *" || heartbeatExpr(360) != "0 */6 * * *" {
		t.Fatal("interval to cron expression")
	}
	// what earlier beats reported is handed to the next one; silent beats are not
	mk := func(result string) {
		_, _ = svc.DB.Exec(ctx, `INSERT INTO tasks(from_kind,from_name,to_agent,title,input,status,result) VALUES('cron',$1,'Atlas','Heartbeat','x','done',$2)`, HeartbeatName, result)
	}
	mk("NO_REPLY")
	mk("The NAS has been unreachable since 14:10.")
	recent := svc.heartbeatRecent(ctx)
	if !strings.Contains(recent, "unreachable since 14:10") || strings.Contains(recent, "NO_REPLY") {
		t.Fatalf("recent news only: %q", recent)
	}
	st := svc.HeartbeatState(ctx)
	if st["every_min"] != 30 || len(st["beats"].([]HeartbeatBeat)) != 2 {
		t.Fatalf("state: %+v", st)
	}
}

// A question briefing closes when its question is settled anywhere (answered on the Memory page, retired by analysis), not
// only when it is replied to in the briefing itself.
func TestQuestionBriefingClosesWhenTheInsightIsSettled(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	var bank int64
	if err := s.DB.QueryRow(ctx, `INSERT INTO memory_banks (kind, name, owner) VALUES ('project','qtest','') RETURNING id`).Scan(&bank); err != nil {
		t.Fatal(err)
	}
	q := "Is the alert threshold intentional?"
	var fact int64
	if err := s.DB.QueryRow(ctx, `INSERT INTO memory_facts (bank_id, text, kind, tags) VALUES ($1,$2,'conclusion','{insight,question}') RETURNING id`, bank, q).Scan(&fact); err != nil {
		t.Fatalf("cannot seed an insight: %v", err)
	}
	id, err := s.AddQuestion(ctx, "Mnemosyne", "Question — "+q, q+"\n\nBased on:\n- something", 3)
	if err != nil {
		t.Fatal(err)
	}
	open := func() bool {
		bs, _ := s.Briefings(ctx, "")
		for _, b := range bs {
			if b.ID == id {
				return b.Status != "dismissed"
			}
		}
		return false
	}
	if !open() {
		t.Fatal("the question is open while its insight is")
	}
	_, _ = s.DB.Exec(ctx, `UPDATE memory_facts SET valid_to=now() WHERE id=$1`, fact)
	if open() {
		t.Fatal("answering the insight elsewhere must close the briefing")
	}
}

func TestCronUpdateFinishAndDuplicateGuard(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	if err := s.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(s.DB)
	s.RegisterTools(reg)
	run := func(agent, name string, args map[string]any) (string, error) {
		tl, ok := reg.Get(name)
		if !ok {
			t.Fatalf("no tool %s", name)
		}
		b, _ := json.Marshal(args)
		return tl.Run(ctx, &tools.Env{Agent: agent}, b)
	}
	if _, err := run("Atlas", "cron_create", map[string]any{"name": "avito x", "expr": "@hourly", "prompt": "Check Avito price for X.", "agent": "Atlas"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run("Atlas", "cron_create", map[string]any{"name": "avito x again", "expr": "@daily", "prompt": "check  avito price for x.", "agent": "Atlas"}); err == nil || !strings.Contains(err.Error(), "cron_update") {
		t.Fatalf("an identical job must be refused with a pointer to cron_update: %v", err)
	}
	if _, err := run("Atlas", "cron_create", map[string]any{"name": "Avito X", "expr": "@daily", "prompt": "A different prompt entirely.", "agent": "Atlas"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a second job with an existing name is refused (runs are attributed by name): %v", err)
	}
	cs, _ := s.Crons(ctx)
	var id int64
	for _, c := range cs {
		if c.Name == "avito x" {
			id = c.ID
		}
	}
	if _, err := run("Atlas", "cron_update", map[string]any{"id": id, "prompt": "Check Avito prices for X, Y and Z.", "name": "avito prices"}); err != nil {
		t.Fatal(err)
	}
	cs, _ = s.Crons(ctx)
	found := false
	for _, c := range cs {
		if c.ID == id && c.Name == "avito prices" && strings.Contains(c.Prompt, "X, Y and Z") && c.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatalf("the job is changed in place: %+v", cs)
	}
	if _, err := run("Scout", "cron_update", map[string]any{"id": id, "enabled": false}); err == nil {
		t.Fatal("another agent may not change a job it does not run")
	}
	var sysID int64
	_ = s.DB.QueryRow(ctx, `SELECT id FROM crons WHERE system LIMIT 1`).Scan(&sysID)
	if _, err := run("Atlas", "cron_update", map[string]any{"id": sysID, "enabled": false}); err == nil {
		t.Fatal("built-in jobs are not changed by agents")
	}
	if _, err := run("Atlas", "cron_finish", map[string]any{"id": id}); err == nil {
		t.Fatal("finishing needs a reason")
	}
	if _, err := run("Atlas", "cron_finish", map[string]any{"id": id, "note": "merged into the tracker"}); err != nil {
		t.Fatal(err)
	}
	cs, _ = s.Crons(ctx)
	for _, c := range cs {
		if c.ID == id && (c.Enabled || c.FinishedAt == nil || c.FinishedNote != "merged into the tracker") {
			t.Fatalf("finished means off, kept, with the reason: %+v", c)
		}
	}
	if out, _ := run("Atlas", "cron_list", nil); !strings.Contains(out, "FINISHED") || !strings.Contains(out, "prompt:") {
		t.Fatalf("the list shows finished jobs and prompts: %q", out)
	}
	if _, err := s.UpdateCron(ctx, id, CronPatch{Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	cs, _ = s.Crons(ctx)
	for _, c := range cs {
		if c.ID == id && c.FinishedAt != nil {
			t.Fatal("switching a finished job on again clears 'finished'")
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestJobReviewMergesDuplicatesAndRewritesFailingJobsUnderTheDial(t *testing.T) {
	s, _, fake := setupFake(t)
	ctx := context.Background()
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		switch {
		case strings.Contains(sys, "ONE kind of check"):
			return testutil.Reply{Content: `{"same":true,"prompt":"Check Avito prices for X (under 700000), Y (under 800000) and Z (under 900000); reply NO_REPLY when none is under its limit.","expr":"@hourly","why":"the same check over three items"}`}
		case strings.Contains(sys, "review one scheduled job"):
			return testutil.Reply{Content: `{"revise":true,"prompt":"Check the Haunted Hotel torrent on the mobile site; reply NO_REPLY when nothing new.","why":"the desktop site blocked three runs"}`}
		}
		return testutil.Reply{Content: "ok"}
	}
	mk := func(name, prompt string) int64 {
		id, err := s.SaveCron(ctx, Cron{Name: name, Agent: "Atlas", Expr: "@daily", Prompt: prompt, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a := mk("avito x", "Check Avito price for the FEVM X box and tell me if it is under 700000. Reply NO_REPLY if nothing.")
	b := mk("avito y", "Check Avito price for the FEVM Y box and tell me if it is under 800000. Reply NO_REPLY if nothing.")
	c := mk("avito z", "Check Avito price for the FEVM Z box and tell me if it is under 900000. Reply NO_REPLY if nothing.")
	w := mk("film", "Watch rutracker for the Haunted Hotel torrent and download it when it appears.")
	for i := 0; i < 3; i++ {
		_, _ = s.DB.Exec(ctx, `INSERT INTO tasks (from_kind, from_name, to_agent, title, input, status, error, depth, root_id) VALUES ('cron','film','Atlas','film','x','failed','blocked by the desktop site',0,0)`)
	}
	n, err := s.ReviewJobs(ctx)
	if err != nil || n != 2 {
		t.Fatalf("a merge and a rewrite expected: %d %v", n, err)
	}
	ps, _ := s.JobProposals(ctx, "pending")
	if len(ps) != 2 {
		t.Fatalf("safe (default): both wait: %+v", ps)
	}
	var merge JobProposal
	for _, p := range ps {
		if p.Kind == "merge" {
			merge = p
		}
	}
	if len(merge.CronIDs) != 3 || !strings.Contains(merge.NewPrompt, "Y (under 800000)") {
		t.Fatalf("the three similar jobs are one proposal: %+v", merge)
	}
	if err := s.ApplyJobProposal(ctx, merge.ID, false); err != nil {
		t.Fatal(err)
	}
	cs, _ := s.Crons(ctx)
	live, finished := 0, 0
	for _, x := range cs {
		if x.ID == a || x.ID == b || x.ID == c {
			if x.FinishedAt != nil {
				finished++
			} else if x.Enabled && strings.Contains(x.Prompt, "Y (under 800000)") && x.Expr == "@hourly" {
				live++
			}
		}
	}
	if live != 1 || finished != 2 {
		t.Fatalf("one job survives with the merged prompt, two are finished: live=%d finished=%d", live, finished)
	}
	if err := s.UndoJobProposal(ctx, merge.ID); err != nil {
		t.Fatal(err)
	}
	cs, _ = s.Crons(ctx)
	for _, x := range cs {
		if (x.ID == a || x.ID == b || x.ID == c) && (!x.Enabled || x.FinishedAt != nil || strings.Contains(x.Prompt, "Y (under 800000)")) {
			t.Fatalf("undo puts all three back: %+v", x)
		}
	}
	// guarded: a small rewrite goes through, and armed applies everything
	_ = s.Settings.Set(ctx, settings.KeyAutonomy, settings.Autonomy{Enabled: true, JobsMode: "armed"})
	_, _ = s.DB.Exec(ctx, `UPDATE job_proposals SET status='dismissed', decided_at=now()-interval '30 days' WHERE status='pending'`)
	if n, _ := s.ReviewJobs(ctx); n < 1 {
		t.Fatal("armed: the review proposes again")
	}
	cs, _ = s.Crons(ctx)
	for _, x := range cs {
		if x.ID == w && !strings.Contains(x.Prompt, "mobile site") {
			t.Fatalf("armed applies the rewrite by itself: %q", x.Prompt)
		}
	}
}

func TestJobReviewTurnsAGroupOfChecksIntoATracker(t *testing.T) {
	s, _, fake := setupFake(t)
	ctx := context.Background()
	s.Trackers = &tracker.Service{DB: s.DB}
	if _, err := s.Engine.Profiles.Save(ctx, agent.Profile{Name: "Organizer", Group: "Home", Description: "keeps track", Soul: "You keep trackers.", Tools: []string{"tracker_snapshot", "tracker_changes", "tracker_rows"}, Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	fake.Handler = func(req map[string]any, call int) testutil.Reply {
		ms := req["messages"].([]any)
		sys, _ := ms[0].(map[string]any)["content"].(string)
		switch {
		case strings.Contains(sys, "ONE TRACKER"):
			return testutil.Reply{Content: `{"fit":true,"name":"FEVM boxes","description":"Avito prices","columns":[{"name":"price","type":"number","unit":"RUB"}],
				"items":[{"key":"FEVM X","data":{"item":"FEVM X"},"notify_when":"under 700000"},{"key":"FEVM Y","data":{"item":"FEVM Y"},"notify_when":"under 700000"},{"key":"FEVM Z","data":{"item":"FEVM Z"},"notify_when":"under 700000"}],
				"method":"search Avito for the box","expr":"@hourly","why":"three items with a price each"}`}
		case strings.Contains(sys, "ONE kind of check"):
			return testutil.Reply{Content: `{"same":true,"prompt":"merged 700000","expr":"@hourly","why":"same check"}`}
		}
		return testutil.Reply{Content: "ok"}
	}
	var ids []int64
	for _, n := range []string{"X", "Y", "Z"} {
		id, err := s.SaveCron(ctx, Cron{Name: "avito " + n, Agent: "Atlas", Expr: "@daily", Prompt: "Check Avito price for the FEVM " + n + " box and tell me if it is under 700000. Reply NO_REPLY if nothing.", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if n, err := s.ReviewJobs(ctx); err != nil || n != 1 {
		t.Fatalf("one proposal: %d %v", n, err)
	}
	ps, _ := s.JobProposals(ctx, "pending")
	if len(ps) != 1 || ps[0].Kind != "tracker" || ps[0].NewAgent != "Organizer" {
		t.Fatalf("a tracker proposal, run by the agent that holds the tracker tools: %+v", ps)
	}
	if err := s.ApplyJobProposal(ctx, ps[0].ID, false); err != nil {
		t.Fatal(err)
	}
	tr, err := s.Trackers.Get(ctx, "FEVM boxes")
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.Trackers.Rows(ctx, tr.ID, "", false, 50); len(rows) != 3 {
		t.Fatalf("one starting row per item: %d", len(rows))
	}
	cs, _ := s.Crons(ctx)
	live, finished := 0, 0
	for _, c := range cs {
		for _, id := range ids {
			if c.ID == id {
				if c.FinishedAt != nil {
					finished++
				} else if c.Agent == "Organizer" && strings.Contains(c.Prompt, "tracker_snapshot") {
					live++
				}
			}
		}
	}
	if live != 1 || finished != 2 {
		t.Fatalf("one refresh job survives, run by the organizer: live=%d finished=%d", live, finished)
	}
	if err := s.UndoJobProposal(ctx, ps[0].ID); err != nil {
		t.Fatal(err)
	}
	cs, _ = s.Crons(ctx)
	for _, c := range cs {
		for _, id := range ids {
			if c.ID == id && (c.Agent != "Atlas" || c.FinishedAt != nil || strings.Contains(c.Prompt, "tracker_snapshot")) {
				t.Fatalf("undo restores the three jobs: %+v", c)
			}
		}
	}
	if _, err := s.Trackers.Get(ctx, "FEVM boxes"); err != nil {
		t.Fatal("the tracker stays")
	}
}

func TestKeepsNumbersAndRefreshPrompt(t *testing.T) {
	orig := []string{"tell me if it is under 700 000 RUB", "alert when below 800000"}
	if !keepsNumbers(orig, "X under 700000, Y below 800 000") {
		t.Fatal("same figures, spaced differently, are kept")
	}
	if keepsNumbers(orig, "X under 700000, Y below 900000") {
		t.Fatal("a changed or missing threshold is caught")
	}
	var plan trackerPlan
	plan.Name = "Boxes"
	plan.Items = append(plan.Items, struct {
		Key        string         `json:"key"`
		Data       map[string]any `json:"data"`
		NotifyWhen string         `json:"notify_when"`
	}{Key: "FEVM X", NotifyWhen: "the cheapest listing is under 700000 RUB"})
	p := refreshPrompt(plan, "search Avito")
	for _, want := range []string{`"Boxes"`, "FEVM X", "under 700000 RUB", "tracker_snapshot", "NO_REPLY", "search Avito"} {
		if !strings.Contains(p, want) {
			t.Fatalf("refresh prompt lacks %q: %s", want, p)
		}
	}
}
