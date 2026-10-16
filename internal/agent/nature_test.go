package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/testutil"
	"prism/internal/tools"
)

func TestPainReflexCutsOffAFailingToolAndLetsOthersAlone(t *testing.T) {
	var b toolBreaker
	for i := 0; i < 4; i++ {
		if tripped, _ := b.record("synology_fs_upload", false, "dial tcp: connection refused"); tripped {
			t.Fatalf("not yet at %d failures", i+1)
		}
	}
	if tripped, _ := b.record("synology_fs_upload", false, "dial tcp: connection refused"); !tripped {
		t.Fatal("five failures in a row trip it")
	}
	for i := 0; i < 10; i++ { // a call that was simply wrong says nothing about the tool
		if tripped, _ := b.record("file_read", false, "open /x/y: no such file or directory"); tripped {
			t.Fatal("bad paths and bad arguments must not switch a healthy tool off for everyone")
		}
	}
	if ok, msg := b.allow("synology_fs_upload"); ok || !strings.Contains(msg, "paused") {
		t.Fatalf("a tripped tool is refused with an explanation: %v %q", ok, msg)
	}
	if ok, _ := b.allow("web_fetch"); !ok {
		t.Fatal("other tools are unaffected")
	}
	for i := 0; i < 10; i++ { // delegation plumbing says nothing about a tool
		if tripped, _ := b.record("delegate", false, "x"); tripped {
			t.Fatal("exempt tools never trip")
		}
	}
	for i := 0; i < 6; i++ { // a mostly-working tool is left alone
		b.record("web_fetch", i%3 != 0, "timeout")
	}
	if ok, _ := b.allow("web_fetch"); !ok {
		t.Fatal("an occasional failure is not an outage")
	}
}

func TestImmuneMemoryFiresOnlyOnItsTrigger(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p := &Profile{Name: "Scout", Tools: []string{"browser_type", "clock"}}
	_ = h.e.AddAntibody(ctx, "Scout", []string{"avito", "price check"}, "When checking Avito prices, use the mobile site; the desktop one blocks bots.", "test")
	_ = h.e.AddAntibody(ctx, "*", []string{"tool:browser_type"}, "Clear a field before typing into it.", "test")
	_ = h.e.AddAntibody(ctx, "Other", []string{"avito"}, "This one belongs to another agent.", "test")
	note := h.e.antibodyNote(ctx, p, "Please do the Avito listing watch")
	if !strings.Contains(note, "mobile site") || !strings.Contains(note, "Clear a field") || strings.Contains(note, "another agent") {
		t.Fatalf("word trigger, tool trigger, and only for the right agent: %q", note)
	}
	if note := h.e.antibodyNote(ctx, &Profile{Name: "Scout", Tools: []string{"clock"}}, "what time is it"); note != "" {
		t.Fatalf("no trigger, no lesson: %q", note)
	}
	if note := h.e.antibodyNote(ctx, p, "run the price check now"); !strings.Contains(note, "mobile site") {
		t.Fatalf("a two-word trigger matches as a phrase: %q", note)
	}
	_, _ = h.e.DB.Exec(ctx, `UPDATE antibodies SET created_at=now()-interval '90 days', last_hit=NULL WHERE rule LIKE 'This one%'`)
	h.e.sweepNature(ctx)
	if abs, _ := h.e.Antibodies(ctx); len(abs) != 2 {
		t.Fatalf("a lesson that never fires expires: %d left", len(abs))
	}
}

func TestTrailsFadeAndShadeRouting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		h.e.depositTrail(ctx, "Scout", []string{"*"}, true)
	}
	h.e.depositTrail(ctx, "Poor", []string{"*"}, false)
	if h.e.trailBonus(ctx, "Scout") <= 0.2 || h.e.trailBonus(ctx, "Poor") >= 0 {
		t.Fatalf("success builds a trail, failure a negative one: %v %v", h.e.trailBonus(ctx, "Scout"), h.e.trailBonus(ctx, "Poor"))
	}
	_, _ = h.e.DB.Exec(ctx, `UPDATE trails SET updated_at=now()-interval '28 days' WHERE agent='Scout'`)
	if w := h.e.trailWeight(ctx, "Scout"); w < 1.4 || w > 1.6 {
		t.Fatalf("two half-lives cut 6 to about 1.5: %v", w)
	}
}

