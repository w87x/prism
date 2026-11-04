package agent

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestVerificationBudgetSharedAcrossConcurrentCalls(t *testing.T) {
	b := &verificationBudget{}
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.allow("web_search") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 18 {
		t.Fatalf("allowed %d web calls, want 18", got)
	}
	for i := 0; i < 8; i++ {
		if !b.allow("delegate") {
			t.Fatal("delegation budget exhausted early")
		}
	}
	if b.allow("ask_colleague") {
		t.Fatal("delegations must share a budget")
	}
	if !b.allow("memory_verify") {
		t.Fatal("verdicts must remain available after searches stop")
	}
}
