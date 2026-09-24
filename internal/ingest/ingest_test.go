package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/memory"
	"prism/internal/testutil"
)

const tgJSON = `{"name":"Work chat","type":"private_supergroup","messages":[
 {"id":1,"type":"service","date":"2024-03-01T09:00:00","from":"Danil","text":""},
 {"id":2,"type":"message","date":"2024-03-01T09:01:00","from":"Danil","text":"We ship the billing rewrite on April 12."},
 {"id":3,"type":"message","date":"2024-03-01T09:02:00","from":"Olga","text":[{"type":"bold","text":"Agreed"}," - I will own the migration."]},
 {"id":4,"type":"message","date":"2024-03-05T18:00:00","from":"Olga","text":"The staging DB is on Postgres 16."},
 {"id":5,"type":"message","date":"2024-03-05T18:01:00","from":"Ivan","text":"ok"}
]}`

func newIngest(t *testing.T) (*Service, *testutil.FakeLLM, string) {
	t.Helper()
	d := testutil.DB(t)
	fake := testutil.NewFakeLLM(t)
	fake.Handler = func(map[string]any, int) testutil.Reply { return testutil.Reply{Content: `{"relations":[]}`} }
	r, st := testutil.Setup(t, d, fake)
	mem := memory.New(d.Pool, r, st)
	dir := t.TempDir()
	return &Service{DB: d.Pool, Mem: mem, LLM: r, Settings: st, InboxDir: dir}, fake, dir
}

func TestDetectsTelegramJSONAndReadsItsMessages(t *testing.T) {
	s, _, dir := newIngest(t)
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(tgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := s.Inspect(context.Background(), "result.json")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != KindTelegram || info.Title != "Work chat" || info.Messages != 4 || len(info.Participants) != 3 {
		t.Fatalf("info = %+v", info)
	}
	if info.Participants[0].Name != "Olga" || info.Participants[0].Messages != 2 {
		t.Fatalf("participants = %+v", info.Participants)
	}
	if info.Windows != 2 { // a four-day silence starts a new slice
		t.Fatalf("windows = %d", info.Windows)
	}
}

func TestParsesTelegramHTMLAndWhatsAppAndPlainLogs(t *testing.T) {
	page := `<div class="page_header"><div class="content"><div class="text bold">Team room</div></div></div>
<div class="message default clearfix" id="message1"><div class="body"><div class="pull_right date details" title="02.03.2024 10:15:00 UTC+03:00">10:15</div><div class="from_name">Danil</div><div class="text">Release is on Friday<br>after lunch &amp; tests</div></div></div>
<div class="message default clearfix joined" id="message2"><div class="body"><div class="pull_right date details" title="02.03.2024 10:16:00 UTC+03:00">10:16</div><div class="text">also bump the version</div></div></div>
<div class="message service" id="message3"><div class="body details">Olga joined the group</div></div>`
	ms, title, ok := parseTelegramHTML([]byte(page))
	if !ok || title != "Team room" || len(ms) != 2 || ms[1].From != "Danil" || !strings.Contains(ms[0].Text, "after lunch & tests") || ms[0].At.Day() != 2 || ms[0].At.Month() != 3 {
		t.Fatalf("html: %+v title=%q ok=%v", ms, title, ok)
	}
	var wa strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&wa, "[12/03/24, 10:%02d:00] %s: message number %d\n", i, []string{"Anna", "Boris"}[i%2], i)
	}
	if ms, ok := parseWhatsApp(wa.String()); !ok || len(ms) != 12 || ms[0].From != "Anna" {
		t.Fatalf("whatsapp: %d %v", len(ms), ok)
	}
	var lg strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&lg, "%s: line %d of the talk\n", []string{"Me", "Them"}[i%2], i)
	}
	if ms, ok := parseNameLog(lg.String()); !ok || len(ms) != 12 {
		t.Fatalf("name log: %d %v", len(ms), ok)
	}
	if _, ok := parseNameLog("Just a paragraph of prose.\nAnother one: with a colon but no dialogue.\n"); ok {
		t.Fatal("prose is not a chat log")
	}
}

