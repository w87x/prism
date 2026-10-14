package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/testutil"
	"prism/internal/tools"
)

func finishedTask(t *testing.T, h *harness, kind, name, status, result string) tasks.Task {
	t.Helper()
	ctx := context.Background()
	tk, err := h.e.Tasks.Create(ctx, tasks.Task{FromKind: kind, FromName: name, ToAgent: "Atlas", Title: name, Input: "watch the ads"}, true)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := h.e.Sessions.Create(ctx, "Atlas", "task", "", tk.ID)
	_ = h.e.Tasks.SetSession(ctx, tk.ID, sess.ID)
	_ = h.e.Tasks.Finish(ctx, tk.ID, status, result, "", "")
	out, _ := h.e.Tasks.Get(ctx, tk.ID)
	return out
}

// A routine job is learned on the main model, then handed to the small model with a runbook; two failures in a row take it back.
func TestRunbookIsLearnedFollowedAndRetired(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		if strings.Contains(sys, "writing a runbook") {
			return testutil.Reply{Content: "1. Open the listing page with web_fetch.\n2. If a price is under 100 euro reply with the ad; otherwise reply exactly NO_REPLY.\n3. Escalate when the page layout is unfamiliar."}
		}
		return testutil.Reply{Content: "ok"}
	}
	probe := finishedTask(t, h, "cron", "ads", tasks.Done, "x")
	state, _ := h.e.runbookGate(ctx, probe)
	if state != "learning" || h.e.routineModelRef(ctx, probe) != "" {
		t.Fatalf("an unknown job is learned on the main model: %q", state)
	}
	for i := 0; i < 2; i++ {
		tk := finishedTask(t, h, "cron", "ads", tasks.Done, "Found nothing new.")
		st, rb := h.e.runbookGate(ctx, tk)
		h.e.afterRoutine(ctx, tk, st, rb, true)
	}
	var rb Runbook
	waitFor(t, 5*time.Second, func() bool {
		rb, _ = h.e.runbookOf(ctx, "cron:ads")
		return rb.Status == "active"
	})
	if !strings.Contains(rb.Steps, "NO_REPLY") || h.e.routineModelRef(ctx, probe) != "role:fast" {
		t.Fatalf("the written runbook hands the job to the small model: %+v", rb)
	}
	for i := 0; i < 2; i++ { // two small-model failures in a row
		tk := finishedTask(t, h, "cron", "ads", tasks.Failed, "")
		st, r := h.e.runbookGate(ctx, tk)
		h.e.afterRoutine(ctx, tk, st, r, false)
	}
	rb, _ = h.e.runbookOf(ctx, "cron:ads")
	if rb.Status != "stale" || h.e.routineModelRef(ctx, probe) != "" {
		t.Fatalf("two failures send the job back to the main model: %+v", rb)
	}
	_ = h.e.Settings.Set(ctx, settings.KeyAutonomy, settings.Autonomy{RunbooksOff: true})
	if h.e.routineModelRef(ctx, probe) != "role:fast" {
		t.Fatal("with runbooks off the small model keeps its old role")
	}
}

// A delegated result is checked against its acceptance criteria; a miss goes back once with the gap named.
func TestAcceptanceCriteriaSendAShortfallBack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var judged, retried atomic.Int32
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		_, last, _ := msgAt(req, -1)
		switch {
		case strings.Contains(sys, "team lead checking"):
			if judged.Add(1) == 1 {
				return testutil.Reply{Content: `{"met":false,"gap":"no file path was reported"}`}
			}
			return testutil.Reply{Content: `{"met":true,"gap":""}`}
		case strings.Contains(last, "[Lead's check]"):
			retried.Add(1)
			return testutil.Reply{Content: "Saved to /tmp/report.md"}
		}
		return testutil.Reply{Content: "Done, report written."}
	}
	tk, _ := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "agent", FromName: "Atlas", ToAgent: "Atlas", Title: "report", Input: "write the report", Depth: 1}, true)
	h.e.Tasks.SetCriteria(ctx, tk.ID, "the saved file's path is reported")
	out := h.e.RunTask(ctx, tk, TaskOpts{})
	m := h.e.Tasks.MetaOf(ctx, tk.ID)
	if retried.Load() != 1 || m.CriteriaMet == nil || !*m.CriteriaMet || !strings.Contains(out.Result, "/tmp/report.md") {
		t.Fatalf("retried=%d meta=%+v result=%q", retried.Load(), m, out.Result)
	}
	// criteria that stay unmet are said so in the result itself
	judged.Store(-10)
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		if strings.Contains(sys, "team lead checking") {
			return testutil.Reply{Content: `{"met":false,"gap":"still nothing saved"}`}
		}
		return testutil.Reply{Content: "all good"}
	}
	tk2, _ := h.e.Tasks.Create(ctx, tasks.Task{FromKind: "agent", FromName: "Atlas", ToAgent: "Atlas", Title: "r2", Input: "again", Depth: 1}, true)
	h.e.Tasks.SetCriteria(ctx, tk2.ID, "something saved")
	out2 := h.e.RunTask(ctx, tk2, TaskOpts{})
	if !strings.Contains(out2.Result, "Acceptance check: NOT met") {
		t.Fatalf("an unmet check must show in the result: %q", out2.Result)
	}
}

