package llm

import (
	"context"
	"testing"
	"time"
)

func TestBackgroundWaitsForForegroundAndCancels(t *testing.T) {
	var p inferencePriority
	finish, err := p.acquire(context.Background(), "local", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan error, 1)
	go func() {
		release, err := p.acquire(ctx, "local", true)
		if err == nil {
			release()
		}
		ready <- err
	}()
	select {
	case <-ready:
		t.Fatal("background ran during foreground")
	case <-time.After(20 * time.Millisecond):
	}
	finish()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("background did not resume")
	}
	finish, _ = p.acquire(context.Background(), "local", false)
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := p.acquire(cancelled, "local", true); err == nil {
		t.Fatal("cancelled waiter succeeded")
	}
	finish()
}

func TestPriorityIsPerEndpointAndBackgroundEventuallyRuns(t *testing.T) {
	p := inferencePriority{maxWait: 25 * time.Millisecond}
	done, err := p.acquire(context.Background(), "first", false)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	start := time.Now()
	other, err := p.acquire(context.Background(), "second", true)
	if err != nil {
		t.Fatal(err)
	}
	other()
	if time.Since(start) > 20*time.Millisecond {
		t.Fatal("unrelated endpoint blocked")
	}
	start = time.Now()
	release, err := p.acquire(context.Background(), "first", true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if time.Since(start) < 15*time.Millisecond || time.Since(start) > time.Second {
		t.Fatal("bounded background wait failed")
	}
}
