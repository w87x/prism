package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"prism/internal/tools"
)

// A repeating watch on something that STAYS true tells the user once when it becomes true, not at every check; it tells again after the
// condition has been false in between.
func TestRepeatingWatchFiresOnTheEdgeNotWhileTheConditionHolds(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	var told atomic.Int32
	s.Notify = func(kind, level, title, text string) {
		if title == "Intent triggered" {
			told.Add(1)
		}
	}
	file := filepath.Join(t.TempDir(), "done.flag")
	id, err := s.CreateIntent(ctx, Intent{Owner: "Atlas", Type: "watch", Description: "the flag appears", CadenceS: 10, Repeat: true, Notify: true}, Predicate{Kind: "file", Path: file})
	if err != nil {
		t.Fatal(err)
	}
	due := func() { _, _ = s.DB.Exec(ctx, `UPDATE intents SET next_due=now() WHERE id=$1`, id) }
	round := func() { due(); s.tick(ctx) }
	round() // the file does not exist yet
	if told.Load() != 0 {
		t.Fatal("nothing to tell yet")
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	round()
	round()
	round()
	if told.Load() != 1 {
		t.Fatalf("told %d times while the file just stayed there, want once", told.Load())
	}
	_ = os.Remove(file)
	round() // false in between
	_ = os.WriteFile(file, []byte("x"), 0o644)
	round()
	if told.Load() != 2 {
		t.Fatalf("it should fire again after the condition was false: %d", told.Load())
	}
	var status string
	_ = s.DB.QueryRow(ctx, `SELECT status FROM intents WHERE id=$1`, id).Scan(&status)
	if status != "active" {
		t.Fatalf("a repeating watch stays active: %s", status)
	}
}

// A check that keeps failing is retried less and less often and is given up on only after it has been failing for half an hour: a laptop
// that was offline for six minutes used to lose every one-minute watch.
func TestFailingChecksBackOffAndAreNotGivenUpOnQuickly(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	id, err := s.CreateIntent(ctx, Intent{Owner: "Atlas", Type: "watch", Description: "unreadable path", CadenceS: 60}, Predicate{Kind: "file", Path: "/etc/passwd/not-a-dir"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		_, _ = s.DB.Exec(ctx, `UPDATE intents SET next_due=now() WHERE id=$1`, id)
		s.tick(ctx)
	}
	var status string
	var secs float64
	_ = s.DB.QueryRow(ctx, `SELECT status, extract(epoch from next_due - now()) FROM intents WHERE id=$1`, id).Scan(&status, &secs)
	if status != "active" || secs < 5*60 {
		t.Fatalf("after 8 quick failures it is still active and waits long: %s, %.0fs", status, secs)
	}
	// failing since long ago: now it is given up on
	_, _ = s.DB.Exec(ctx, `UPDATE intents SET next_due=now(), predicate=jsonb_set(predicate,'{state,err_since}', to_jsonb(extract(epoch from now()-interval '40 minutes'))) WHERE id=$1`, id)
	s.tick(ctx)
	_ = s.DB.QueryRow(ctx, `SELECT status FROM intents WHERE id=$1`, id).Scan(&status)
	if status != "error" {
		t.Fatalf("failing for 40 minutes should stop it: %s", status)
	}
	// resumed, it starts over: the old failures do not count against it
	if err := s.UpdateIntent(ctx, id, "active", nil); err != nil {
		t.Fatal(err)
	}
	s.tick(ctx)
	_ = s.DB.QueryRow(ctx, `SELECT status FROM intents WHERE id=$1`, id).Scan(&status)
	if status != "active" {
		t.Fatalf("a resumed watch must not be stopped by its old failures: %s", status)
	}
}

// A watch polls its tool every few minutes with nobody watching: a tool that changes things must be refused when the watch is made.
func TestWatchOnAToolThatChangesThingsIsRefused(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	reg := tools.NewRegistry(s.DB)
	s.RegisterTools(reg)
	run := func(name string, risk tools.Risk) {
		reg.Register(&tools.Tool{Name: name, Description: "x", Risk: risk, Params: tools.Obj(""),
			Run: func(context.Context, *tools.Env, json.RawMessage) (string, error) { return "ok", nil }})
	}
	run("list_things", tools.RiskRead)
	run("delete_things", tools.RiskExec)
	intent, _ := reg.Get("intent_create")
	args := func(tool string) []byte {
		b, _ := json.Marshal(map[string]any{"description": "poll", "kind": "tool", "tool": tool, "expect": "done"})
		return b
	}
	if _, err := intent.Run(ctx, &tools.Env{Agent: "Atlas"}, args("delete_things")); err == nil || !strings.Contains(err.Error(), "changes things") {
		t.Fatalf("a changing tool must be refused: %v", err)
	}
	if out, err := intent.Run(ctx, &tools.Env{Agent: "Atlas"}, args("list_things")); err != nil || !strings.Contains(out, "registered") {
		t.Fatalf("a reading tool is fine: %q %v", out, err)
	}
}