func TestTrustGrowsFromGoodWorkAndSkipsTheReviewer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var asked atomic.Int32
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		asked.Add(1)
		return testutil.Reply{Content: `{"verdict":"ask","reason":"unsure"}`}
	}
	_ = h.e.Settings.Set(ctx, settings.KeyReview, settings.Review{Enabled: true})
	env := &tools.Env{Agent: "Atlas"}
	write := &tools.Tool{Name: "note_save", Risk: tools.RiskWrite}
	h.e.noteTrust(ctx, "Atlas", []string{"note_save"}, true)
	if v, _ := h.e.preReview(ctx, env, write, "note_save", `{}`, "x"); v != "ask" || asked.Load() != 1 {
		t.Fatalf("unknown trust: the reviewer is consulted (%q, %d)", v, asked.Load())
	}
	for i := 0; i < 5; i++ {
		h.e.noteTrust(ctx, "Atlas", []string{"note_save"}, true)
	}
	if v, why := h.e.preReview(ctx, env, write, "note_save", `{}`, "x"); v != "approve" || asked.Load() != 1 || !strings.Contains(why, "trusted") {
		t.Fatalf("a good record goes through without bothering anyone: %q %q", v, why)
	}
	exec := &tools.Tool{Name: "runner", Risk: tools.RiskExec}
	for i := 0; i < 6; i++ {
		h.e.noteTrust(ctx, "Atlas", []string{"runner"}, true)
	}
	if v, _ := h.e.preReview(ctx, env, exec, "runner", `{}`, "x"); v == "approve" {
		t.Fatal("trust never waves code execution through")
	}
	h.e.noteTrustLoss(ctx, "Atlas", "note_save", 8) // the user said no, repeatedly
	if v, _ := h.e.preReview(ctx, env, write, "note_save", `{}`, "x"); v != "ask" {
		t.Fatalf("a bad record goes to the user: %q", v)
	}
	if trustLevelOf(5, 0) != TrustTrusted || trustLevelOf(2, 2) != TrustDistrusted || trustLevelOf(3, 0) != TrustUnknown {
		t.Fatal("trust levels")
	}
}

