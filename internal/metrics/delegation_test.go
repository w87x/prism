package metrics

import (
	"context"
	"testing"
	"time"

	"prism/internal/testutil"
)

// A call graph shaped like a real delegated request: Atlas (root run 1) makes a routing call and, after the
// specialist answers, a synthesis call — both its own time, no direct work. Scout (run 2, parent 1) does the
// actual work in one call. A second, undelegated root (run 3: Atlas answering something itself — e.g. a
// plain chat reply with no delegate() call) must NOT be counted as overhead: nothing to divide against.
func TestDelegationReportSeparatesEntryOverheadFromSpecialistWork(t *testing.T) {
	d := testutil.DB(t)
	s := &Store{DB: d.Pool}
	ctx := context.Background()
	now := time.Now()

	ins := func(runID, parentRun int64, agent string, ms int) {
		if _, err := d.Exec(ctx, `INSERT INTO llm_calls(ts,model,agent,ms,run_id,parent_run) VALUES($1,'m',$2,$3,$4,NULLIF($5,0))`,
			now, agent, ms, runID, parentRun); err != nil {
			t.Fatal(err)
		}
	}
	// delegated chain: root run 1 (Atlas) → child run 2 (Scout)
	ins(1, 0, "Atlas", 500)  // routing decision
	ins(2, 1, "Scout", 4000) // the actual work
	ins(1, 0, "Atlas", 700)  // synthesis after the specialist answers
	// undelegated root: Atlas answered directly, no child run
	ins(3, 0, "Atlas", 300)
	// outside the window entirely
	if _, err := d.Exec(ctx, `INSERT INTO llm_calls(ts,model,agent,ms,run_id,parent_run) VALUES($1,'m','Atlas',99999,4,NULL)`,
		now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	rep, err := s.DelegationReport(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Roots != 2 {
		t.Fatalf("expected 2 root runs (1 delegated, 1 not) inside the window, got %d: %+v", rep.Roots, rep.Chains)
	}
	if rep.DelegatedRuns != 1 {
		t.Fatalf("expected exactly 1 delegated root, got %d", rep.DelegatedRuns)
	}
	if rep.RootMS != 1200 || rep.ChildMS != 4000 { // 500+700 Atlas overhead vs Scout's 4000 of real work
		t.Fatalf("root/child ms: %d/%d", rep.RootMS, rep.ChildMS)
	}
	wantOverhead := 1200.0 / (1200.0 + 4000.0)
	if got := rep.Overhead(); got < wantOverhead-0.001 || got > wantOverhead+0.001 {
		t.Fatalf("overhead = %v, want %v", got, wantOverhead)
	}

	var delegated, plain *DelegationChain
	for i := range rep.Chains {
		c := &rep.Chains[i]
		switch c.RootRun {
		case 1:
			delegated = c
		case 3:
			plain = c
		}
	}
	if delegated == nil || !delegated.Delegated || delegated.RootMS != 1200 || delegated.ChildMS != 4000 || delegated.ChildCalls != 1 {
		t.Fatalf("delegated chain: %+v", delegated)
	}
	if plain == nil || plain.Delegated || plain.RootMS != 300 || plain.ChildMS != 0 {
		t.Fatalf("undelegated chain must not look like overhead: %+v", plain)
	}
	if plain.Overhead() != 0 {
		t.Fatalf("an undelegated chain has nothing to compute overhead against, got %v", plain.Overhead())
	}
}
