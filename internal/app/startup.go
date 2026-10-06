package app

import (
	"log"
	"sync"
	"time"
)

// Start-up progress. PRISM now listens before it has finished starting, so a browser that opens (or reconnects after
// a restart) can be told what is going on instead of staring at "connecting…": which step it is on, how long it has
// taken, and what finished before. The tracker has its own lock — App.Connect runs for a long time and everything
// else must stay responsive meanwhile.

type StartupStage struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	State  string `json:"state"` // running | done
	MS     int64  `json:"ms"`    // how long it took (so far, while running)
}

type StartupInfo struct {
	State     string         `json:"state"` // idle | starting | ready | failed | stopping
	ElapsedMS int64          `json:"elapsed_ms"`
	Stages    []StartupStage `json:"stages"`
	Error     string         `json:"error,omitempty"`
}

type startupTracker struct {
	mu       sync.Mutex
	state    string
	began    time.Time
	stageAt  time.Time
	stages   []StartupStage
	err      string
	lastEmit time.Time
}

func (t *startupTracker) snapshotLocked() StartupInfo {
	info := StartupInfo{State: t.state, Error: t.err, Stages: append([]StartupStage(nil), t.stages...)}
	if info.State == "" {
		info.State = "idle"
	}
	if !t.began.IsZero() {
		info.ElapsedMS = time.Since(t.began).Milliseconds()
	}
	if n := len(info.Stages); n > 0 && info.Stages[n-1].State == "running" {
		info.Stages[n-1].MS = time.Since(t.stageAt).Milliseconds()
	}
	return info
}

// Startup returns the current start-up progress.
func (a *App) Startup() StartupInfo {
	a.startup.mu.Lock()
	defer a.startup.mu.Unlock()
	return a.startup.snapshotLocked()
}

func (a *App) startupBegin() {
	t := &a.startup
	t.mu.Lock()
	t.state, t.began, t.stageAt, t.stages, t.err = "starting", time.Now(), time.Now(), nil, ""
	info := t.snapshotLocked()
	t.mu.Unlock()
	a.Hub.Broadcast("startup", info)
}

// startupStage marks the previous step done and a new one running; the same name with a new detail only updates the
// detail (throttled, since a long loop may report every batch).
func (a *App) startupStage(name, detail string) {
	t := &a.startup
	t.mu.Lock()
	if t.state != "starting" && t.state != "stopping" {
		t.mu.Unlock()
		return
	}
	now := time.Now()
	if n := len(t.stages); n > 0 && t.stages[n-1].Name == name {
		t.stages[n-1].Detail = detail
		if now.Sub(t.lastEmit) < 250*time.Millisecond {
			t.mu.Unlock()
			return
		}
	} else {
		if n > 0 {
			t.stages[n-1].State, t.stages[n-1].MS = "done", now.Sub(t.stageAt).Milliseconds()
		}
		t.stages = append(t.stages, StartupStage{Name: name, Detail: detail, State: "running"})
		t.stageAt = now
		log.Printf("[%s] %s%s", map[string]string{"starting": "start", "stopping": "stop"}[t.state], name, map[bool]string{true: " — " + detail}[detail != ""])
	}
	t.lastEmit = now
	info := t.snapshotLocked()
	t.mu.Unlock()
	a.Hub.Broadcast("startup", info)
}

func (a *App) startupFinish(err error) {
	t := &a.startup
	t.mu.Lock()
	now := time.Now()
	if n := len(t.stages); n > 0 && t.stages[n-1].State == "running" {
		t.stages[n-1].State, t.stages[n-1].MS = "done", now.Sub(t.stageAt).Milliseconds()
	}
	t.state = "ready"
	if err != nil {
		t.state, t.err = "failed", err.Error()
	}
	log.Printf("[start] %s after %s%s", t.state, now.Sub(t.began).Round(time.Millisecond), map[bool]string{true: ": " + t.err}[t.err != ""])
	info := t.snapshotLocked()
	t.mu.Unlock()
	a.Hub.Broadcast("startup", info)
}

// BeginShutdown tells every open page that PRISM is going down (and which step it is on), so a restart reads as
// "shutting down…" with details instead of a dead connection.
func (a *App) BeginShutdown() {
	t := &a.startup
	t.mu.Lock()
	t.state, t.began, t.stageAt, t.stages, t.err = "stopping", time.Now(), time.Now(), nil, ""
	info := t.snapshotLocked()
	t.mu.Unlock()
	a.Hub.Broadcast("startup", info)
}

// CurrentStage names the step start-up or shutdown is on right now ("" when idle).
func (a *App) CurrentStage() string {
	info := a.Startup()
	if n := len(info.Stages); n > 0 {
		return info.Stages[n-1].Name
	}
	return ""
}
