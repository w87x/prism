package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/agent"
	"prism/internal/memory"
	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/testutil"
	"prism/internal/tools"
)

type fakeTG struct {
	mu      sync.Mutex
	updates []map[string]any
	sent    []map[string]any
	methods []string
	nextMsg int
	file    []byte // what /file/bot…/<path> serves (a photo the user "sent")
	seen    [][]byte
	uploads []string // "<method>|<chat>|<thread>|<filename>|<bytes>" for multipart uploads (sendAudio/sendPhoto…)
}

func (f *fakeTG) push(u map[string]any) { f.mu.Lock(); f.updates = append(f.updates, u); f.mu.Unlock() }
func (f *fakeTG) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.sent {
		out = append(out, fmt.Sprintf("%v|%v|%v", s["chat_id"], s["message_thread_id"], s["text"]))
	}
	return out
}
func (f *fakeTG) sawSent(sub string) bool {
	for _, s := range f.sentTexts() {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func (f *fakeTG) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/file/bot") { // file download
			f.mu.Lock()
			b := f.file
			f.mu.Unlock()
			w.Write(b)
			return
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			_ = r.ParseMultipartForm(32 << 20)
			var name string
			var size int
			for _, fhs := range r.MultipartForm.File {
				name, size = fhs[0].Filename, int(fhs[0].Size)
			}
			f.mu.Lock()
			f.uploads = append(f.uploads, fmt.Sprintf("%s|%s|%s|%s|%d", method, r.FormValue("chat_id"), r.FormValue("message_thread_id"), name, size))
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 9}})
			return
		}
		var params map[string]any
		_ = json.NewDecoder(r.Body).Decode(&params)
		f.mu.Lock()
		f.methods = append(f.methods, method)
		var result any = true
		switch method {
		case "getMe":
			result = map[string]any{"username": "prism_bot"}
		case "getFile":
			result = map[string]any{"file_path": "photos/file_1.jpg"}
		case "getUpdates":
			ups := f.updates
			f.updates = nil
			f.mu.Unlock()
			if len(ups) == 0 {
				time.Sleep(150 * time.Millisecond)
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": append([]map[string]any{}, ups...)})
			return
		case "sendMessage":
			f.nextMsg++
			f.sent = append(f.sent, params)
			result = map[string]any{"message_id": f.nextMsg}
		case "createForumTopic":
			result = map[string]any{"message_thread_id": 77}
		}
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	})
}

// mkMsg builds a private-chat message update for handle().
func mkMsg(from int64, text string) *message {
	var m update
	b, _ := json.Marshal(msg(1, from, "private", 0, from, text))
	_ = json.Unmarshal(b, &m)
	return m.Message
}

