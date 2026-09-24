package macos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"prism/internal/tools"
)

// ── running the user's shortcuts ────────────────────────────────────────────

// ShortcutNames lists the user's shortcuts (for the settings page).
func ShortcutNames(ctx context.Context) ([]string, error) { return shortcutNames(ctx) }

func shortcutNames(ctx context.Context) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "shortcuts", "list").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("shortcuts list: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// resolveShortcut matches a requested name to an installed shortcut (exact, then case-insensitive), or explains what exists.
func resolveShortcut(want string, names []string) (string, error) {
	want = strings.TrimSpace(want)
	for _, n := range names {
		if n == want {
			return n, nil
		}
	}
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return n, nil
		}
	}
	var close []string
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), strings.ToLower(want)) {
			close = append(close, n)
		}
	}
	if len(close) == 1 {
		return close[0], nil
	}
	if len(close) > 1 {
		return "", fmt.Errorf("%q matches several shortcuts: %s", want, strings.Join(close, ", "))
	}
	if len(names) > 12 {
		names = names[:12]
	}
	return "", fmt.Errorf("no shortcut called %q (you have: %s)", want, strings.Join(names, ", "))
}

// runShortcut runs a shortcut and returns its text output. Nothing is interpolated into a shell: name and
// input travel as arguments and files.
func runShortcut(ctx context.Context, name, input string, timeout time.Duration) (string, error) {
	work, err := os.MkdirTemp("", "prism-shortcut-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	args := []string{"run", name, "--output-path", filepath.Join(work, "out.txt"), "--output-type", "public.plain-text"}
	if input != "" {
		in := filepath.Join(work, "in.txt")
		if err := os.WriteFile(in, []byte(input), 0o600); err != nil {
			return "", err
		}
		args = append(args, "--input-path", in)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(cctx, "shortcuts", args...)
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	b, _ := os.ReadFile(filepath.Join(work, "out.txt"))
	out := strings.TrimSpace(string(b))
	if len(out) > 8000 {
		out = out[:8000] + "\n…[output truncated]"
	}
	switch {
	case cctx.Err() != nil:
		return out, fmt.Errorf("the shortcut %q did not finish within %s", name, timeout)
	case runErr != nil:
		return out, fmt.Errorf("the shortcut %q failed: %v %s", name, runErr, strings.TrimSpace(stderr.String()))
	case out == "":
		return "The shortcut ran and produced no output.", nil
	}
	return out, nil
}

// ── creating a shortcut ─────────────────────────────────────────────────────

// Step is one action of a generated shortcut. Only a small, well-understood set is offered: Apple's action
// identifiers are undocumented and change between macOS versions, so anything outside it is refused.
type Step struct {
	Do      string  `json:"do"` // notify | alert | text | show | speak | open_url | wait | shell
	Title   string  `json:"title"`
	Text    string  `json:"text"`
	URL     string  `json:"url"`
	Script  string  `json:"script"`
	Seconds float64 `json:"seconds"`
}

func action(id string, params map[string]any) map[string]any {
	return map[string]any{"WFWorkflowActionIdentifier": "is.workflow.actions." + id, "WFWorkflowActionParameters": params}
}

// buildWorkflow turns steps into the workflow dictionary of a .shortcut file.
func buildWorkflow(steps []Step) (map[string]any, error) {
	if len(steps) == 0 {
		return nil, errors.New("give at least one step")
	}
	if len(steps) > 20 {
		return nil, errors.New("at most 20 steps")
	}
	var acts []any
	for i, s := range steps {
		bad := func(what string) error { return fmt.Errorf("step %d (%s): %s", i+1, s.Do, what) }
		switch s.Do {
		case "notify":
			if s.Text == "" {
				return nil, bad("text is required")
			}
			acts = append(acts, action("notification", map[string]any{"WFNotificationActionTitle": s.Title, "WFNotificationActionBody": s.Text, "WFNotificationActionSound": true}))
		case "alert":
			if s.Text == "" {
				return nil, bad("text is required")
			}
			acts = append(acts, action("alert", map[string]any{"WFAlertActionTitle": s.Title, "WFAlertActionMessage": s.Text, "WFAlertActionCancelButtonShown": false}))
		case "text":
			acts = append(acts, action("gettext", map[string]any{"WFTextActionText": s.Text}))
		case "show":
			if s.Text == "" {
				return nil, bad("text is required (or put a text step before it)")
			}
			acts = append(acts, action("showresult", map[string]any{"Text": s.Text}))
		case "speak":
			if s.Text == "" {
				return nil, bad("text is required")
			}
			acts = append(acts, action("speaktext", map[string]any{"WFText": s.Text}))
		case "open_url":
			if !regexp.MustCompile(`^https?://`).MatchString(s.URL) {
				return nil, bad("url must start with http:// or https://")
			}
			acts = append(acts, action("url", map[string]any{"WFURLActionURL": s.URL}), action("openurl", map[string]any{}))
		case "wait":
			if s.Seconds <= 0 || s.Seconds > 3600 {
				return nil, bad("seconds must be between 0 and 3600")
			}
			acts = append(acts, action("delay", map[string]any{"WFDelayTime": s.Seconds}))
		case "shell":
			if strings.TrimSpace(s.Script) == "" {
				return nil, bad("script is required")
			}
			acts = append(acts, action("runshellscript", map[string]any{"Script": s.Script, "Shell": "/bin/zsh", "InputMode": "to stdin"}))
		default:
			return nil, bad("unknown action; use notify, alert, text, show, speak, open_url, wait or shell")
		}
	}
	return map[string]any{
		"WFWorkflowActions":                    acts,
		"WFWorkflowClientVersion":              "2700",
		"WFWorkflowMinimumClientVersion":       900,
		"WFWorkflowMinimumClientVersionString": "900",
		"WFWorkflowImportQuestions":            []any{},
		"WFWorkflowInputContentItemClasses":    []any{"WFStringContentItem"},
		"WFWorkflowTypes":                      []any{},
		"WFWorkflowIcon":                       map[string]any{"WFWorkflowIconStartColor": 4282601983, "WFWorkflowIconGlyphNumber": 59511},
	}, nil
}

// plistXML renders nested maps/slices/strings/numbers/bools as an XML property list.
func plistXML(v any) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n" + `<plist version="1.0">`)
	var w func(any)
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	w = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			sb.WriteString("<dict>")
			for _, k := range keys {
				sb.WriteString("<key>" + esc.Replace(k) + "</key>")
				w(t[k])
			}
			sb.WriteString("</dict>")
		case []any:
			sb.WriteString("<array>")
			for _, e := range t {
				w(e)
			}
			sb.WriteString("</array>")
		case string:
			sb.WriteString("<string>" + esc.Replace(t) + "</string>")
		case bool:
			if t {
				sb.WriteString("<true/>")
			} else {
				sb.WriteString("<false/>")
			}
		case int:
			fmt.Fprintf(&sb, "<integer>%d</integer>", t)
		case float64:
			fmt.Fprintf(&sb, "<real>%g</real>", t)
		default:
			fmt.Fprintf(&sb, "<string>%s</string>", esc.Replace(fmt.Sprint(t)))
		}
	}
	w(v)
	sb.WriteString("</plist>\n")
	return sb.String()
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N} _.-]+`)

// createShortcut writes, signs and (optionally) opens a generated shortcut for import.
func createShortcut(ctx context.Context, dir, name string, steps []Step, open bool) (path string, err error) {
	wf, err := buildWorkflow(steps)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(unsafeName.ReplaceAllString(name, ""))
	if name == "" {
		return "", errors.New("give the shortcut a name")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	raw := filepath.Join(dir, name+".unsigned.shortcut")
	if err := os.WriteFile(raw, []byte(plistXML(wf)), 0o644); err != nil {
		return "", err
	}
	defer os.Remove(raw)
	// the Shortcuts app only imports binary property lists
	if out, err := exec.CommandContext(ctx, "plutil", "-convert", "binary1", raw).CombinedOutput(); err != nil {
		return "", fmt.Errorf("plutil: %v %s", err, strings.TrimSpace(string(out)))
	}
	signed := filepath.Join(dir, name+".shortcut")
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(cctx, "shortcuts", "sign", "--mode", "people-who-know-me", "--input", raw, "--output", signed).CombinedOutput(); err != nil {
		return "", fmt.Errorf("signing the shortcut failed (it needs a network connection and an iCloud account): %v %s", err, strings.TrimSpace(string(out)))
	}
	if open {
		if out, err := exec.CommandContext(ctx, "open", signed).CombinedOutput(); err != nil {
			return signed, fmt.Errorf("could not open it in Shortcuts: %v %s", err, strings.TrimSpace(string(out)))
		}
	}
	return signed, nil
}

func registerShortcuts(reg *tools.Registry, dataDir string) {
	reg.Register(
		&tools.Tool{
			Name: "shortcuts_list", Category: "macos", Risk: tools.RiskRead,
			Description: "List the user's Apple Shortcuts by name. Each one is something the user already automated: check it before doing a job by hand.",
			Params:      tools.Obj(""),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				names, err := shortcutNames(ctx)
				if err != nil {
					return "", err
				}
				if len(names) == 0 {
					return "The user has no shortcuts.", nil
				}
				return "- " + strings.Join(names, "\n- "), nil
			},
		},
		&tools.Tool{
			Name: "shortcuts_run", Category: "macos", Risk: tools.RiskExec,
			Description: "Run one of the user's Apple Shortcuts, optionally passing text as its input, and return its text output. A shortcut can do anything the user allowed it to (send messages, change settings…): only run ones that fit the request.",
			Params: tools.Obj("name", tools.Str("name", "shortcut name (see shortcuts_list)"), tools.Str("input", "text given to the shortcut as input (optional)"),
				tools.Int("timeout_s", "give up after this many seconds (default 120, max 600)")),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Name, Input string
					TimeoutS    int `json:"timeout_s"`
				}](raw)
				if err != nil {
					return "", err
				}
				names, err := shortcutNames(ctx)
				if err != nil {
					return "", err
				}
				name, err := resolveShortcut(a.Name, names)
				if err != nil {
					return "", err
				}
				t := time.Duration(a.TimeoutS) * time.Second
				if t <= 0 || t > 10*time.Minute {
					t = 2 * time.Minute
				}
				return runShortcut(ctx, name, a.Input, t)
			},
		},
		&tools.Tool{
			Name: "shortcuts_create", Category: "macos", Risk: tools.RiskExec,
			Description: "EXPERIMENTAL. Create a new Apple Shortcut from a few simple steps, sign it and open it in the Shortcuts app; the user finishes by clicking “Add Shortcut”. Use this only when the automation must live in Shortcuts itself (Siri, the share sheet, the iPhone). For anything time- or event-driven prefer PRISM's own schedules, intents and watches, which need no clicks. Steps run in order; actions: notify(title,text), alert(title,text), text(text), show(text), speak(text), open_url(url), wait(seconds), shell(script).",
			Params: tools.Obj("name,steps", tools.Str("name", "the shortcut's name"),
				tools.ObjList("steps", "actions in order", "do",
					tools.Enum("do", "action", "notify", "alert", "text", "show", "speak", "open_url", "wait", "shell"),
					tools.Str("title", "title (notify, alert)"), tools.Str("text", "message text"), tools.Str("url", "address (open_url)"),
					tools.Str("script", "zsh script (shell)"), tools.Num("seconds", "delay (wait)"))),
			Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
				a, err := tools.Decode[struct {
					Name  string
					Steps []Step
				}](raw)
				if err != nil {
					return "", err
				}
				p, err := createShortcut(ctx, filepath.Join(dataDir, "shortcuts"), a.Name, a.Steps, true)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Created and opened “%s” in the Shortcuts app — the user must click “Add Shortcut” to install it (the file is %s).", a.Name, p), nil
			},
		},
	)
}
