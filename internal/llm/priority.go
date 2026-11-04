package llm

import (
	"context"
	"sync"
	"time"
)

// inferencePriority delays background calls on the same endpoint while foreground
// inference runs. It does not interrupt active calls. The bounded wait protects both
// background progress and the caller's own deadline.
type inferencePriority struct {
	mu         sync.Mutex
	foreground map[string]int
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
	for background && p.foreground[endpoint] > 0 {
		ch := p.changed
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return func() {}, nil
		case <-ch:
		}
		p.mu.Lock()
	}
	if !background {
		p.foreground[endpoint]++
	}
	p.mu.Unlock()
	return func() {
		if background {
			return
		}
		p.mu.Lock()
		p.foreground[endpoint]--
		if p.foreground[endpoint] == 0 {
			delete(p.foreground, endpoint)
		}
		close(p.changed)
		p.changed = make(chan struct{})
		p.mu.Unlock()
	}, nil
}
func backgroundAction(action string) bool {
	switch action {
	case "icons", "memory_extract", "memory_dedupe", "memory_banks", "memory_sweep", "memory_analyze", "memory_reflect", "entities", "task_summary", "handover", "skill_tidy", "person_model", "standup", "jobs_review", "runbook":
		return true
	}
	return false
}