func msg(id int64, chat int64, chatType string, thread int64, from int64, text string) map[string]any {
	m := map[string]any{"message_id": 1, "from": map[string]any{"id": from}, "chat": map[string]any{"id": chat, "type": chatType}, "text": text}
	if thread != 0 {
		m["message_thread_id"] = thread
		m["is_topic_message"] = true
	}
	return map[string]any{"update_id": id, "message": m}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPairingChatAndTopics(t *testing.T) {
	d := testutil.DB(t)
	fakeLLM := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fakeLLM)
	reg := tools.NewRegistry(d.Pool)
	e := agent.NewEngine(agent.Deps{DB: d.Pool, LLM: r, Tools: reg, Profiles: agent.NewProfileStore(d.Pool), Sessions: agent.NewSessionStore(d.Pool),
		Tasks: tasks.NewStore(d.Pool), Memory: memory.New(d.Pool, r, st), Settings: st})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.Profiles.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	tg := &fakeTG{}
	srv := httptest.NewServer(tg.handler())
	defer srv.Close()
	_ = st.Set(ctx, settings.KeyTelegram, settings.Telegram{Enabled: true, Token: "TOKEN", PairCode: "123456", PairExpires: time.Now().Add(time.Minute).Unix(), GroupID: -100, MirrorNotices: true})
	bot := &Bot{Settings: st, Engine: e, DB: d.Pool, APIBase: srv.URL}
	e.Sinks = append(e.Sinks, bot)
	go bot.Run(ctx)

	waitFor(t, "connection", func() bool { s, _ := bot.State(); return s == "ok" })

	// a stranger is ignored; the right code pairs the owner
	tg.push(msg(1, 999, "private", 0, 999, "hello?"))
	tg.push(msg(2, 5, "private", 0, 5, "/pair 123456"))
	waitFor(t, "pairing", func() bool { return tg.sawSent("Paired") })
	if c := bot.cfg(ctx); c.OwnerID != 5 || c.PairCode != "" {
		t.Fatalf("pairing state: %+v", c)
	}
	if tg.sawSent("999|") {
		t.Fatal("must not talk to strangers")
	}

	// DM → Atlas → reply
	fakeLLM.Handler = func(req map[string]any, call int) testutil.Reply {
		return testutil.Reply{Content: "Hi **there**, friend"}
	}
	tg.push(msg(3, 5, "private", 0, 5, "hello Atlas"))
	waitFor(t, "DM reply", func() bool { return tg.sawSent("5|<nil>|Hi <b>there</b>, friend") })

	// an agent drops a message into a named topic: the topic is created and remembered
	e.Notify(ctx, agent.Notice{Agent: "Scout", Text: "Milk dropped to 0.99 at ShopB", Topic: "Price watch"})
	waitFor(t, "topic message", func() bool { return tg.sawSent("-100|77|") })
	var name, by string
	if err := d.QueryRow(ctx, `SELECT name, created_by FROM telegram_topics WHERE thread_id=77`).Scan(&name, &by); err != nil || name != "Price watch" || by != "Scout" {
		t.Fatalf("topic row: %q %q %v", name, by, err)
	}

	// replying inside the topic reaches Atlas with the notice as context
	fakeLLM.Handler = func(req map[string]any, call int) testutil.Reply {
		var all strings.Builder
		for _, m := range req["messages"].([]any) {
			c, _ := m.(map[string]any)["content"].(string)
			all.WriteString(c + "\n")
		}
		if strings.Contains(all.String(), "Milk dropped to 0.99") {
			return testutil.Reply{Content: "Yes — ShopB now has milk at 0.99."}
		}
		return testutil.Reply{Content: "no context!"}
	}
	tg.push(msg(4, -100, "supergroup", 77, 5, "where was that cheap milk?"))
	waitFor(t, "topic reply", func() bool { return tg.sawSent("-100|77|Yes — ShopB now has milk at 0.99.") })
}

func TestToHTML(t *testing.T) {
	got := ToHTML("# Title\n**bold** and *it* with `a<b` and [link](https://x.y/z?a=1&b=2)\n- item\n```go\nx := 1 < 2\n```")
	for _, want := range []string{"<b>Title</b>", "<b>bold</b>", "<i>it</i>", "<code>a&lt;b</code>", `<a href="https://x.y/z?a=1&amp;b=2">link</a>`, "• item", "<pre>x := 1 &lt; 2</pre>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	long := strings.Repeat("line of text\n", 500)
	for _, p := range Split(long, 3800) {
		if len([]rune(p)) > 3800 {
			t.Fatal("split too long")
		}
	}
}

// A pairing code is short-lived and burns after a handful of wrong guesses, so a stranger who knows the
// bot's username cannot walk through the 6-digit space.
func TestPairingCodeExpiresAndBurns(t *testing.T) {
	d := testutil.DB(t)
	r, st := testutil.Setup(t, d, testutil.NewFakeLLM(t))
	e := agent.NewEngine(agent.Deps{DB: d.Pool, LLM: r, Tools: tools.NewRegistry(d.Pool), Profiles: agent.NewProfileStore(d.Pool), Sessions: agent.NewSessionStore(d.Pool),
		Tasks: tasks.NewStore(d.Pool), Memory: memory.New(d.Pool, r, st), Settings: st})
	ctx := context.Background()
	tg := &fakeTG{}
	srv := httptest.NewServer(tg.handler())
	defer srv.Close()
	bot := &Bot{Settings: st, Engine: e, DB: d.Pool, APIBase: srv.URL}
	handle := func(from int64, text string) {
		bot.handle(ctx, "TOKEN", update{UpdateID: 1, Message: mkMsg(from, text)})
	}

	// expired code: refused even when correct
	_ = st.Set(ctx, settings.KeyTelegram, settings.Telegram{Enabled: true, Token: "TOKEN", PairCode: "123456", PairExpires: time.Now().Add(-time.Minute).Unix()})
	handle(5, "/pair 123456")
	if c := bot.cfg(ctx); c.OwnerID != 0 || c.PairCode != "" {
		t.Fatalf("an expired code must not pair (and is cleared): %+v", c)
	}
	// wrong guesses burn a live code
	code, _ := bot.NewPairCode(ctx)
	for i := 0; i < maxBadPairs; i++ {
		handle(999, "/pair 000000")
	}
	handle(999, "/pair "+code)
	if c := bot.cfg(ctx); c.OwnerID != 0 {
		t.Fatalf("a burnt code must not pair, even with the right digits afterwards: %+v", c)
	}
	// a fresh code still works for the real owner
	code, _ = bot.NewPairCode(ctx)
	handle(5, "/pair "+code)
	if c := bot.cfg(ctx); c.OwnerID != 5 {
		t.Fatalf("a fresh code must pair: %+v", c)
	}
}

func TestErrorsNeverContainTheBotToken(t *testing.T) {
	bot := &Bot{APIBase: "http://127.0.0.1:1", http: &http.Client{Timeout: time.Second}}
	err := bot.call(context.Background(), "123:SECRET-TOKEN", "getMe", map[string]any{}, nil)
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("token leaked into the error: %v", err)
	}
}