func TestDecisionLogRecordsSearchesAndSupersedes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tool, ok := h.e.Tools.Get("decisions")
	if !ok {
		t.Fatal("decisions tool missing")
	}
	run := func(args map[string]any) string {
		b, _ := json.Marshal(args)
		out, err := tool.Run(ctx, &tools.Env{Agent: "Atlas"}, b)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	run(map[string]any{"action": "record", "title": "Media server", "decision": "Use Plex", "why": "Jellyfin lacked the TV app", "review_in_days": 1})
	if out := run(map[string]any{"action": "search", "query": "plex"}); !strings.Contains(out, "Jellyfin lacked") {
		t.Fatalf("search finds the reason: %q", out)
	}
	ds, _ := h.e.Decisions(ctx, "")
	run(map[string]any{"action": "record", "title": "Media server", "decision": "Use Jellyfin", "why": "Plex went paid", "supersedes": ds[0].ID})
	if act, _ := h.e.Decisions(ctx, "active"); len(act) != 1 || act[0].Decision != "Use Jellyfin" {
		t.Fatalf("the old decision is superseded: %+v", act)
	}
	if _, err := h.e.RecordDecision(ctx, Decision{Title: "x"}); err == nil {
		t.Fatal("a decision needs its text")
	}
	_, _ = h.e.DB.Exec(ctx, `UPDATE decisions SET review_at=now()-interval '1 day' WHERE status='active'`)
	if due, _ := h.e.Decisions(ctx, "due"); len(due) != 1 {
		t.Fatal("a passed review date makes it due")
	}
}

func TestReviewerApprovesDeniesAndNeverWavesThroughRiskyThings(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var asked atomic.Int32
	verdict := "approve"
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		asked.Add(1)
		return testutil.Reply{Content: `{"verdict":"` + verdict + `","reason":"fits the request"}`}
	}
	env := &tools.Env{Agent: "Atlas", TaskID: 0}
	write := &tools.Tool{Name: "note_save", Risk: tools.RiskWrite, Description: "save a note"}
	if v, _ := h.e.preReview(ctx, env, write, "note_save", `{}`, "save a note"); v != "ask" || asked.Load() != 0 {
		t.Fatal("off by default: the user is asked and no model is bothered")
	}
	_ = h.e.Settings.Set(ctx, settings.KeyReview, settings.Review{Enabled: true, AlwaysAsk: []string{"calendar_add"}})
	if v, _ := h.e.preReview(ctx, env, write, "note_save", `{}`, "save a note"); v != "approve" {
		t.Fatalf("a harmless call that follows the request is approved: %q", v)
	}
	asked.Store(0)
	for _, name := range []string{"mail_send", "file_delete", "calendar_add"} {
		if v, _ := h.e.preReview(ctx, env, &tools.Tool{Name: name, Risk: tools.RiskWrite}, name, `{}`, "x"); v != "ask" {
			t.Fatalf("%s must always go to the user, got %q", name, v)
		}
	}
	if asked.Load() != 0 {
		t.Fatal("tools that always go to the user are not even put to the model")
	}
	if v, _ := h.e.preReview(ctx, env, &tools.Tool{Name: "shell", Risk: tools.RiskExec}, "shell", `{"cmd":"ls"}`, "list files"); v != "ask" {
		t.Fatalf("code execution is never waved through: %q", v)
	}
	verdict = "deny"
	if v, _ := h.e.preReview(ctx, env, write, "note_save", `{}`, "x"); v != "deny" {
		t.Fatalf("a refusal stands: %q", v)
	}
	if rows, _ := h.e.ReviewLog(ctx, 10); len(rows) < 2 {
		t.Fatalf("decisions are logged: %+v", rows)
	}
}

func TestThumbsDownPatternPutsAReviewOnMetisDesk(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	mk := func() int64 {
		tk := finishedTask(t, h, "user", "user", tasks.Done, "meh")
		return tk.ID
	}
	_ = h.e.RateTask(ctx, mk(), -1, "wrong store")
	var n int
	_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE to_agent='Metis'`).Scan(&n)
	if n != 0 {
		t.Fatal("one thumbs-down is not a pattern")
	}
	_ = h.e.RateTask(ctx, mk(), -1, "")
	_ = h.e.RateTask(ctx, mk(), -1, "")
	_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE to_agent='Metis' AND title='Improve Atlas'`).Scan(&n)
	if n != 1 {
		t.Fatalf("two or more thumbs-down in two weeks ask Metis once, got %d", n)
	}
}

func TestStandupSummarisesTheWeek(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, last, _ := msgAt(req, -1)
		_ = last
		return testutil.Reply{Content: "x"}
	}
	tk := finishedTask(t, h, "user", "user", tasks.Done, "ok")
	_ = h.e.RateTask(ctx, tk.ID, -1, "too slow")
	var seen string
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, seen, _ = msgAt(req, -1)
		return testutil.Reply{Content: "All quiet."}
	}
	s, err := h.e.RunStandup(ctx, time.Now().Add(-24*time.Hour), time.Now().Add(time.Hour), false)
	if err != nil || s.Text != "All quiet." {
		t.Fatalf("%v %+v", err, s)
	}
	if !strings.Contains(seen, "Atlas: 1 done") || !strings.Contains(seen, "too slow") {
		t.Fatalf("the model is given the real numbers and the bad rating: %q", seen)
	}
	if list, _ := h.e.Standups(ctx, 5); len(list) != 1 {
		t.Fatal("standups are kept")
	}
}
