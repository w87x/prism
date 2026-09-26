package app

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"prism/internal/settings"
)

// keepAwakeLoop stops macOS from idle-sleeping while agents are working. A sleeping Mac suspends the whole process:
// a task once sat frozen for almost two hours between two model calls and simply resumed on wake. While at least one
// run is active a `caffeinate -i -w <our pid>` holds the assertion (the -w makes it exit with us, even on a crash);
// it is released a minute after the last run ends. Off with Settings → Runtime "keep awake", and a no-op elsewhere.
func (a *App) keepAwakeLoop(ctx context.Context) {
	if runtime.GOOS != "darwin" {
		return
	}
	bin, err := exec.LookPath("caffeinate")
	if err != nil {
		return
	}
	var cmd *exec.Cmd
	release := func() {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		cmd = nil
	}
	defer release()
	var idleSince time.Time
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cfg := settings.Load(ctx, a.Settings, settings.KeyRuntime, settings.DefaultRuntime())
		busy := len(a.Engine.ActiveRuns()) > 0
		switch {
		case busy && !cfg.KeepAwakeOff:
			idleSince = time.Time{}
			if cmd == nil {
				c := exec.Command(bin, "-i", "-w", strconv.Itoa(os.Getpid()))
				if c.Start() == nil {
					cmd = c
					a.Logf("info", "power", "keeping the Mac awake while agents work")
				}
			}
		case cmd != nil:
			if idleSince.IsZero() {
				idleSince = time.Now()
			} else if cfg.KeepAwakeOff || time.Since(idleSince) > time.Minute {
				release()
				a.Logf("info", "power", "released the keep-awake hold")
			}
		}
	}
}
