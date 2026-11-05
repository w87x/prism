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
		wait = 45 * time.Second
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

type interactiveContextKey struct{}

type interactiveLease struct {
	mu        sync.Mutex
	active    bool
	pauses    int
	gate      *inferencePriority
	endpoints []string
	releases  []func()
}

func (l *interactiveLease) hold() {
	for _, endpoint := range l.endpoints {
		release, _ := l.gate.acquire(context.Background(), endpoint, false)
		l.releases = append(l.releases, release)
	}
}
func (l *interactiveLease) drop() {
	for _, release := range l.releases {
		release()
	}
	l.releases = nil
}
func (l *interactiveLease) extend(endpoints []string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active {
		return false
	}
	for _, endpoint := range endpoints {
		found := false
		for _, existing := range l.endpoints {
			if existing == endpoint {
				found = true
				break
			}
		}
		if !found {
			l.endpoints = append(l.endpoints, endpoint)
			if l.pauses == 0 {
				release, _ := l.gate.acquire(context.Background(), endpoint, false)
				l.releases = append(l.releases, release)
			}
		}
	}
	return true
}
func interactiveFrom(ctx context.Context) *interactiveLease {
	l, _ := ctx.Value(interactiveContextKey{}).(*interactiveLease)
	return l
}
func IsInteractive(ctx context.Context) bool {
	l := interactiveFrom(ctx)
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active
}

// SuspendInteractive temporarily releases endpoint reservations while the user
// is being asked a question. Nested suspensions and concurrent finish are safe.
func SuspendInteractive(ctx context.Context) func() {
	l := interactiveFrom(ctx)
	if l == nil {
		return func() {}
	}
	l.mu.Lock()
	if !l.active {
		l.mu.Unlock()
		return func() {}
	}
	l.pauses++
	if l.pauses == 1 {
		l.drop()
	}
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.pauses--
			if l.active && l.pauses == 0 {
				l.hold()
			}
		})
	}
}

// BeginInteractive holds the primary endpoint of each model reference across
// a user task, including gaps between model calls. Calls in the same context are
// foreground, so a tool needed by that task cannot wait behind its own lease.
func (r *Router) BeginInteractive(ctx context.Context, refs ...string) (context.Context, func()) {
	var endpoints []string
	seen := map[string]bool{}
	for _, ref := range refs {
		targets, err := r.resolve(ctx, ref, "chat")
		if err != nil || len(targets) == 0 {
			continue
		}
		endpoint := targets[0].prov.BaseURL
		if !seen[endpoint] {
			seen[endpoint] = true
			endpoints = append(endpoints, endpoint)
		}
	}
	if l := interactiveFrom(ctx); l != nil && l.extend(endpoints) {
		return ctx, func() {}
	}
	return r.priority.beginInteractive(ctx, endpoints)
}

func (p *inferencePriority) beginInteractive(ctx context.Context, endpoints []string) (context.Context, func()) {
	if IsInteractive(ctx) {
		return ctx, func() {}
	}
	l := &interactiveLease{active: true, gate: p, endpoints: endpoints}
	l.hold()
	var once sync.Once
	return context.WithValue(ctx, interactiveContextKey{}, l), func() { once.Do(func() { l.mu.Lock(); defer l.mu.Unlock(); l.active = false; l.drop() }) }
}

func isBackgroundInference(ctx context.Context, action string) bool {
	return IsBackground(ctx) || (backgroundAction(action) && !IsInteractive(ctx))
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
	case "icons", "chat_naming", "chat_housekeeping", "background_summary", "promise_review", "memory_extract", "memory_dedupe", "memory_banks", "memory_sweep", "memory_analyze", "memory_reflect", "entities", "task_summary", "handover", "skill_tidy", "person_model", "standup", "jobs_review", "runbook":
		return true
	}
	return false
}
