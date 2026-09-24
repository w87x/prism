package scheduler

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestFractionOfAndETA(t *testing.T) {
	for in, want := range map[string]float64{"downloading 45%": 0.45, "copied 30% ... 60%": 0.6, "12/48 files": 0.25, "done": -1, "5 of 10": -1, "7/0": -1, "150%": -1} {
		f, ok := fractionOf(in)
		if want < 0 {
			if ok {
				t.Fatalf("%q should have no fraction, got %v", in, f)
			}
			continue
		}
		if !ok || f < want-0.001 || f > want+0.001 {
			t.Fatalf("%q → %v %v, want %v", in, f, ok, want)
		}
	}
	now := time.Now().Unix()
	if e, ok := etaSeconds([]sample{{T: now - 120, F: 0.2}, {T: now - 60, F: 0.4}, {T: now, F: 0.6}}); !ok || e < 110 || e > 130 {
		t.Fatalf("eta = %d %v, want about 120s", e, ok)
	}
	if _, ok := etaSeconds([]sample{{T: now - 60, F: 0.5}, {T: now, F: 0.5}}); ok {
		t.Fatal("a stalled monitor has no estimate")
	}
	if _, ok := etaSeconds([]sample{{T: now, F: 0.5}}); ok {
		t.Fatal("one sample is not enough for an estimate")
	}
}

// A monitor samples the fraction it reports, estimates its finish time, ends itself when its time budget is
// used up, and can be given more time on demand (which also wakes an expired one).
func TestMonitorSamplesEstimatesExpiresAndExtends(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	s.Env.Download = func(context.Context, int64) (string, int64, int64, error) { return "downloading", 60, 100, nil }

	exp := time.Now().Add(30 * time.Minute)
	id, err := s.CreateIntent(ctx, Intent{Owner: "Atlas", Type: "watch", Description: "big download", CadenceS: 60, Notify: true, ExpiresAt: &exp},
		Predicate{Kind: "download", DownloadID: 7})
	if err != nil {
		t.Fatal(err)
	}
	// two earlier samples, as if it had been checked a minute and two minutes ago
	prior, _ := json.Marshal([]sample{{T: time.Now().Unix() - 120, F: 0.2}, {T: time.Now().Unix() - 60, F: 0.4}})
	if _, err := s.DB.Exec(ctx, `UPDATE intents SET samples=$2 WHERE id=$1`, id, prior); err != nil {
		t.Fatal(err)
	}
	is, _ := s.Intents(ctx, "active")
	s.check(ctx, is[0])
	is, _ = s.Intents(ctx, "active")
	if len(is) != 1 || is[0].Fraction == nil || *is[0].Fraction < 0.59 || is[0].ETASeconds == nil || *is[0].ETASeconds < 100 || *is[0].ETASeconds > 140 {
		t.Fatalf("expected fraction 0.6 and an ETA near 2 minutes: %+v", is)
	}

	// time is up: the next tick ends it
	if _, err := s.DB.Exec(ctx, `UPDATE intents SET expires_at=now()-interval '1 minute' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	s.tickIntents(ctx)
	if act, _ := s.Intents(ctx, "active"); len(act) != 0 {
		t.Fatalf("an expired monitor must stop being active: %+v", act)
	}
	if ex, _ := s.Intents(ctx, "expired"); len(ex) != 1 {
		t.Fatalf("expected one expired monitor: %+v", ex)
	}

	// more time wakes it again
	if err := s.ExtendIntent(ctx, id, 45); err != nil {
		t.Fatal(err)
	}
	act, _ := s.Intents(ctx, "active")
	if len(act) != 1 || act[0].ExpiresAt == nil || time.Until(*act[0].ExpiresAt) < 40*time.Minute {
		t.Fatalf("extending must reactivate with a fresh budget: %+v", act)
	}
	if err := s.ExtendIntent(ctx, 99999, 5); err == nil {
		t.Fatal("extending a missing monitor must fail")
	}
}
