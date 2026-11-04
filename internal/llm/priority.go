package llm

import (
	"context"
	"sync"
	"time"
)

// inferencePriority gives foreground calls precedence on each endpoint, and
// allows at most one background inference per endpoint. A waiting background
// call becomes eligible alongside foreground work after a bounded wait.
// In-flight calls are never interrupted. Caller deadlines include queue time.
type inferencePriority struct {
	mu         sync.Mutex
	foreground map[string]int
	background map[string]bool
	changed    chan struct{}
	maxWait    time.Duration
}

func (p *inferencePriority) acquire(ctx context.Context, endpoint string, background bool) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.changed == nil {
		p.changed = make(chan struct{})
		p.foreground = map[string]int{}
		p.background = map[string]bool{}
	}
	if !background {
		p.foreground[endpoint]++
		p.mu.Unlock()
		return p.release(endpoint, false), nil
	}
	wait := p.maxWait
	if wait <= 0 {
		wait = 5 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline) / 4
		if remaining < wait {
			wait = remaining
		}
	}
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	fair := false
	for p.background[endpoint] || (p.foreground[endpoint] > 0 && !fair) {
		ch := p.changed
		p.mu.Unlock()
		if fair {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ch:
			}
		} else {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
				fair = true
			case <-ch:
			}
		}
		p.mu.Lock()
	}
	p.background[endpoint] = true
	p.mu.Unlock()
	return p.release(endpoint, true), nil
}
func (p *inferencePriority) release(endpoint string, background bool) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if background {
				delete(p.background, endpoint)
			} else {
				p.foreground[endpoint]--
				if p.foreground[endpoint] == 0 {
					delete(p.foreground, endpoint)
				}
			}
			close(p.changed)
			p.changed = make(chan struct{})
		})
	}
}

type backgroundContextKey struct{}

// WithBackground marks a non-interactive task and its descendants as background work.
func WithBackground(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundContextKey{}, true)
}
func IsBackground(ctx context.Context) bool {
	b, _ := ctx.Value(backgroundContextKey{}).(bool)
	return b
}

func backgroundAction(action string) bool {
	switch action {
	case "icons", "memory_extract", "memory_dedupe", "memory_banks", "memory_sweep", "memory_analyze", "memory_reflect", "entities", "task_summary", "handover", "skill_tidy", "person_model", "standup", "jobs_review", "runbook":
		return true
	}
	return false
}