// A photo sent to the bot reaches Atlas as a picture (the caption is the text), and a bad download is reported
// to the user instead of being dropped.
func TestTelegramPhotoReachesAtlas(t *testing.T) {
	d := testutil.DB(t)
	fakeLLM := testutil.NewFakeLLM(t)
	r, st := testutil.Setup(t, d, fakeLLM)
	e := agent.NewEngine(agent.Deps{DB: d.Pool, LLM: r, Tools: tools.NewRegistry(d.Pool), Profiles: agent.NewProfileStore(d.Pool), Sessions: agent.NewSessionStore(d.Pool),
		Tasks: tasks.NewStore(d.Pool), Memory: memory.New(d.Pool, r, st), Settings: st})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.Profiles.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	stored := map[int64][]byte{}
	e.SaveImage = func(_ context.Context, name, mime string, data []byte) (int64, error) {
		id := int64(len(stored) + 1)
		stored[id] = data
		return id, nil
	}
	e.LoadImage = func(_ context.Context, id int64) (string, []byte, error) { return "image/jpeg", stored[id], nil }
	imageParts := 0
	fakeLLM.Handler = func(req map[string]any, call int) testutil.Reply {
		for _, m := range req["messages"].([]any) {
			if parts, ok := m.(map[string]any)["content"].([]any); ok {
				for _, p := range parts {
					if p.(map[string]any)["type"] == "image_url" {
						imageParts++
					}
				}
			}
		}
		return testutil.Reply{Content: "Nice photo."}
	}
	tg := &fakeTG{file: append([]byte("\xff\xd8\xff\xe0"), []byte("jpeg-ish bytes")...)}
	srv := httptest.NewServer(tg.handler())
	defer srv.Close()
	_ = st.Set(ctx, settings.KeyTelegram, settings.Telegram{Enabled: true, Token: "TOKEN", OwnerID: 5})
	bot := &Bot{Settings: st, Engine: e, DB: d.Pool, APIBase: srv.URL}
	go bot.Run(ctx)
	waitFor(t, "connection", func() bool { s, _ := bot.State(); return s == "ok" })

	photo := msg(1, 5, "private", 0, 5, "")
	m := photo["message"].(map[string]any)
	delete(m, "text")
	m["caption"] = "what is this?"
	m["photo"] = []map[string]any{{"file_id": "small", "width": 90, "height": 90, "file_size": 1000}, {"file_id": "big", "width": 1280, "height": 960, "file_size": 90000}}
	tg.push(photo)
	waitFor(t, "reply to the photo", func() bool { return tg.sawSent("Nice photo") })
	if imageParts != 1 {
		t.Fatalf("Atlas must have received the picture, image parts seen: %d", imageParts)
	}
	if len(stored) != 1 {
		t.Fatalf("the picture must be stored once: %d", len(stored))
	}
	hist, _ := e.ChatHistory(ctx, "telegram", "", 10)
	if len(hist) == 0 || hist[0].Text != "what is this?" || len(hist[0].Images) != 1 {
		t.Fatalf("chat log: %+v", hist)
	}

	// a document that is not an image is not a picture: it lands in the workspace and Atlas is told where
	upl := t.TempDir()
	bot.UploadDir = upl
	tg.file = []byte("%PDF-1.4 fake")
	doc := msg(2, 5, "private", 0, 5, "")
	dm := doc["message"].(map[string]any)
	delete(dm, "text")
	dm["caption"] = "summarise this"
	dm["document"] = map[string]any{"file_id": "d", "file_name": "../notes.pdf", "mime_type": "application/pdf", "file_size": 13}
	tg.push(doc)
	waitFor(t, "the file reaches Atlas", func() bool {
		h, _ := e.ChatHistory(ctx, "telegram", "", 10)
		for _, m := range h {
			if strings.Contains(m.Text, "summarise this") && strings.Contains(m.Text, "Attached (already on disk") {
				return true
			}
		}
		return false
	})
	if len(stored) != 1 {
		t.Fatal("a PDF must not be stored as a picture")
	}
	var saved string
	_ = filepath.Walk(upl, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			saved = p
		}
		return nil
	})
	if got, _ := os.ReadFile(saved); !strings.HasSuffix(saved, "/notes.pdf") || string(got) != "%PDF-1.4 fake" || !strings.Contains(saved, "/tg-") {
		t.Fatalf("saved as %q (%q): the client-supplied name must not escape the folder", saved, got)
	}

	// too large for a bot to download: explained, nothing saved
	big := msg(3, 5, "private", 0, 5, "")
	bm := big["message"].(map[string]any)
	delete(bm, "text")
	bm["document"] = map[string]any{"file_id": "big", "file_name": "huge.zip", "mime_type": "application/zip", "file_size": 30 << 20}
	tg.push(big)
	waitFor(t, "size notice", func() bool { return tg.sawSent("larger than 20 MB") })

	// without a folder to put files in they are refused politely
	bot.UploadDir = ""
	nodir := msg(4, 5, "private", 0, 5, "")
	nm := nodir["message"].(map[string]any)
	delete(nm, "text")
	nm["document"] = map[string]any{"file_id": "d", "file_name": "a.txt", "mime_type": "text/plain", "file_size": 3}
	tg.push(nodir)
	waitFor(t, "refusal", func() bool { return tg.sawSent("files are not enabled") })
}

