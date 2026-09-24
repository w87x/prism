package metrics

import (
	"context"
	"testing"
	"time"

	"prism/internal/llm"
	"prism/internal/testutil"
)

func TestDashboardAggregates(t *testing.T) {
	d := testutil.DB(t)
	s := &Store{DB: d.Pool}
	ctx := context.Background()
	now := time.Now()
	ins := func(ts time.Time, model, agent string, in, out, ms, wait int, ok bool, err string) {
		if _, e := d.Exec(ctx, `INSERT INTO llm_calls(ts,model,agent,tokens_in,tokens_out,ms,wait_ms,ok,err) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ts, model, agent, in, out, ms, wait, ok, err); e != nil {
			t.Fatal(e)
		}
	}
	// today: two good calls and one failure; two days ago: one call; 20 days ago: outside a 7-day view
	ins(now, "qwen", "Atlas", 1000, 200, 4000, 0, true, "")
	ins(now, "qwen", "Scout", 3000, 400, 8000, 2000, true, "")
	ins(now, "qwen", "Scout", 0, 0, 100, 0, false, "HTTP 500: model crashed")
	ins(now.AddDate(0, 0, -2), "nemotron", "", 500, 100, 1000, 0, true, "")
	ins(now.AddDate(0, 0, -20), "old", "Atlas", 9999, 9999, 1, 0, true, "")
	for _, r := range []struct {
		tool string
		ms   int
		ok   bool
		err  string
	}{{"web_fetch", 300, true, ""}, {"web_fetch", 900, false, "timeout"}, {"shell", 50, true, ""}} {
		if _, e := d.Exec(ctx, `INSERT INTO tool_calls(agent,tool,ms,ok,err) VALUES('Scout',$1,$2,$3,$4)`, r.tool, r.ms, r.ok, r.err); e != nil {
			t.Fatal(e)
		}
	}
	for _, st := range []string{"done", "done", "failed"} {
		if _, e := d.Exec(ctx, `INSERT INTO tasks(from_kind,from_name,to_agent,title,input,status,finished_at) VALUES('user','user','Scout','t','i',$1,now())`, st); e != nil {
			t.Fatal(e)
		}
	}

	db, err := s.Dashboard(ctx, 7, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Buckets) != 7 || db.Hourly {
		t.Fatalf("7 daily buckets expected, got %d hourly=%v", len(db.Buckets), db.Hourly)
	}
	today := db.Buckets[len(db.Buckets)-1]
	if today.Calls != 3 || today.Errs != 1 || today.In != 4000 || today.Out != 600 || today.Done != 2 || today.Fail != 1 {
		t.Fatalf("today: %+v", today)
	}
	if db.Buckets[4].Calls != 1 { // two days ago = index 4 of 7 (…, -2, -1, today)
		t.Fatalf("two days ago: %+v", db.Buckets[4])
	}
	tt := db.Totals
	if tt.Calls != 4 || tt.Errors != 1 || tt.In != 4500 || tt.Out != 700 || tt.TasksDone != 2 || tt.TasksFail != 1 {
		t.Fatalf("totals: %+v", tt)
	}
	// generation speed counts only successful calls that produced tokens: 700 tokens / 13 s
	if tt.TokPerSec < 50 || tt.TokPerSec > 55 {
		t.Fatalf("tok/s = %v", tt.TokPerSec)
	}
	if tt.WaitP95MS < 1000 || tt.Since == nil {
		t.Fatalf("wait/since: %+v", tt)
	}
	if len(db.Models) != 2 || db.Models[0].Model != "qwen" || db.Models[0].Errors != 1 || db.Models[0].Calls != 3 {
		t.Fatalf("models: %+v", db.Models)
	}
	agents := map[string]AgentRow{}
	for _, a := range db.Agents {
		agents[a.Agent] = a
	}
	if agents["Scout"].Calls != 2 || agents["Scout"].Failed != 1 || agents["Scout"].Done != 2 || agents["(background)"].Calls != 1 {
		t.Fatalf("agents: %+v", db.Agents)
	}
	if len(db.Tools) != 2 || db.Tools[0].Tool != "web_fetch" || db.Tools[0].Errors != 1 {
		t.Fatalf("tools: %+v", db.Tools)
	}
	if len(db.Errors) != 2 || db.Errors[0].Err == "" {
		t.Fatalf("recent errors: %+v", db.Errors)
	}
	// 24 h view is hourly and quiet hours exist as zeros
	h, err := s.Dashboard(ctx, 1, "UTC")
	if err != nil || !h.Hourly || len(h.Buckets) != 24 {
		t.Fatalf("hourly: %v %+v", err, len(h.Buckets))
	}
	// the server's own zone is Go's "Local", which Postgres has never heard of: bucketing must not depend on it
	for _, tz := range []string{"", "Asia/Kolkata", "America/Los_Angeles"} {
		z, err := s.Dashboard(ctx, 7, tz)
		if err != nil {
			t.Fatalf("tz %q: %v", tz, err)
		}
		var calls int64
		for _, b := range z.Buckets {
			calls += b.Calls
		}
		if calls != 4 || z.Totals.TasksDone != 2 {
			t.Fatalf("tz %q: calls in buckets %d (want 4), tasks done %d (want 2)", tz, calls, z.Totals.TasksDone)
		}
	}
	// out-of-range days are clamped; an unknown zone falls back instead of failing
	if x, err := s.Dashboard(ctx, 500, "Not/AZone"); err != nil || x.Days != 30 || len(x.Buckets) != 30 {
		t.Fatalf("clamp: %v", err)
	}
}

// The router reports every finished call with the caller's attribution; a cancelled call is not a failure.
func TestRouterFeedsTheCallLog(t *testing.T) {
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	r, _ := testutil.Setup(t, d, fake)
	s := &Store{DB: d.Pool}
	r.OnCall(s.RecordLLM)
	ctx := context.Background()

	fake.Handler = func(req map[string]any, call int) testutil.Reply { return testutil.Reply{Content: "hello"} }
	if _, err := r.Chat(llm.WithMeta(ctx, &llm.CallMeta{Agent: "Atlas", WaitMS: 42}), "chat", llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}}, nil); err != nil {
		t.Fatal(err)
	}
	fake.Handler = func(req map[string]any, call int) testutil.Reply { return testutil.Reply{Status: 500} }
	_, _ = r.Chat(llm.WithMeta(ctx, &llm.CallMeta{Agent: "Scout"}), "chat", llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}}, nil)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, _ = r.Chat(cctx, "chat", llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}}, nil)

	var db *Dashboard
	for i := 0; i < 100; i++ { // writes are asynchronous
		db, _ = s.Dashboard(ctx, 7, "UTC")
		if db != nil && db.Totals.Calls >= 2 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if db.Totals.Calls != 2 || db.Totals.Errors != 1 {
		t.Fatalf("one success + one failure recorded, the cancelled call not: %+v", db.Totals)
	}
	got := map[string]AgentRow{}
	for _, a := range db.Agents {
		got[a.Agent] = a
	}
	if got["Atlas"].Calls != 1 || got["Scout"].Errors != 1 {
		t.Fatalf("attribution: %+v", db.Agents)
	}
	if db.Totals.WaitAvgMS != 21 { // (42 + 0) / 2
		t.Fatalf("wait avg %v", db.Totals.WaitAvgMS)
	}
	if len(db.Errors) != 1 || db.Errors[0].Kind != "llm" || db.Errors[0].Agent != "Scout" {
		t.Fatalf("errors: %+v", db.Errors)
	}
}
