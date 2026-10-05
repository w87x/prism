package app

import (
	"encoding/json"
	"sync"
)

// liveFeed is the one shared buffer behind every "thinking" view. It observes the same run.* events the browser
// receives and keeps, per in-flight run, its start payload and the tail of what it streamed. A window opened in the
// middle of a run (a new tab, a reload, a phone joining) asks for this tail and so starts mid-thought instead of
// blank; live events then carry on from there.
type liveFeed struct {
	mu   sync.Mutex
	runs map[int64]*liveRun
}

type liveRun struct {
	start  map[string]any
	usage  map[string]any
	events []map[string]any // {"type": "run.delta"|"run.tool"|"run.compacted", ...payload}
	chars  int              // streamed characters currently held
}

const (
	liveMaxChars  = 6000 // streamed text kept per run
	liveMaxEvents = 120  // tool lines and the like kept per run
)

// LiveEvent is one replayable entry of a run's tail.
type LiveEvent = map[string]any

// LiveRun is what a joining window needs to rebuild a run it did not see start.
type LiveRun struct {
	Start  map[string]any `json:"start"`
	Usage  map[string]any `json:"usage,omitempty"`
	Events []LiveEvent    `json:"events"`
}

func runID(m map[string]any) (int64, bool) {
	switch v := m["run"].(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	}
	return 0, false
}

func (f *liveFeed) observe(typ string, data any) {
	switch typ {
	case "run.start", "run.delta", "run.tool", "run.compacted", "run.usage", "run.end":
	default:
		return
	}
	var m map[string]any
	if mm, ok := data.(map[string]any); ok {
		m = mm
	} else { // run.start carries a struct
		b, err := json.Marshal(data)
		if err != nil || json.Unmarshal(b, &m) != nil {
			return
		}
	}
	id, ok := runID(m)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runs == nil {
		f.runs = map[int64]*liveRun{}
	}
	switch typ {
	case "run.start":
		f.runs[id] = &liveRun{start: m}
		return
	case "run.end":
		delete(f.runs, id)
		return
	}
	r := f.runs[id]
	if r == nil {
		return
	}
	switch typ {
	case "run.usage":
		r.usage = m
	case "run.delta":
		kind, _ := m["kind"].(string)
		text, _ := m["text"].(string)
		if n := len(r.events); n > 0 && kind != "break" && r.events[n-1]["type"] == "run.delta" && r.events[n-1]["kind"] == kind {
			prev, _ := r.events[n-1]["text"].(string)
			r.events[n-1]["text"] = prev + text // a stream of tokens is one entry, not thousands
		} else {
			e := LiveEvent{"type": typ, "kind": kind}
			if kind != "break" {
				e["text"] = text
			}
			r.events = append(r.events, e)
		}
		r.chars += len(text)
	default: // run.tool, run.compacted
		e := LiveEvent{"type": typ}
		for k, v := range m {
			e[k] = v
		}
		r.events = append(r.events, e)
	}
	r.trim()
}

// trim drops the oldest material once a run holds more than its budget.
func (r *liveRun) trim() {
	for r.chars > liveMaxChars && len(r.events) > 0 {
		t, _ := r.events[0]["text"].(string)
		over := r.chars - liveMaxChars
		if r.events[0]["type"] == "run.delta" && over < len(t) {
			r.events[0]["text"] = t[over:]
			r.chars -= over
			break
		}
		r.chars -= len(t)
		r.events = r.events[1:]
	}
	if len(r.events) > liveMaxEvents {
		r.events = r.events[len(r.events)-liveMaxEvents:]
	}
}

// Live returns the tails of every in-flight run, keyed by run id.
func (a *App) Live() map[int64]LiveRun {
	f := &a.live
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int64]LiveRun, len(f.runs))
	for id, r := range f.runs {
		ev := make([]LiveEvent, len(r.events))
		for i, e := range r.events {
			c := make(LiveEvent, len(e))
			for k, v := range e {
				c[k] = v
			}
			ev[i] = c
		}
		out[id] = LiveRun{Start: r.start, Usage: r.usage, Events: ev}
	}
	return out
}
