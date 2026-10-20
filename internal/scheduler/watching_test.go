package scheduler

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"prism/internal/tools"
	"prism/internal/tracker"
)

// "I have the show, stop watching it" finds the job, the watch and the tracker row in one search and ends them all, leaving everything else.
func TestWatchFindAndStopSpanJobsIntentsAndTrackers(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	s.Trackers = &tracker.Service{DB: s.DB}
	cid, err := s.SaveCron(ctx, Cron{Name: "Dune_Part_Three_torrent_monitor", Agent: "Vera", Expr: "0 9 * * *", Prompt: "check whether Dune Part Three has a torrent", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.SaveCron(ctx, Cron{Name: "Habr_digest", Agent: "Vera", Expr: "45 8 * * *", Prompt: "daily Habr digest", Enabled: true})
	iid, err := s.CreateIntent(ctx, Intent{Owner: "Vera", Type: "watch", Description: "Dune release date announcement", CadenceS: 3600, Notify: true}, Predicate{Kind: "shell", Command: "true", Expect: "x"})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := s.Trackers.Create(ctx, "Releases", "films and shows awaited", []tracker.Column{{Name: "title", Type: "string"}}, "t")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = s.Trackers.UpsertRow(ctx, tr.ID, "Dune Part Three", map[string]any{"title": "Dune Part Three"}, "")
	_, _, _ = s.Trackers.UpsertRow(ctx, tr.ID, "Severance S3", map[string]any{"title": "Severance S3"}, "")

	find, _ := s.watchHits(ctx)
	if len(find) < 5 {
		t.Fatalf("hits: %+v", find)
	}
	reg := tools.NewRegistry(s.DB)
	s.RegisterTools(reg)
	run := func(name, args string) string {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("no tool %s", name)
		}
		out, err := tool.Run(ctx, &tools.Env{Agent: "Atlas"}, []byte(args))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := run("watch_find", `{"query":"dune"}`)
	for _, want := range []string{"cron:", "intent:", "row:Releases/Dune Part Three"} {
		if !strings.Contains(out, want) {
			t.Fatalf("watch_find missed %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Habr") || strings.Contains(out, "Severance") {
		t.Fatalf("watch_find returned unrelated things:\n%s", out)
	}
	got := run("watch_stop", `{"refs":["cron:`+strconv.FormatInt(cid, 10)+`","intent:`+strconv.FormatInt(iid, 10)+`","row:Releases/Dune Part Three"],"note":"released and downloaded"}`)
	if !strings.Contains(got, "Stopped") || strings.Contains(got, "Could not") {
		t.Fatalf("stop: %s", got)
	}
	cs, _ := s.Crons(ctx)
	for _, c := range cs {
		if c.ID == cid && (c.Enabled || c.FinishedAt == nil) {
			t.Fatalf("the Dune job should be finished: %+v", c)
		}
		if c.ID == other && !c.Enabled {
			t.Fatal("an unrelated job must be left running")
		}
	}
	if is, _ := s.Intents(ctx, "active"); len(is) != 0 {
		t.Fatalf("the watch should be cancelled: %+v", is)
	}
	if rows, _ := s.Trackers.Rows(ctx, tr.ID, "", false, 50); len(rows) != 1 || rows[0].Key != "Severance S3" {
		t.Fatalf("only the Dune row is retired: %+v", rows)
	}
	// archiving a tracker also finishes the jobs that refresh it
	rid, _ := s.SaveCron(ctx, Cron{Name: "Releases_refresh", Agent: "Ilya", Expr: "0 6 * * *", Prompt: "Refresh tracker Releases daily", Enabled: true})
	run("watch_stop", `{"refs":["tracker:Releases"],"note":"all released"}`)
	cs, _ = s.Crons(ctx)
	for _, c := range cs {
		if c.ID == rid && c.FinishedAt == nil {
			t.Fatal("the job that refreshes an archived tracker should finish with it")
		}
	}
}
