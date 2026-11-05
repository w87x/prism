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

func TestBackgroundCallsOnSameEndpointAreSerialized(t *testing.T) {
	p := inferencePriority{maxWait: 10 * time.Millisecond}
	release, err := p.acquire(context.Background(), "local", true)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan func(), 1)
	go func() {
		done, err := p.acquire(context.Background(), "local", true)
		if err == nil {
			ready <- done
		}
	}()
	select {
	case <-ready:
		t.Fatal("background calls overlapped")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case done := <-ready:
		done()
	case <-time.After(time.Second):
		t.Fatal("queued background call stalled")
	}
}

func TestBackgroundContextIsInherited(t *testing.T) {
	ctx := WithBackground(context.Background())
	if !IsBackground(context.WithoutCancel(ctx)) || IsBackground(context.Background()) {
		t.Fatal("background task priority was not inherited")
	}
}

func TestInteractiveLeaseYieldsWhileAskingAndPreservesRequiredWork(t *testing.T) {
	p := inferencePriority{maxWait: time.Second}
	ctx, finish := p.beginInteractive(context.Background(), []string{"local"})
	if !IsInteractive(ctx) || isBackgroundInference(ctx, "memory_dedupe") || !isBackgroundInference(WithBackground(ctx), "memory_dedupe") {
		t.Fatal("wrong inference priority in user task")
	}
	ready := make(chan func(), 1)
	go func() {
		release, err := p.acquire(context.Background(), "local", true)
		if err == nil {
			ready <- release
		}
	}()
	select {
	case <-ready:
		t.Fatal("maintenance started during user task")
	case <-time.After(20 * time.Millisecond):
	}
	resume := SuspendInteractive(ctx)
	select {
	case release := <-ready:
		release()
	case <-time.After(time.Second):
		t.Fatal("maintenance did not resume while user question was pending")
	}
	resume()
	finish()
	resume()
	if IsInteractive(ctx) {
		t.Fatal("finished lease remained active")
	}
	if !isBackgroundInference(ctx, "memory_dedupe") {
		t.Fatal("detached maintenance retained foreground priority")
	}
}

func TestFinishWhileInteractiveLeaseIsSuspended(t *testing.T) {
	p := inferencePriority{}
	ctx, finish := p.beginInteractive(context.Background(), []string{"local"})
	resume := SuspendInteractive(ctx)
	finish()
	resume()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.foreground["local"] != 0 {
		t.Fatal("finished suspended lease was reacquired")
	}
}

func TestPausingOneUserDoesNotReleaseAnotherUsersPriority(t *testing.T) {
	p := inferencePriority{maxWait: time.Second}
	first, finishFirst := p.beginInteractive(context.Background(), []string{"local"})
	_, finishSecond := p.beginInteractive(context.Background(), []string{"local"})
	resume := SuspendInteractive(first)
	ready := make(chan func(), 1)
	go func() {
		done, err := p.acquire(context.Background(), "local", true)
		if err == nil {
			ready <- done
		}
	}()
	select {
	case <-ready:
		t.Fatal("maintenance bypassed the other active user")
	case <-time.After(20 * time.Millisecond):
	}
	finishSecond()
	select {
	case done := <-ready:
		done()
	case <-time.After(time.Second):
		t.Fatal("maintenance did not start after both users yielded")
	}
	resume()
	finishFirst()
}

func TestInteractiveLeaseExtendsToSpecialistEndpoint(t *testing.T) {
	p := inferencePriority{}
	ctx, finish := p.beginInteractive(context.Background(), []string{"atlas"})
	l := interactiveFrom(ctx)
	if !l.extend([]string{"atlas", "specialist"}) {
		t.Fatal("active lease could not be extended")
	}
	p.mu.Lock()
	if p.foreground["atlas"] != 1 || p.foreground["specialist"] != 1 {
		t.Fatal("specialist endpoint was not reserved exactly once")
	}
	p.mu.Unlock()
	resume := SuspendInteractive(ctx)
	l.extend([]string{"another"})
	resume()
	p.mu.Lock()
	if p.foreground["another"] != 1 {
		t.Fatal("endpoint added while suspended was not reserved on resume")
	}
	p.mu.Unlock()
	finish()
}
