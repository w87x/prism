// Package macos integrates with the macOS ecosystem: native notifications,
// clipboard, Reminders, Spotlight, `open`, speech — as tools and as a
// notification sink for agent-initiated messages.
package macos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"prism/internal/agent"
	"prism/internal/settings"
	"prism/internal/tools"
)

func run(ctx context.Context, stdin string, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// osascript runs an AppleScript with positional arguments (never interpolated → no injection).
func osascript(ctx context.Context, script string, args ...string) (string, error) {
	a := []string{}
	for _, l := range strings.Split(script, "\n") {
		a = append(a, "-e", l)
	}
	a = append(a, args...)
	return run(ctx, "", "osascript", a...)
}

// Available reports whether we are on macOS.
func Available() bool { return runtime.GOOS == "darwin" }

// Notify posts a native notification.
func Notify(ctx context.Context, title, message, sound string) error {
	script := "on run argv\nif (count of argv) > 2 then\ndisplay notification (item 2 of argv) with title (item 1 of argv) sound name (item 3 of argv)\nelse\ndisplay notification (item 2 of argv) with title (item 1 of argv)\nend if\nend run"
	args := []string{title, message}
	if sound != "" {
		args = append(args, sound)
	}
	_, err := osascript(ctx, script, args...)
	return err
}

// Sink delivers agent notices and asks as macOS notifications when the UI is not in use.
type Sink struct {
	Settings *settings.Store
	Clients  func() int // connected UI clients
}

func (s *Sink) enabled(ctx context.Context) (settings.MacOS, bool) {
	c := settings.Load(ctx, s.Settings, settings.KeyMacOS, settings.MacOS{Notifications: true, Sound: "Glass"})
	return c, Available() && c.Notifications
}

func (s *Sink) Notice(ctx context.Context, n agent.Notice) {
	c, ok := s.enabled(ctx)
	if !ok {
		return
	}
	// stay quiet when the user is looking at the UI, except for things that need attention
	if s.Clients() > 0 && n.Level != "attention" && n.Level != "warning" && n.Level != "error" {
		return
	}
	text := strings.NewReplacer("**", "", "`", "").Replace(n.Text)
	if r := []rune(text); len(r) > 240 {
		text = string(r[:240]) + "…"
	}
	_ = Notify(ctx, "PRISM · "+n.Agent, text, c.Sound)
}

func (s *Sink) AskUser(ctx context.Context, id int64, ag string, q tools.Question) {
	c, ok := s.enabled(ctx)
	if !ok || s.Clients() > 0 {
		return
	}
	_ = Notify(ctx, "PRISM · "+ag+" needs you", q.Text, c.Sound)
}

// RegisterTools installs the mac_* tools (no-ops off macOS).
func RegisterTools(reg *tools.Registry, st *settings.Store, h *Helper, dataDir string) {
	if !Available() {
		return
	}
	registerCalendar(reg, h)
	registerShortcuts(reg, dataDir)
	reg.Register(
		&tools.Tool{
			Name: "mac_notify", Category: "macos", Risk: tools.RiskWrite, Auto: true,
			Description: "Show a native macOS notification.",
			Params:      tools.Obj("message", tools.Str("title", "title"), tools.Str("message", "text")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Title, Message string }](raw)
				if err != nil {
					return "", err
				}
				if a.Title == "" {
					a.Title = "PRISM · " + env.Agent
				}
				c := settings.Load(ctx, st, settings.KeyMacOS, settings.MacOS{Sound: "Glass"})
				return "shown", Notify(ctx, a.Title, a.Message, c.Sound)
			},
		},
		&tools.Tool{
			Name: "mac_clipboard_get", Category: "macos", Risk: tools.RiskRead,
			Description: "Read the current clipboard text.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				if err := tools.ConfirmIfTainted(ctx, env, "mac_clipboard_get", "your clipboard"); err != nil {
					return "", err
				}
				return run(ctx, "", "pbpaste")
			},
		},
		&tools.Tool{
			Name: "mac_clipboard_set", Category: "macos", Risk: tools.RiskWrite,
			Description: "Put text on the clipboard.",
			Params:      tools.Obj("text", tools.Str("text", "text to copy")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Text string }](raw)
				if err != nil {
					return "", err
				}
				_, err = run(ctx, a.Text, "pbcopy")
				return "copied", err
			},
		},
		&tools.Tool{
			Name: "mac_open", Category: "macos", Risk: tools.RiskExec,
			Description: "Open a file, folder, URL or application with the default handler (macOS `open`). app selects a specific application.",
			Params:      tools.Obj("target", tools.Str("target", "path or URL"), tools.Str("app", "optional application name")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Target, App string }](raw)
				if err != nil {
					return "", err
				}
				args := []string{}
				if a.App != "" {
					args = append(args, "-a", a.App)
				}
				args = append(args, "--", a.Target)
				_, err = run(ctx, "", "open", args...)
				return "opened", err
			},
		},
		&tools.Tool{
			Name: "mac_say", Category: "macos", Risk: tools.RiskWrite, Auto: true,
			Description: "Speak text aloud with the system voice.",
			Params:      tools.Obj("text", tools.Str("text", "what to say")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Text string }](raw)
				if err != nil {
					return "", err
				}
				_, err = run(ctx, "", "say", "--", a.Text)
				return "spoken", err
			},
		},
		&tools.Tool{
			Name: "mac_spotlight", Category: "macos", Risk: tools.RiskRead,
			Description: "Search files with Spotlight (mdfind). Returns up to 25 paths.",
			Params:      tools.Obj("query", tools.Str("query", "Spotlight query or plain words"), tools.Str("dir", "restrict to a directory")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Query, Dir string }](raw)
				if err != nil {
					return "", err
				}
				args := []string{}
				if a.Dir != "" {
					args = append(args, "-onlyin", a.Dir)
				}
				args = append(args, a.Query)
				out, err := run(ctx, "", "mdfind", args...)
				if err != nil {
					return "", err
				}
				lines := strings.Split(out, "\n")
				if len(lines) > 25 {
					lines = append(lines[:25], fmt.Sprintf("…(+%d more)", len(lines)-25))
				}
				return strings.Join(lines, "\n"), nil
			},
		},
		&tools.Tool{
			Name: "mac_reminder_add", Category: "macos", Risk: tools.RiskWrite,
			Description: "Create a reminder in the macOS Reminders app (optionally with a due date-time, RFC3339) — appears on the user's iPhone too.",
			Params: tools.Obj("title", tools.Str("title", "reminder text"), tools.Str("due", "RFC3339 due date-time, optional"),
				tools.Str("list", "Reminders list name (default list if empty)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Title, Due, List string }](raw)
				if err != nil {
					return "", err
				}
				script := "on run argv\nset t to item 1 of argv\nset d to item 2 of argv\nset l to item 3 of argv\ntell application \"Reminders\"\nif l is \"\" then\nset targetList to default list\nelse\nset targetList to list l\nend if\nif d is \"\" then\nmake new reminder at end of reminders of targetList with properties {name:t}\nelse\nset dueDate to (current date)\nset year of dueDate to (word 1 of d as integer)\nset month of dueDate to (word 2 of d as integer)\nset day of dueDate to (word 3 of d as integer)\nset hours of dueDate to (word 4 of d as integer)\nset minutes of dueDate to (word 5 of d as integer)\nset seconds of dueDate to 0\nmake new reminder at end of reminders of targetList with properties {name:t, due date:dueDate}\nend if\nend tell\nend run"
				due := ""
				if a.Due != "" {
					t, err := time.Parse(time.RFC3339, a.Due)
					if err != nil {
						return "", errors.New("due must be RFC3339")
					}
					t = t.In(time.Local)
					due = fmt.Sprintf("%d %d %d %d %d", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute())
				}
				_, err = osascript(ctx, script, a.Title, due, a.List)
				return "reminder created", err
			},
		},
		&tools.Tool{
			Name: "mac_applescript", Category: "macos", Risk: tools.RiskExec,
			Description: "Run an AppleScript for anything else in the macOS ecosystem (Calendar, Notes, Mail drafts, Finder…). Powerful — use sparingly.",
			Params:      tools.Obj("script", tools.Str("script", "AppleScript source")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct{ Script string }](raw)
				if err != nil {
					return "", err
				}
				return osascript(ctx, a.Script)
			},
		},
	)
}
