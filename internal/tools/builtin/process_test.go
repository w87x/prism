package builtin

import (
	"sync"

	"context"
	"net/url"
	"prism/internal/settings"
	"prism/internal/testutil"
	"prism/internal/tools"
	"strings"
	"testing"
	"time"
)

func waitProcess(t *testing.T, pm *ProcessManager, id int64, want string, timeout time.Duration) Process {
	t.Helper()
	dl := time.Now().Add(timeout)
	for time.Now().Before(dl) {
		p, err := pm.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status == want {
			return p
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("process #%d did not reach status %q in time", id, want)
	return Process{}
}

// Bug this replaces: shell/python block for the whole call, so a long command either ties up the agent's
// entire turn or has to be killed by the tool's own timeout. process_start must return control immediately,
// well before the command itself finishes.
func TestProcessStartReturnsImmediatelyAndReportsExitCode(t *testing.T) {
	_, _, _, pm := setup(t)
	ctx := context.Background()
	started := time.Now()
	p, err := pm.Start(ctx, StartReq{Command: "sleep 2; exit 7", Agent: "Scout"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("Start blocked for %s — it must return before the command finishes", elapsed)
	}
	if p.Status != "running" {
		t.Fatalf("expected running immediately after Start, got %s", p.Status)
	}
	fin := waitProcess(t, pm, p.ID, "exited", 5*time.Second)
	if fin.ExitCode == nil || *fin.ExitCode != 7 {
		t.Fatalf("exit code: %+v", fin.ExitCode)
	}
	if fin.FinishedAt == nil {
		t.Fatal("finished_at not set")
	}
}

// Output must be readable while the process is still running (not just after it exits), and paged the same
// way web_fetch/artifact_read page long text.
func TestProcessLogStreamsWhileRunningAndPages(t *testing.T) {
	_, _, _, pm := setup(t)
	ctx := context.Background()
	p, err := pm.Start(ctx, StartReq{Command: "echo start; sleep 5; echo end", Agent: "Scout"})
	if err != nil {
		t.Fatal(err)
	}
	defer pm.Cancel(ctx, p.ID)

	var text string
	dl := time.Now().Add(2 * time.Second)
	for time.Now().Before(dl) {
		text, _, err = pm.Log(ctx, p.ID, 0, 8000)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(text, "start") {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(text, "start") {
		t.Fatalf("expected partial output while still running, got %q", text)
	}
	if strings.Contains(text, "end") {
		t.Fatal("process is still running (5s sleep); it should not have produced its final line yet")
	}
	// paging: offset past the content returns nothing more, not an error
	page, total, err := pm.Log(ctx, p.ID, 1000, 100)
	if err != nil || page != "" || total == 0 {
		t.Fatalf("offset beyond content: page=%q total=%d err=%v", page, total, err)
	}
}

// A process must accept stdin while running (a REPL, a prompt) — this is exactly what shell/python cannot
// offer since they only see the whole command up front.
func TestProcessInputReachesTheRunningProcess(t *testing.T) {
	_, _, _, pm := setup(t)
	ctx := context.Background()
	p, err := pm.Start(ctx, StartReq{Command: `read line; echo "got: $line"`, Agent: "Scout"})
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.Input(ctx, p.ID, "hello there"); err != nil {
		t.Fatal(err)
	}
	fin := waitProcess(t, pm, p.ID, "exited", 3*time.Second)
	text, _, err := pm.Log(ctx, fin.ID, 0, 8000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "got: hello there") {
		t.Fatalf("stdin did not reach the process: %q", text)
	}
}

// Cancel must actually stop the process (SIGTERM, escalating to SIGKILL), not just mark it stopped in the DB
// while the OS process keeps running unsupervised.
func TestProcessCancelStopsTheProcess(t *testing.T) {
	_, _, _, pm := setup(t)
	ctx := context.Background()
	p, err := pm.Start(ctx, StartReq{Command: "trap '' TERM; sleep 30", Agent: "Scout"}) // ignores SIGTERM, forcing the SIGKILL escalation
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.Cancel(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	fin := waitProcess(t, pm, p.ID, "killed", 8*time.Second)
	if fin.FinishedAt == nil {
		t.Fatal("finished_at not set on a killed process")
	}
}

// Bug class already fixed once for tasks (pending_asks/ReconcilePendingAsks): a process still marked
// 'running' after a restart has no live handle any more. Reconcile must not leave it claiming to be running
// when nothing can actually vouch for that.
func TestReconcileMarksOrphanedRunningProcessesUnknown(t *testing.T) {
	_, deps, _, pm := setup(t)
	ctx := context.Background()
	p, err := pm.Start(ctx, StartReq{Command: "sleep 30", Agent: "Scout"})
	if err != nil {
		t.Fatal(err)
	}
	defer pm.Cancel(ctx, p.ID)
	waitProcess(t, pm, p.ID, "running", time.Second) // sanity: it is genuinely running before we simulate the restart

	// Simulate a restart: a fresh manager over the same DB, with no in-memory record of the process at all.
	fresh := newProcessManager(deps)
	n, err := fresh.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row reconciled, got %d", n)
	}
	got, err := fresh.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unknown" {
		t.Fatalf("expected status unknown after reconciliation, got %q", got.Status)
	}
}

// Cancelling a process this manager has no live handle for (e.g. it was reconciled as 'unknown' by a
// different manager instance, or the row was created out of band) must still tell the UI something changed
// — silently updating the DB with no event leaves an open log view stuck showing a stale "running" state.
func TestCancelWithoutLiveHandleStillEmitsUpdate(t *testing.T) {
	_, deps, _, _ := setup(t)
	ctx := context.Background()
	var events []map[string]any
	deps.Emit = func(typ string, data any) {
		if typ == "process.update" {
			events = append(events, data.(map[string]any))
		}
	}
	pm := newProcessManager(deps)
	var id int64
	if err := deps.DB.QueryRow(ctx, `INSERT INTO bg_processes(agent,command,status) VALUES('Scout','sleep 1','running') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := pm.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	p, err := pm.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "unknown" {
		t.Fatalf("expected status unknown, got %q", p.Status)
	}
	if len(events) != 1 || events[0]["id"] != id || events[0]["status"] != "unknown" {
		t.Fatalf("expected exactly one process.update event, got %+v", events)
	}
}

// A quick command needs ONE tool call: process_start waits for it and returns exit status and output inline
// (agents used to start a process for every `cat`/`ls`, then poll status, then read the log — burning their
// whole iteration budget and sending one "finished" notice per command). A slow command still returns control
// after the wait, with its id and progress.
func TestProcessStartWaitsForQuickCommandsAndReportsSlowOnes(t *testing.T) {
	reg, _, _, _ := setup(t)

	out, err := run(t, reg, "process_start", map[string]any{"command": "echo hello; exit 0"})
	if err != nil || !strings.Contains(out, "exited") || !strings.Contains(out, "hello") || strings.Contains(out, "Still running") {
		t.Fatalf("quick command should finish inline: %q %v", out, err)
	}

	start := time.Now()
	out, err = run(t, reg, "process_start", map[string]any{"command": "printf 'work 40%% at 2.0MiB/s ETA 00:30\\n'; sleep 8", "wait_s": 1})
	if err != nil || !strings.Contains(out, "Still running") || !strings.Contains(out, "progress: 40% · 2.0MiB/s · ETA 00:30") {
		t.Fatalf("slow command should report running with progress: %q %v", out, err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("wait_s=1 blocked for %s", time.Since(start))
	}
	out, err = run(t, reg, "process_status", map[string]any{"id": 2, "wait_s": 12})
	if err != nil || !strings.Contains(out, "exited") {
		t.Fatalf("process_status with wait_s should return the finished state: %q %v", out, err)
	}
}

func TestProgressOfReadsRealToolOutput(t *testing.T) {
	for in, want := range map[string]string{
		"[download]  45.3% of  350.10MiB at    2.50MiB/s ETA 02:10":                    "45.3% · 2.50MiB/s · ETA 02:10",
		"  1.2G  53%   45.3MB/s    0:00:20 (xfr#3)":                                    "53%",
		"warming up\n[download]  10.0% of 5MiB at 1MiB/s ETA 00:04\n[download]  11.0%": "11.0%",
		"no progress here": "",
	} {
		if got := progressOf(in); got != want {
			t.Fatalf("progressOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestYtdlpProgressCoversPlaylists(t *testing.T) {
	var y ytProgress
	lines := []string{
		"[download] Downloading item 2 of 4",
		"[download] Destination: /tmp/Episode 2 [abc].mp4",
		"[download]  50.0% of 100MiB at 1MiB/s ETA 00:50",
	}
	var p float64
	for _, l := range lines {
		if v, ok := y.feed(l); ok {
			p = v
		}
	}
	if p < 37.4 || p > 37.6 { // item 2 of 4, half done: (1*100 + 50) / 4
		t.Fatalf("overall progress = %v, want 37.5", p)
	}
	if y.Dest != "/tmp/Episode 2 [abc].mp4" {
		t.Fatalf("dest = %q", y.Dest)
	}
	u, _ := url.Parse("https://rutube.ru/video/abc/")
	if !isMediaSite(u) {
		t.Fatal("rutube should use yt-dlp")
	}
	u2, _ := url.Parse("https://example.com/file.zip")
	if isMediaSite(u2) {
		t.Fatal("a plain file host must not use yt-dlp")
	}
}

// Only long-running processes announce their end to the user; a quick command must not send a notice.
func TestQuickProcessesDoNotNotify(t *testing.T) {
	d := testutil.DB(t)
	var mu sync.Mutex
	notices := 0
	deps := Deps{DB: d.Pool, Settings: settings.New(d.Pool), DataDir: t.TempDir(), Emit: func(string, any) {},
		Notify: func(context.Context, string, string) { mu.Lock(); notices++; mu.Unlock() }}
	reg := tools.NewRegistry(d.Pool)
	_, pm := Register(reg, deps)
	p, err := pm.Start(context.Background(), StartReq{Command: "echo hi", Agent: "t"})
	if err != nil {
		t.Fatal(err)
	}
	waitProcess(t, pm, p.ID, "exited", 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if notices != 0 {
		t.Fatalf("a quick command produced %d notice(s)", notices)
	}
}

// Finished processes go after a day when they ended well and after a week when they failed; running ones stay.
func TestFinishedProcessesArePrunedByAgeAndOutcome(t *testing.T) {
	_, deps, _, _ := setup(t)
	ctx := context.Background()
	add := func(status string, exit *int, age time.Duration) int64 {
		var id int64
		at := time.Now().Add(-age)
		if err := deps.DB.QueryRow(ctx, `INSERT INTO bg_processes(command,status,exit_code,created_at,finished_at) VALUES('x',$1,$2,$3,$3) RETURNING id`, status, exit, at).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	zero, one := 0, 1
	okOld := add("exited", &zero, 30*time.Hour)     // gone
	okNew := add("exited", &zero, 5*time.Hour)      // kept: under a day
	killedOld := add("killed", nil, 26*time.Hour)   // gone: stopped on purpose
	failNew := add("exited", &one, 3*24*time.Hour)  // kept: failures stay a week
	failOld := add("failed", nil, 8*24*time.Hour)   // gone
	lostOld := add("unknown", nil, 9*24*time.Hour)  // gone
	running := add("running", nil, 30*24*time.Hour) // never touched
	n, err := PruneProcesses(ctx, deps.DB)
	if err != nil || n != 4 {
		t.Fatalf("pruned %d err=%v", n, err)
	}
	left := map[int64]bool{}
	rows, _ := deps.DB.Query(ctx, `SELECT id FROM bg_processes`)
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		left[id] = true
	}
	rows.Close()
	for id, want := range map[int64]bool{okOld: false, okNew: true, killedOld: false, failNew: true, failOld: false, lostOld: false, running: true} {
		if left[id] != want {
			t.Errorf("process %d present=%v, want %v", id, left[id], want)
		}
	}
}
