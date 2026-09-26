package scheduler

import (
	"context"
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
	return &Service{DB: d.Pool, Engine: e, Settings: st, Emit: rc.emit, Env: Env{LLM: r}}, rc
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
