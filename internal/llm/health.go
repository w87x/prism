package llm

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// Health. When no model can be reached — LM Studio has nothing loaded, the server is off, a provider is down — every job
// that tries fails the same way and fills the task list with identical errors. The router therefore watches the outcome of
// real calls: several failures in a row that are about reaching a model (not about one request being wrong) mark the models
// as down, and the first success marks them up again. The agent layer pauses background work while they are down and a
// light probe finds out when they are back.
const downAfter = 3 // consecutive unreachable-model failures before the models count as down

type health struct {
	mu     sync.Mutex
	streak int
	last   string
	down   bool
	since  time.Time
	on     func(down bool, reason string)
	ch     chan healthEvent // events are delivered in order by one goroutine
}

type healthEvent struct {
	down   bool
	reason string
}

// OnHealth registers a callback for every change between up and down (reason is the last error when going down).
func (r *Router) OnHealth(fn func(down bool, reason string)) {
	h := &r.health
	h.mu.Lock()
	h.on = fn
	if h.ch == nil {
		h.ch = make(chan healthEvent, 16)
		go func() {
			for ev := range h.ch {
				h.mu.Lock()
				cb := h.on
				h.mu.Unlock()
				if cb != nil {
					cb(ev.down, ev.reason)
				}
			}
		}()
	}
	h.mu.Unlock()
}

// Down reports whether the models are currently unreachable, why, and since when.
func (r *Router) Down() (bool, string, time.Time) {
	r.health.mu.Lock()
	defer r.health.mu.Unlock()
	return r.health.down, r.health.last, r.health.since
}

// Reachability errors are the ones that say "nothing answered here", as opposed to "this request was refused": a missing
// model, a refused connection, a server error, a timeout. A 4xx about the request itself, a context overflow or a cut-off reply
// do not count.
func unreachable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ErrContextLength) || errors.Is(err, ErrTruncated) {
		return false
	}
	low := strings.ToLower(err.Error())
	for _, s := range []string{"no models loaded", "no model loaded", "model is not loaded", "model not loaded", "connection refused", "no such host", "connection reset", "i/o timeout", "server misbehaving"} {
		if strings.Contains(low, s) {
			return true
		}
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status >= 500 || ae.Status == 404 && strings.Contains(low, "model")
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// noteResult feeds the outcome of one real call into the health state.
func (r *Router) noteResult(err error) {
	h := &r.health
	h.mu.Lock()
	var ev *healthEvent
	switch {
	case err == nil:
		h.streak = 0
		if h.down {
			h.down, ev = false, &healthEvent{}
		}
	case unreachable(err):
		h.streak++
		h.last = err.Error()
		if len(h.last) > 300 {
			h.last = h.last[:300]
		}
		if !h.down && h.streak >= downAfter {
			h.down, h.since = true, time.Now()
			ev = &healthEvent{true, h.last}
		}
	}
	if ev != nil && h.ch != nil {
		select {
		case h.ch <- *ev:
		default:
		}
	}
	h.mu.Unlock()
}

// Probe tries one tiny completion on the main model, falling back to the fast one: the way back up when nothing else is calling.
func (r *Router) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	var err error
	for _, role := range []string{"role:chat", "role:fast"} {
		if _, err = r.Chat(ctx, role, Request{Messages: []Message{{Role: "user", Content: "Reply with the single word: ok"}}, MaxTokens: 8}, nil); err == nil {
			return nil
		}
	}
	return err
}