func TestThumbsDownStartsARepairThatIsCheckedLater(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		if strings.Contains(sys, "rated an AI agent's result as bad") {
			return testutil.Reply{Content: `{"rule":"When asked for prices, always say which store and date they are from.","triggers":["price","prices"]}`}
		}
		return testutil.Reply{Content: "ok"}
	}
	tk := finishedTask(t, h, "user", "user", tasks.Done, "It costs 10.")
	if err := h.e.RateTask(ctx, tk.ID, -1, "no store named"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE from_name='repair' AND to_agent='Atlas'`).Scan(&n)
	var open int
	_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM repairs WHERE task_id=$1 AND status='open'`, tk.ID).Scan(&open)
	if n != 1 || open != 1 {
		t.Fatalf("a repair task and a repair record: %d %d", n, open)
	}
	_ = h.e.RateTask(ctx, tk.ID, -1, "again")
	_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE from_name='repair'`).Scan(&n)
	if n != 1 {
		t.Fatal("one open repair per task")
	}
	waitFor(t, 5*time.Second, func() bool { abs, _ := h.e.Antibodies(ctx); return len(abs) == 1 })
	_, _ = h.e.DB.Exec(ctx, `UPDATE repairs SET due_at=now()-interval '1 hour'`)
	h.e.sweepRepairs(ctx)
	var st string
	_ = h.e.DB.QueryRow(ctx, `SELECT status FROM repairs WHERE task_id=$1`, tk.ID).Scan(&st)
	if st != "held" {
		t.Fatalf("no further trouble: it held, got %q", st)
	}
}

func TestHandoverNoteIsWrittenAndCarriedToWhoContinues(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		return testutil.Reply{Content: "State: half done.\nDone: 3 of 8 files renamed.\nWatch out: the NAS path has spaces.\nCares about: no data loss."}
	}
	tk := finishedTask(t, h, "user", "user", tasks.Partial, "renamed some")
	h.e.writeHandover(ctx, tk)
	if !strings.Contains(h.e.handoverNote(ctx, tk.ID), "3 of 8 files") {
		t.Fatal("the note is stored")
	}
	next, err := h.e.ResolveTask(ctx, tk.ID, "continue", "")
	if err != nil || !strings.Contains(next.Input, "NAS path has spaces") {
		t.Fatalf("a continuation carries the note: %v %+v", err, next)
	}
}

func TestScoutLooksFirstAndCannotChangeAnything(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.e.Profiles.Save(ctx, Profile{Name: "Worker", Group: "Files", Description: "does file jobs", Soul: "You are Worker.", Tools: []string{"clock"}, Enabled: true}, ""); err != nil {
		t.Fatal(err)
	}
	var sawRecon atomic.Bool
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, last, _ := msgAt(req, -1)
		if strings.Contains(last, "[Recon]") {
			sawRecon.Store(true)
		}
		if strings.Contains(last, "Recon only") && call < 3 {
			return testutil.Reply{Tools: []llm.ToolCall{tc("c1", "artifact_save", map[string]any{"name": "x.txt", "content": "y"})}}
		}
		return testutil.Reply{Content: "Found 12 files; two have spaces in their names."}
	}
	tool, _ := h.e.Tools.Get("delegate")
	b, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"agent": "Worker", "instruction": "rename the files", "title": "rename", "scout": true}}})
	out, err := tool.Run(ctx, &tools.Env{Agent: "Atlas", Restricted: false}, b)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE title='Scout: rename' AND status='done'`).Scan(&n)
	if n != 1 || !sawRecon.Load() {
		t.Fatalf("a scout ran and its brief reached the real run: %d %v %q", n, sawRecon.Load(), out)
	}
	var arts int
	_ = h.e.DB.QueryRow(ctx, `SELECT count(*) FROM artifacts`).Scan(&arts)
	if arts != 0 {
		t.Fatal("the scout must not have changed anything")
	}
}

func TestPersonModelIsLearnedFromUseAndCharterReachesPrompts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		_, _ = h.e.DB.Exec(ctx, `INSERT INTO chat_messages(role,agent,text,channel,topic,created_at) VALUES('user','',$1,'web','',now()-make_interval(hours => $2))`, fmt.Sprintf("short note %d", i), i)
	}
	var mu sync.Mutex
	var prompt string
	get := func() string { mu.Lock(); defer mu.Unlock(); return prompt }
	set := func(v string) { mu.Lock(); prompt = v; mu.Unlock() }
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, last, _ := msgAt(req, -1)
		set(last)
		return testutil.Reply{Content: "The user writes briefly and in lowercase; reply in one or two sentences and skip preambles. Not to be interrupted overnight."}
	}
	text, err := h.e.RefreshPersonModel(ctx)
	if err != nil || !strings.Contains(text, "writes briefly") || !strings.Contains(get(), "RHYTHM") {
		t.Fatalf("%v %q", err, text)
	}
	if core := h.e.Memory.CoreText(ctx); !strings.Contains(core, "writes briefly") {
		t.Fatal("the profile is core memory, so every agent sees it")
	}
	_ = h.e.Settings.Set(ctx, settings.KeyCharter, settings.Charter{Text: "Never download anything without telling the user first."})
	h.fake.Handler = func(req map[string]any, call int) testutil.Reply {
		_, sys, _ := msgAt(req, 0)
		if strings.Contains(sys, "You are Atlas") {
			set(sys)
		}
		return testutil.Reply{Content: "ok"}
	}
	_ = h.e.UserMessage(ctx, UserMsg{Text: "hello", Channel: "web"})
	waitFor(t, 10*time.Second, func() bool {
		p := get()
		return strings.Contains(p, "the team charter") && strings.Contains(p, "Never download anything")
	})
}
