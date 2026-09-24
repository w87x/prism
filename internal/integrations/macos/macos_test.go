package macos

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"prism/internal/tools"
)

// fakeHelper is a shell script standing in for prism-eventkit: it logs "<command>\t<json arg>" and answers
// with <dir>/<command>.json (or {}).
func fakeHelper(t *testing.T, answers map[string]string) (*Helper, func() []string) {
	t.Helper()
	dir := t.TempDir()
	for cmd, body := range answers {
		if err := os.WriteFile(filepath.Join(dir, cmd+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "fake")
	script := "#!/bin/sh\nprintf '%s\\t%s\\n' \"$1\" \"$2\" >> '" + dir + "/log'\nif [ -f '" + dir + "/'\"$1\".json ]; then cat '" + dir + "/'\"$1\".json; else echo '{}'; fi\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := func() []string {
		b, _ := os.ReadFile(filepath.Join(dir, "log"))
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	return &Helper{Bin: bin}, calls
}

func runTool(t *testing.T, reg *tools.Registry, name string, args any, env *tools.Env) (string, error) {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("tool %s is not registered", name)
	}
	b, _ := json.Marshal(args)
	if env == nil {
		env = &tools.Env{Agent: "Atlas"}
	}
	return tool.Run(context.Background(), env, b)
}

const meeting = `{"events":[{"id":"EV1","title":"Standup","start":"2026-09-22T10:00:00+03:00","end":"2026-09-22T10:15:00+03:00","calendar":"Work","recurring":true,"location":"Zoom","attendees":["Ann","Bob"]},
{"id":"EV2","title":"Holiday","start":"2026-09-23T00:00:00+03:00","end":"2026-09-24T00:00:00+03:00","all_day":true,"calendar":"Personal"}],"total":2}`

func TestCalendarToolsTalkToTheHelper(t *testing.T) {
	h, calls := fakeHelper(t, map[string]string{"events": meeting,
		"get-event":    `{"event":{"id":"EV1","title":"Standup","start":"2026-09-22T10:00:00+03:00","end":"2026-09-22T10:15:00+03:00","calendar":"Work","recurring":true}}`,
		"delete-event": `{"deleted":{"id":"EV1","title":"Standup","start":"2026-09-22T10:00:00+03:00","end":"2026-09-22T10:15:00+03:00","calendar":"Work"}}`,
		"update-event": `{"event":{"id":"EV1","title":"Standup (moved)","start":"2026-09-22T11:00:00+03:00","end":"2026-09-22T11:15:00+03:00","calendar":"Work"}}`,
		"add-event":    `{"event":{"id":"EV9","title":"Dentist","start":"2026-09-25T09:30:00+03:00","end":"2026-09-25T10:30:00+03:00","calendar":"Personal"}}`})
	reg := tools.NewRegistry(nil)
	registerCalendar(reg, h)

	// listing: one line per event with what the model needs to act on it, and repeating/all-day flags
	out, err := runTool(t, reg, "calendar_events", map[string]any{"from": "2026-09-22", "to": "2026-09-24", "query": "it's a \"quoted\" query\nwith a newline"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Standup [Work] @ Zoom (repeats)", "id=EV1 start=2026-09-22T10:00:00+03:00", "with: Ann, Bob", "Holiday [Personal]", "(all day)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// the query travels as ONE argument, byte for byte: nothing is interpolated into a script
	call := calls()[0]
	arg := call[strings.Index(call, "\t")+1:]
	var sent map[string]any
	if err := json.Unmarshal([]byte(arg), &sent); err != nil || sent["query"] != "it's a \"quoted\" query\nwith a newline" || sent["to"] != "2026-09-24" {
		t.Fatalf("argument mangled: %q → %v (%v)", arg, sent, err)
	}

	// adding maps the single alert to the helper's list
	if out, err := runTool(t, reg, "calendar_add", map[string]any{"title": "Dentist", "start": "2026-09-25T09:30", "alert_min": 15, "calendar": ""}, nil); err != nil || !strings.Contains(out, "Created:") {
		t.Fatalf("add: %q %v", out, err)
	}
	last := calls()[len(calls())-1]
	if !strings.HasPrefix(last, "add-event\t") || !strings.Contains(last, `"alerts_min":[15]`) || strings.Contains(last, `"calendar"`) {
		t.Fatalf("add payload: %s", last)
	}

	// updating: "start" names the occurrence, "new_start" is the move
	if _, err := runTool(t, reg, "calendar_update", map[string]any{"id": "EV1", "start": "2026-09-22T10:00:00+03:00", "new_start": "2026-09-22T11:00", "title": "Standup (moved)", "span": "this"}, nil); err != nil {
		t.Fatal(err)
	}
	last = calls()[len(calls())-1]
	var up map[string]any
	_ = json.Unmarshal([]byte(last[strings.Index(last, "\t")+1:]), &up)
	if up["occurrence"] != "2026-09-22T10:00:00+03:00" || up["start"] != "2026-09-22T11:00" || up["title"] != "Standup (moved)" || up["span"] != "this" || up["id"] != "EV1" {
		t.Fatalf("update payload: %v", up)
	}
}

// Deleting always asks, even for an armed tool, and shows what will be deleted; a "no" never reaches the helper.
func TestDeletesAlwaysAsk(t *testing.T) {
	h, calls := fakeHelper(t, map[string]string{
		"get-event":       `{"event":{"id":"EV1","title":"Dentist","start":"2026-09-25T09:30:00+03:00","end":"2026-09-25T10:30:00+03:00","calendar":"Personal"}}`,
		"get-reminder":    `{"reminder":{"id":"R1","title":"Call mum","list":"Home","due":"2026-09-25T18:00:00+03:00","due_has_time":true}}`,
		"delete-event":    `{"deleted":{"id":"EV1","title":"Dentist","start":"2026-09-25T09:30:00+03:00","end":"2026-09-25T10:30:00+03:00","calendar":"Personal"}}`,
		"delete-reminder": `{"deleted":{}}`})
	reg := tools.NewRegistry(nil)
	registerCalendar(reg, h)
	var asked []string
	answer := "deny"
	env := &tools.Env{Agent: "Atlas", Ask: func(ctx context.Context, q tools.Question) (string, error) {
		asked = append(asked, q.Text)
		return answer, nil
	}}
	countDeletes := func() int {
		n := 0
		for _, c := range calls() {
			if strings.HasPrefix(c, "delete-") {
				n++
			}
		}
		return n
	}
	if _, err := runTool(t, reg, "calendar_delete", map[string]any{"id": "EV1"}, env); err == nil || countDeletes() != 0 {
		t.Fatalf("a denied delete must not run: %v", err)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "Dentist") || !strings.Contains(asked[0], "Personal") {
		t.Fatalf("the question must say what is deleted: %v", asked)
	}
	answer = "allow"
	if out, err := runTool(t, reg, "calendar_delete", map[string]any{"id": "EV1"}, env); err != nil || !strings.Contains(out, "Deleted") || countDeletes() != 1 {
		t.Fatalf("approved delete: %q %v", out, err)
	}
	if _, err := runTool(t, reg, "reminders_delete", map[string]any{"id": "R1"}, env); err != nil || countDeletes() != 2 || !strings.Contains(asked[len(asked)-1], "Call mum") {
		t.Fatalf("reminder delete: %v asked=%v", err, asked)
	}
	// nobody to ask (an unattended run): refuse rather than delete
	if _, err := runTool(t, reg, "calendar_delete", map[string]any{"id": "EV1"}, &tools.Env{Agent: "Atlas"}); err == nil {
		t.Fatal("with no one to ask, deleting must be refused")
	}
}

func TestHelperErrorsAreReadable(t *testing.T) {
	h, _ := fakeHelper(t, map[string]string{"events": `{"error":"access to Calendars was not granted"}`})
	reg := tools.NewRegistry(nil)
	registerCalendar(reg, h)
	_, err := runTool(t, reg, "calendar_events", map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "access to Calendars was not granted") {
		t.Fatalf("%v", err)
	}
	if out, _ := runTool(t, reg, "reminders_list", map[string]any{}, nil); out != "No reminders." {
		t.Fatalf("%q", out)
	}
}

func TestReminderFormatting(t *testing.T) {
	got := describeReminder(reminder{ID: "R1", Title: "Pay rent", List: "Home", Due: "2026-10-01T00:00:00+03:00", Priority: 1, Notes: "  bank\napp "}, true)
	for _, want := range []string{"[ ] Pay rent (Home)", "due Thu 1 Oct", "❗", "id=R1", "notes: bank app"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if !strings.Contains(describeReminder(reminder{Title: "x", Completed: true}, false), "[x]") {
		t.Error("completed box")
	}
}

// ── shortcuts ───────────────────────────────────────────────────────────────

func TestShortcutNameResolution(t *testing.T) {
	names := []string{"Morning routine", "Text mum", "Text dad", "Open Work Apps"}
	for in, want := range map[string]string{"Text mum": "Text mum", "text MUM": "Text mum", "morning": "Morning routine", "work apps": "Open Work Apps"} {
		if got, err := resolveShortcut(in, names); err != nil || got != want {
			t.Errorf("%q → %q %v", in, got, err)
		}
	}
	if _, err := resolveShortcut("text", names); err == nil || !strings.Contains(err.Error(), "several") {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := resolveShortcut("nope", names); err == nil || !strings.Contains(err.Error(), "Morning routine") {
		t.Errorf("unknown must list what exists: %v", err)
	}
}

func TestShortcutWorkflowIsValidAndStrict(t *testing.T) {
	steps := []Step{{Do: "notify", Title: "Hi", Text: "Fish & <chips>"}, {Do: "wait", Seconds: 2.5}, {Do: "open_url", URL: "https://example.com/?a=1&b=2"}, {Do: "shell", Script: "echo \"hi\" >&2"}}
	wf, err := buildWorkflow(steps)
	if err != nil {
		t.Fatal(err)
	}
	acts := wf["WFWorkflowActions"].([]any)
	if len(acts) != 5 { // open_url is two actions: the address, then opening it
		t.Fatalf("%d actions", len(acts))
	}
	x := plistXML(wf)
	if !strings.Contains(x, "Fish &amp; &lt;chips&gt;") {
		t.Fatalf("text must be XML-escaped:\n%s", x)
	}
	if runtime.GOOS == "darwin" {
		f := filepath.Join(t.TempDir(), "x.plist")
		_ = os.WriteFile(f, []byte(x), 0o644)
		if out, err := exec.Command("plutil", "-lint", f).CombinedOutput(); err != nil {
			t.Fatalf("plutil rejects the plist: %s", out)
		}
	}
	for _, bad := range [][]Step{nil, {{Do: "notify"}}, {{Do: "open_url", URL: "javascript:alert(1)"}}, {{Do: "wait", Seconds: 99999}}, {{Do: "rm_rf"}}, {{Do: "shell", Script: " "}}} {
		if _, err := buildWorkflow(bad); err == nil {
			t.Errorf("%+v must be refused", bad)
		}
	}
}

// The embedded Swift helper must compile and start. This does not touch Calendar or Reminders data:
// selftest only reads the permission status.
func TestEmbeddedHelperCompiles(t *testing.T) {
	if runtime.GOOS != "darwin" || testing.Short() {
		t.Skip("macOS only")
	}
	if _, err := exec.LookPath("swiftc"); err != nil {
		t.Skip("no Swift compiler")
	}
	data := t.TempDir()
	h := NewHelper(data)
	var r struct {
		OK      bool `json:"ok"`
		Version int  `json:"version"`
	}
	if err := h.Run(context.Background(), "selftest", nil, &r); err != nil || !r.OK || r.Version < 1 {
		t.Fatalf("selftest: %+v %v", r, err)
	}
	if err := h.Run(context.Background(), "no-such-command", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("errors must come through: %v", err)
	}
	// the second call reuses the compiled binary
	if _, ok := h.Cached(); !ok {
		t.Fatal("binary should be cached")
	}
	if !NewHelper(data).CanBuild() {
		t.Fatal("CanBuild must see the cached binary")
	}
}

// Generating and signing a shortcut needs the `shortcuts` tool and a network connection to Apple, so it only
// runs on request: PRISM_TEST_SIGN=1. It never opens the Shortcuts app.
func TestShortcutIsSignedByApple(t *testing.T) {
	if os.Getenv("PRISM_TEST_SIGN") == "" || runtime.GOOS != "darwin" {
		t.Skip("set PRISM_TEST_SIGN=1 on a Mac to run")
	}
	dir := t.TempDir()
	p, err := createShortcut(context.Background(), dir, "PRISM test", []Step{{Do: "notify", Title: "PRISM", Text: "It works"}, {Do: "wait", Seconds: 1}}, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) < 100 {
		t.Fatalf("signed file: %d bytes, %v", len(b), err)
	}
	t.Logf("signed shortcut: %d bytes, starts %q", len(b), b[:4])
	if _, err := os.Stat(filepath.Join(dir, "PRISM test.unsigned.shortcut")); err == nil {
		t.Error("the unsigned intermediate file must be removed")
	}
}
