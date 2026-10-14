package llm

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelsCountAsDownAfterRepeatedUnreachableFailuresAndUpAfterOneSuccess(t *testing.T) {
	r := &Router{}
	var events atomic.Int32
	var lastDown atomic.Bool
	r.OnHealth(func(down bool, reason string) { events.Add(1); lastDown.Store(down) })

	// failures about the request do not count
	for i := 0; i < 5; i++ {
		r.noteResult(fmt.Errorf("%w: too long", ErrContextLength))
		r.noteResult(&APIError{Status: 400, Body: "bad request"})
		r.noteResult(context.Canceled)
	}
	if d, _, _ := r.Down(); d {
		t.Fatal("request errors are not an outage")
	}
	for i := 0; i < downAfter-1; i++ {
		r.noteResult(errors.New("lmstudio: No models loaded. Please load a model"))
	}
	if d, _, _ := r.Down(); d {
		t.Fatal("not yet: a couple of failures can be bad luck")
	}
	r.noteResult(&APIError{Status: 502, Body: "bad gateway"})
	d, why, _ := r.Down()
	if !d || why == "" {
		t.Fatalf("down with a reason expected: %v %q", d, why)
	}
	r.noteResult(errors.New("connection refused")) // still down: no second announcement
	r.noteResult(nil)
	if d, _, _ := r.Down(); d {
		t.Fatal("one success brings the models back")
	}
	deadline := time.Now().Add(2 * time.Second)
	for events.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if events.Load() != 2 || lastDown.Load() {
		t.Fatalf("exactly one 'down' and one 'up' event expected, got %d", events.Load())
	}
}