// The job reads each slice with the model, files facts about the user under the user bank and the rest under
// a bank named after the chat, keeps the source and a moderate trust, and can be resumed.
func TestIngestJobTeachesMemoryFromAChat(t *testing.T) {
	ctx := context.Background()
	s, fake, dir := newIngest(t)
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(tgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	prompts := 0
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		all := fmt.Sprint(req["messages"])
		if !strings.Contains(all, "You read one slice of a chat log") {
			return testutil.Reply{Content: `{"relations":[]}`}
		}
		prompts++
		if !strings.Contains(all, "staging DB") {
			return testutil.Reply{Content: `{"facts":[
				{"text":"Danil and Olga plan to ship the billing rewrite on 2024-04-12.","subject":"other","tags":["release"],"confidence":0.9},
				{"text":"Danil (ME) leads the billing rewrite at work.","subject":"user","confidence":0.8}]}`}
		}
		return testutil.Reply{Content: `{"facts":[{"text":"The staging database of the work chat runs Postgres 16.","subject":"other","confidence":0.7}]}`}
	}
	j, err := s.Start(ctx, StartReq{Ref: "result.json", Me: "Danil"})
	if err != nil {
		t.Fatal(err)
	}
	var got Job
	for i := 0; i < 100; i++ {
		got, _ = s.job(ctx, j.ID)
		if got.Status != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got.Status != "done" || got.Done != 2 || got.Total != 2 || got.Facts != 3 || prompts != 2 {
		t.Fatalf("job = %+v prompts=%d", got, prompts)
	}
	proj, _ := s.Mem.BankBySpec(ctx, "project:Work chat", "", false)
	usr, _ := s.Mem.BankBySpec(ctx, "user", "", false)
	pf, _ := s.Mem.Facts(ctx, proj.ID, "", false, 20, 0)
	uf, _ := s.Mem.Facts(ctx, usr.ID, "", false, 20, 0)
	if len(pf) != 2 || len(uf) != 1 {
		t.Fatalf("project facts %d, user facts %d", len(pf), len(uf))
	}
	for _, f := range append(pf, uf...) {
		if f.Confidence < 0.5 || f.Confidence > 0.8 || !strings.HasPrefix(f.Source, "document: result.json") {
			t.Fatalf("fact provenance: %+v", f)
		}
	}
}

// The inbox tells learned files from new ones, and a file can be removed (not while memory is learning from it).
func TestInboxFilesShowLearningStatusAndCanBeDeleted(t *testing.T) {
	ctx := context.Background()
	s, _, dir := newIngest(t)
	for _, n := range []string{"done.txt", "new.txt", "half.txt", "busy.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, st := range map[string]string{"done.txt": "done", "half.txt": "cancelled", "busy.txt": "running"} {
		if _, err := s.DB.Exec(ctx, `INSERT INTO memory_ingests(name,path,status,facts) VALUES($1,$2,$3,4)`, name, filepath.Join(dir, name), st); err != nil {
			t.Fatal(err)
		}
	}
	fs, err := s.Files(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]File{}
	for _, f := range fs {
		got[f.Name] = f
	}
	if got["done.txt"].Status != "learned" || got["done.txt"].Facts != 4 || got["new.txt"].Status != "new" || got["half.txt"].Status != "partial" || got["busy.txt"].Status != "learning" {
		t.Fatalf("files = %+v", got)
	}
	if err := s.Delete(ctx, "busy.txt", false); err == nil {
		t.Fatal("a file being learned must not be deleted")
	}
	if err := s.Delete(ctx, "../done.txt", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "done.txt")); !os.IsNotExist(err) {
		t.Fatal("file still there")
	}
	var n int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM memory_ingests WHERE name='done.txt'`).Scan(&n)
	if n != 0 {
		t.Fatal("the job history should be forgotten with the file")
	}
}

// New files that have settled are learned on their own (one job at a time); a chat in which the user cannot be
// recognised waits for them; a fresh file and an already-seen one are left alone.
func TestScanLearnsSettledDocumentsAndFlagsChatsThatNeedYou(t *testing.T) {
	ctx := context.Background()
	s, fake, dir := newIngest(t)
	fake.Handler = func(req map[string]any, _ int) testutil.Reply {
		if strings.Contains(fmt.Sprint(req["messages"]), "You read one passage of a document") {
			return testutil.Reply{Content: `{"facts":[{"text":"The greenhouse project needs a permit from the city council.","subject":"other","confidence":0.8}]}`}
		}
		return testutil.Reply{Content: `{"relations":[]}`}
	}
	old := time.Now().Add(-10 * time.Minute)
	write := func(name, body string, mod time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, mod, mod)
	}
	write("notes.txt", "Greenhouse notes.\n\nThe greenhouse needs a permit from the city council before building starts.", old)
	write("chat.json", tgJSON, old)
	write("fresh.txt", "Something that was just dropped in and is still being written.", time.Now())

	n, err := s.Scan(ctx, true)
	if err != nil || n != 1 {
		t.Fatalf("scan started %d err=%v", n, err)
	}
	w := s.Waiting()
	if len(w) != 1 || w[0].Name != "chat.json" || w[0].Participants != 3 {
		t.Fatalf("waiting = %+v", w)
	}
	for i := 0; i < 100; i++ {
		if js, _ := s.Jobs(ctx); len(js) == 1 && js[0].Status != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	fs, _ := s.Files(ctx)
	got := map[string]string{}
	for _, f := range fs {
		got[f.Name] = f.Status
	}
	if got["notes.txt"] != "learned" || got["chat.json"] != "needs_you" || got["fresh.txt"] != "new" {
		t.Fatalf("statuses = %+v", got)
	}
	if n, _ := s.Scan(ctx, true); n != 0 {
		t.Fatalf("nothing new to start, got %d", n)
	}
	// once the user says who they are the chat is no longer waiting
	if _, err := s.Start(ctx, StartReq{Ref: "chat.json", Me: "Danil"}); err != nil {
		t.Fatal(err)
	}
	if len(s.Waiting()) != 0 {
		t.Fatalf("still waiting: %+v", s.Waiting())
	}
}