// Generated audio/pictures (markers like [audio:12]) are sent as real attachments after the words.
func TestTelegramSendsGeneratedMedia(t *testing.T) {
	d := testutil.DB(t)
	tg := &fakeTG{}
	srv := httptest.NewServer(tg.handler())
	defer srv.Close()
	ctx := context.Background()
	dir := t.TempDir()
	mk := func(name, mime, content string) int64 {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(content), 0o644)
		var id int64
		if err := d.QueryRow(ctx, `INSERT INTO artifacts(name,mime,path,size) VALUES($1,$2,$3,$4) RETURNING id`, name, mime, p, len(content)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	audio := mk("speech-hello.mp3", "audio/mpeg", "ID3-audio-bytes")
	img := mk("lighthouse.png", "image/png", "\x89PNG-image-bytes")
	bot := &Bot{DB: d.Pool, APIBase: srv.URL}

	if _, err := bot.send(ctx, "TOKEN", 5, 0, fmt.Sprintf("Here you go: [audio:%d] and [image:%d]", audio, img), nil); err != nil {
		t.Fatal(err)
	}
	if !tg.sawSent("Here you go:") || tg.sawSent("[audio:") {
		t.Fatalf("the words are sent without the raw markers: %v", tg.sentTexts())
	}
	if len(tg.uploads) != 2 || !strings.HasPrefix(tg.uploads[0], "sendAudio|5||speech-hello.mp3|15") || !strings.HasPrefix(tg.uploads[1], "sendPhoto|5||lighthouse.png|") {
		t.Fatalf("uploads: %v", tg.uploads)
	}
	// media only: no empty text message, thread preserved
	before := len(tg.sentTexts())
	if _, err := bot.send(ctx, "TOKEN", -100, 77, fmt.Sprintf("[audio:%d]", audio), nil); err != nil {
		t.Fatal(err)
	}
	if len(tg.sentTexts()) != before || len(tg.uploads) != 3 || !strings.HasPrefix(tg.uploads[2], "sendAudio|-100|77|") {
		t.Fatalf("audio-only reply: texts=%d uploads=%v", len(tg.sentTexts()), tg.uploads)
	}
	// a marker that points nowhere must not break the message
	if _, err := bot.send(ctx, "TOKEN", 5, 0, "still readable [audio:99999]", nil); err != nil || !tg.sawSent("still readable") {
		t.Fatalf("dangling marker: %v", err)
	}
}
