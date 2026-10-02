package consult

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/browser"
	"prism/internal/settings"
	"prism/internal/testutil"
)

// fakeCodex writes a stand-in for the CLI: it echoes stdin into the -o file, after an optional sleep.
func fakeCodex(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
all="$*"
out=""
while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift;; esac; shift; done
` + body
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newSvc(t *testing.T, cfg settings.Consult) *Service {
	t.Helper()
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	if err := st.Set(context.Background(), settings.KeyConsult, cfg); err != nil {
		t.Fatal(err)
	}
	return &Service{Settings: st, DataDir: t.TempDir(), Poll: 100 * time.Millisecond}
}

func TestCodexAnswersViaStdin(t *testing.T) {
	bin := fakeCodex(t, `cat > "$out.in"; { echo "PLAN:"; cat "$out.in"; } > "$out"`)
	s := newSvc(t, settings.Consult{CodexBin: bin})
	if got := s.DefaultProvider(context.Background()); got != "codex" {
		t.Fatalf("default provider %q", got)
	}
	j, err := s.Start(context.Background(), "codex", "tester", "design a cache", time.Minute, Options{})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Wait(context.Background(), j.ID, 20*time.Second)
	if snap.Status != "done" || !strings.Contains(snap.Answer, "PLAN:") || !strings.Contains(snap.Answer, "design a cache") {
		t.Fatalf("job: %+v", snap)
	}
	if ents, _ := os.ReadDir(filepath.Join(s.DataDir, "work", "consult")); len(ents) != 0 {
		t.Fatalf("scratch dir not cleaned: %v", ents)
	}
}

func TestCodexMissingAndFailing(t *testing.T) {
	s := newSvc(t, settings.Consult{CodexBin: "/nonexistent/codex"})
	j, _ := s.Start(context.Background(), "codex", "t", "q", time.Minute, Options{})
	snap, _ := s.Wait(context.Background(), j.ID, 10*time.Second)
	if snap.Status != "failed" || !strings.Contains(snap.Error, "codex") {
		t.Fatalf("missing: %+v", snap)
	}

	s = newSvc(t, settings.Consult{CodexBin: fakeCodex(t, `echo "not logged in" >&2; exit 3`)})
	j, _ = s.Start(context.Background(), "codex", "t", "q", time.Minute, Options{})
	snap, _ = s.Wait(context.Background(), j.ID, 10*time.Second)
	if snap.Status != "failed" || !strings.Contains(snap.Error, "not logged in") {
		t.Fatalf("failing: %+v", snap)
	}
}

func TestCancelAndBackgroundCollect(t *testing.T) {
	s := newSvc(t, settings.Consult{CodexBin: fakeCodex(t, `sleep 60; echo late > "$out"`)})
	j, _ := s.Start(context.Background(), "codex", "t", "slow one", time.Minute, Options{})
	time.Sleep(300 * time.Millisecond)
	if snap, _ := s.Wait(context.Background(), j.ID, 100*time.Millisecond); snap.Status != "running" {
		t.Fatalf("should still run: %+v", snap)
	}
	if err := s.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	snap, _ := s.Wait(context.Background(), j.ID, 20*time.Second)
	if snap.Status != "cancelled" || time.Since(start) > 10*time.Second {
		t.Fatalf("cancel: %+v after %s", snap, time.Since(start))
	}
	if err := s.Cancel(j.ID); err == nil {
		t.Fatal("cancelling a finished job should error")
	}
}

func TestJobTimeoutAndQueueing(t *testing.T) {
	s := newSvc(t, settings.Consult{CodexBin: fakeCodex(t, `sleep 60`)})
	j, _ := s.Start(context.Background(), "codex", "t", "q", 700*time.Millisecond, Options{})
	snap, _ := s.Wait(context.Background(), j.ID, 20*time.Second)
	if snap.Status != "failed" || !strings.Contains(snap.Error, "timed out") {
		t.Fatalf("timeout: %+v", snap)
	}

	s = newSvc(t, settings.Consult{CodexBin: fakeCodex(t, `sleep 1; echo ok > "$out"`)})
	a, _ := s.Start(context.Background(), "codex", "t", "one", time.Minute, Options{})
	b, _ := s.Start(context.Background(), "codex", "t", "two", time.Minute, Options{})
	t0 := time.Now()
	sa, _ := s.Wait(context.Background(), a.ID, 20*time.Second)
	sb, _ := s.Wait(context.Background(), b.ID, 20*time.Second)
	if sa.Status != "done" || sb.Status != "done" || gap(sa, sb) < 800*time.Millisecond {
		t.Fatalf("jobs on one provider must run one at a time: %+v %+v (%s)", sa, sb, time.Since(t0))
	}
}

func gap(a, b Job) time.Duration {
	if d := b.Finished.Sub(*a.Finished); d >= 0 {
		return d
	}
	return a.Finished.Sub(*b.Finished)
}

func TestCodexModelAndEffortArgs(t *testing.T) {
	bin := fakeCodex(t, `echo "$all" > "$out"`)
	s := newSvc(t, settings.Consult{CodexBin: bin, CodexModel: "default-model", CodexEffort: "low"})
	run := func(o Options) string {
		j, err := s.Start(context.Background(), "codex", "t", "q", time.Minute, o)
		if err != nil {
			t.Fatal(err)
		}
		snap, _ := s.Wait(context.Background(), j.ID, 20*time.Second)
		return snap.Answer
	}
	if a := run(Options{}); !strings.Contains(a, "-m default-model") || !strings.Contains(a, "model_reasoning_effort=low") {
		t.Fatalf("defaults not applied: %s", a)
	}
	if a := run(Options{Model: "gpt-x", Effort: "HIGH"}); !strings.Contains(a, "-m gpt-x") || !strings.Contains(a, "model_reasoning_effort=high") || strings.Contains(a, "default-model") {
		t.Fatalf("overrides not applied: %s", a)
	}
	for _, o := range []Options{{Effort: "turbo"}, {Model: "--evil"}, {Model: "a b"}} {
		if _, err := s.Start(context.Background(), "codex", "t", "q", 0, o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	if _, err := s.Start(context.Background(), "chatgpt", "t", "q", 0, Options{Effort: "high"}); err == nil {
		t.Fatal("chatgpt accepted effort")
	}
}

func TestSitesMerge(t *testing.T) {
	cfg := settings.Consult{Sites: map[string]settings.ConsultSite{
		"deepseek": {Answer: ".x"},
		"mine":     {URL: "https://chat.example/", Label: "Mine"},
		"kimi":     {Disabled: true},
		"Bad Name": {URL: "https://x/"},
		"codex":    {URL: "https://x/"},
	}}
	got := map[string]Site{}
	for _, s := range Sites(cfg) {
		got[s.Name] = s
	}
	if got["deepseek"].Answer != ".x" || got["deepseek"].Input != "textarea" || got["deepseek"].URL == "" {
		t.Fatalf("override should merge over the built-in: %+v", got["deepseek"])
	}
	if !got["chatgpt"].Verified || got["deepseek"].Verified {
		t.Fatal("only untouched chatgpt counts as verified")
	}
	if got["mine"].Label != "Mine" || got["mine"].Builtin {
		t.Fatalf("custom site: %+v", got["mine"])
	}
	if _, ok := got["Bad Name"]; ok || got["codex"].Name != "" {
		t.Fatal("invalid/reserved names must be ignored")
	}
	if _, err := findSite(cfg, "kimi"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled site: %v", err)
	}
	if _, err := findSite(cfg, "nope"); err == nil || !strings.Contains(err.Error(), "mine") {
		t.Fatalf("unknown site should list the available ones: %v", err)
	}
}

func TestAfterPrompt(t *testing.T) {
	if got := afterPrompt("menu\nYou: hello\nthere\nBot: hi!", "menu", "hello\nthere"); got != "Bot: hi!" {
		t.Fatalf("%q", got)
	}
	if got := afterPrompt("menu\nBot: hi!", "menu", "never shown"); got != "Bot: hi!" {
		t.Fatalf("%q", got)
	}
}

func TestStartValidation(t *testing.T) {
	s := newSvc(t, settings.Consult{})
	if _, err := s.Start(context.Background(), "bard", "t", "q", 0, Options{}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, err := s.Start(context.Background(), "codex", "t", "  ", 0, Options{}); err == nil {
		t.Fatal("empty prompt accepted")
	}
	if _, err := s.Start(context.Background(), "codex", "t", strings.Repeat("x", maxPrompt+1), 0, Options{}); err == nil {
		t.Fatal("oversize prompt accepted")
	}
}

// A stand-in for chatgpt.com: composer, send button, a stop button while "generating", then the answer.
const fakeChat = `<!doctype html><title>ChatGPT</title><body>
<div id="prompt-textarea" contenteditable="true"></div>
<button data-testid="send-button">send</button><div id="log"></div>
<script>
document.querySelector('[data-testid=send-button]').onclick=()=>{
  const q=document.getElementById('prompt-textarea').innerText;
  const stop=document.createElement('button'); stop.dataset.testid='stop-button'; stop.setAttribute('data-testid','stop-button'); document.body.append(stop);
  setTimeout(()=>{const m=document.createElement('div'); m.setAttribute('data-message-author-role','assistant'); m.innerText='ANSWER to: '+q; document.getElementById('log').append(m)},500);
  setTimeout(()=>stop.remove(),1200);
};
</script>`

const fakeLogin = `<!doctype html><title>ChatGPT</title><body><button data-testid="login-button">Log in</button>`

func TestChatGPTViaFakeSite(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	_ = st.Set(context.Background(), settings.KeyBrowser, settings.Browser{Headless: true})
	dir, err := os.MkdirTemp("", "prism-consult-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	br := &browser.Manager{Settings: st, DataDir: dir}
	if !br.Available() {
		t.Skip("no Chrome installed")
	}
	defer br.Stop()

	loggedIn := true
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if loggedIn {
			fmt.Fprint(w, fakeChat)
		} else {
			fmt.Fprint(w, fakeLogin)
		}
	}))
	defer site.Close()
	_ = st.Set(context.Background(), settings.KeyConsult, settings.Consult{Sites: map[string]settings.ConsultSite{"chatgpt": {URL: site.URL}}})
	s := &Service{Settings: st, Browser: br, DataDir: dir, Poll: 100 * time.Millisecond}

	j, err := s.Start(context.Background(), "chatgpt", "t", "make a plan\nwith two lines", 2*time.Minute, Options{})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Wait(context.Background(), j.ID, 90*time.Second)
	if snap.Status != "done" || !strings.Contains(snap.Answer, "ANSWER to: make a plan") || !strings.Contains(snap.Answer, "with two lines") {
		t.Fatalf("job: %+v", snap)
	}

	loggedIn = false
	j, _ = s.Start(context.Background(), "chatgpt", "t", "again", 2*time.Minute, Options{})
	snap, _ = s.Wait(context.Background(), j.ID, 90*time.Second)
	if snap.Status != "failed" || !strings.Contains(snap.Error, "not signed in") {
		t.Fatalf("logged-out: %+v", snap)
	}
}

// A site with no selectors at all: a bare textarea submitted with Enter, answer read from the page text.
const fakeBare = `<!doctype html><title>Bare</title><body><nav>menu · history</nav><textarea></textarea><div id="log"></div>
<script>
document.querySelector('textarea').addEventListener('keydown',e=>{
  if(e.key!=='Enter')return; e.preventDefault();
  const q=e.target.value; e.target.value='';
  const u=document.createElement('p'); u.textContent='You: '+q; document.getElementById('log').append(u);
  setTimeout(()=>{const a=document.createElement('p'); a.textContent='Bot says: '+q.toUpperCase(); document.getElementById('log').append(a)},400);
});
</script>`

func TestGenericSiteWithoutSelectors(t *testing.T) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	_ = st.Set(context.Background(), settings.KeyBrowser, settings.Browser{Headless: true})
	dir, err := os.MkdirTemp("", "prism-consult-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	br := &browser.Manager{Settings: st, DataDir: dir}
	if !br.Available() {
		t.Skip("no Chrome installed")
	}
	defer br.Stop()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, fakeBare) }))
	defer site.Close()
	_ = st.Set(context.Background(), settings.KeyConsult, settings.Consult{Sites: map[string]settings.ConsultSite{"bare": {URL: site.URL}}})
	s := &Service{Settings: st, Browser: br, DataDir: dir, Poll: 100 * time.Millisecond, Quiet: time.Second}

	j, err := s.Start(context.Background(), "bare", "t", "quiet please", 2*time.Minute, Options{})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Wait(context.Background(), j.ID, 90*time.Second)
	if snap.Status != "done" || snap.Answer != "Bot says: QUIET PLEASE" {
		t.Fatalf("job: %+v", snap)
	}
}

// A chat page whose replies are always PONG, with a user bubble echoing the prompt and a Stop button while busy.
const fakeChat2 = `<!doctype html><title>Chat2</title><body>
<div id="prompt-textarea" contenteditable="true" style="min-height:40px;border:1px solid"></div>
<button data-testid="send-button" style="width:40px;height:30px">send</button><div id="log"></div>
<script>
document.querySelector('[data-testid=send-button]').onclick=()=>{
  const e=document.getElementById('prompt-textarea'); const q=e.innerText; e.innerText='';
  const u=document.createElement('div'); u.setAttribute('data-message-author-role','user'); u.innerText=q; document.getElementById('log').append(u);
  const stop=document.createElement('button'); stop.setAttribute('data-testid','stop-button'); stop.style='width:40px;height:30px'; stop.textContent='stop'; document.body.append(stop);
  setTimeout(()=>{const m=document.createElement('div'); m.setAttribute('data-message-author-role','assistant'); m.innerHTML='<p>PONG</p>'; document.getElementById('log').append(m)},600);
  setTimeout(()=>stop.remove(),1400);
};
</script>`

type fakeLLM struct {
	calls   int
	badOnce bool
	users   []string
}

func (f *fakeLLM) CompleteJSON(_ context.Context, _, system, user string, out any) error {
	f.calls++
	f.users = append(f.users, user)
	var js string
	switch {
	case strings.Contains(system, "PONG"): // stage 2
		js = `{"answer":"[data-message-author-role=\"assistant\"]","stop":"[data-testid=\"stop-button\"]","why":"x"}`
	case f.badOnce && f.calls == 1:
		js = `{"input":"#does-not-exist","send":""}`
	default:
		js = `{"input":"#prompt-textarea","send":"[data-testid=\"send-button\"]","why":"x"}`
	}
	return json.Unmarshal([]byte(js), out)
}

func tuneEnv(t *testing.T, page string) (*Service, *settings.Store) {
	t.Helper()
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	_ = st.Set(context.Background(), settings.KeyBrowser, settings.Browser{Headless: true})
	dir, err := os.MkdirTemp("", "prism-consult-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	br := &browser.Manager{Settings: st, DataDir: dir}
	if !br.Available() {
		t.Skip("no Chrome installed")
	}
	t.Cleanup(br.Stop)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, page) }))
	t.Cleanup(site.Close)
	_ = st.Set(context.Background(), settings.KeyConsult, settings.Consult{Sites: map[string]settings.ConsultSite{"fake": {URL: site.URL}}})
	return &Service{Settings: st, Browser: br, DataDir: dir, Poll: 100 * time.Millisecond}, st
}

func TestTuneFindsSelectors(t *testing.T) {
	s, _ := tuneEnv(t, fakeChat2)
	f := &fakeLLM{}
	s.LLM = f
	rep, err := s.Tune(context.Background(), "fake")
	if err != nil || !rep.OK {
		t.Fatalf("tune: %v %+v", err, rep)
	}
	if rep.Site.Input != "#prompt-textarea" || !strings.Contains(rep.Site.Answer, "assistant") || !strings.Contains(rep.Site.Stop, "stop-button") {
		t.Fatalf("selectors: %+v", rep.Site)
	}
	if f.calls != 2 || !strings.Contains(f.users[0], "prompt-textarea") || !strings.Contains(f.users[1], "data-message-author-role") {
		t.Fatalf("model should have been shown the page structure: %d calls\n%v", f.calls, f.users)
	}
}

func TestTuneRecoversFromABadFirstGuess(t *testing.T) {
	s, _ := tuneEnv(t, fakeChat2)
	f := &fakeLLM{badOnce: true}
	s.LLM = f
	rep, err := s.Tune(context.Background(), "fake")
	if err != nil || !rep.OK {
		t.Fatalf("tune: %v %+v", err, rep)
	}
	if !strings.Contains(f.users[1], "#does-not-exist") {
		t.Fatalf("the failure should be fed back to the model:\n%s", f.users[1])
	}
}

func TestTuneNeedsAModel(t *testing.T) {
	s := newSvc(t, settings.Consult{})
	if _, err := s.Tune(context.Background(), "chatgpt"); err == nil {
		t.Fatal("expected an error without an LLM")
	}
}
