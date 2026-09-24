package elevenlabs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/settings"
	"prism/internal/testutil"
	"prism/internal/tools"
)

type fakeAPI struct {
	imageBody map[string]any
	mu        sync.Mutex
	used      int64
	limit     int64
	tier      string
	calls     []string
	ttsBodies []map[string]any
	imagePoll int
	failImage int // HTTP status for POST /v1/flows/image (0 = ok)
	srv       *httptest.Server
	saved     map[int64][]byte
}

func newFake(t *testing.T) *fakeAPI {
	f := &fakeAPI{used: 100, limit: 10000, tier: "free", saved: map[int64][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("xi-api-key") == "" && !strings.HasPrefix(r.URL.Path, "/cdn/") {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"detail":{"status":"invalid_api_key","message":"Invalid API key"}}`)
			return
		}
		switch {
		case r.URL.Path == "/v1/user/subscription":
			fmt.Fprintf(w, `{"tier":%q,"character_count":%d,"character_limit":%d,"next_character_count_reset_unix":1900000000,"status":"active"}`, f.tier, f.used, f.limit)
		case r.URL.Path == "/v1/voices":
			fmt.Fprint(w, `{"voices":[{"voice_id":"voiceidAAAAAAAAAAAAAA","name":"Rachel","category":"premade","labels":{"gender":"female","accent":"american"}},{"voice_id":"voiceidBBBBBBBBBBBBBB","name":"Adam","category":"premade","labels":{"gender":"male"}}]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			b["_voice"] = strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/")
			b["_format"] = r.URL.Query().Get("output_format")
			f.ttsBodies = append(f.ttsBodies, b)
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write([]byte("ID3fake-mp3-bytes"))
		case r.URL.Path == "/v1/sound-generation":
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write([]byte("ID3fake-sfx"))
		case r.URL.Path == "/v1/flows/image" && r.Method == http.MethodPost:
			if f.failImage != 0 {
				w.WriteHeader(f.failImage)
				fmt.Fprint(w, `{"detail":{"status":"paid_plan_required","message":"The Image & Video API requires a Pro plan or above."}}`)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&f.imageBody)
			fmt.Fprint(w, `{"id":"gen_1","status":"pending"}`)
		case r.URL.Path == "/v1/flows/image/gen_1":
			f.imagePoll++
			if f.imagePoll < 3 {
				fmt.Fprint(w, `{"id":"gen_1","status":"generating"}`)
				return
			}
			fmt.Fprintf(w, `{"id":"gen_1","status":"completed","content_url":"%s/cdn/out.png","content_mime_type":"image/png"}`, f.srv.URL)
		case r.URL.Path == "/cdn/out.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("\x89PNGfake-image"))
		default:
			w.WriteHeader(404)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) service(t *testing.T, cfg settings.ElevenLabs) (*Service, *settings.Store) {
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	cfg.APIKey = "test-key"
	if err := st.Set(context.Background(), settings.KeyElevenLabs, cfg); err != nil {
		t.Fatal(err)
	}
	var next int64
	return &Service{Settings: st, Base: f.srv.URL, HTTP: f.srv.Client(), PollEvery: time.Millisecond,
		Save: func(_ context.Context, name, mime string, data []byte, by string) (int64, error) {
			next++
			f.saved[next] = data
			return next, nil
		}}, st
}

func TestSpeakStoresAudioAndCountsCredits(t *testing.T) {
	f := newFake(t)
	s, _ := f.service(t, settings.DefaultElevenLabs())
	m, err := s.Speak(context.Background(), "Hello there, this is PRISM.", "adam", "Atlas")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != 1 || m.MIME != "audio/mpeg" || string(f.saved[1]) != "ID3fake-mp3-bytes" {
		t.Fatalf("%+v", m)
	}
	b := f.ttsBodies[0]
	if b["model_id"] != "eleven_flash_v2_5" || b["_voice"] != "voiceidBBBBBBBBBBBBBB" || b["_format"] != "mp3_44100_128" || b["text"] != "Hello there, this is PRISM." {
		t.Fatalf("request: %v", b)
	}
	if m.Voice != "Adam" || m.Credit != 13 { // 27 chars × 0.5 (Flash)
		t.Fatalf("voice %q credit %d", m.Voice, m.Credit)
	}
	if want := int64(10000 - 100 - 13); m.Left != want {
		t.Fatalf("left %d want %d", m.Left, want)
	}
	// unknown voice names are refused with suggestions instead of a silent fallback
	if _, err := s.Speak(context.Background(), "hi", "Nobody", "Atlas"); err == nil || !strings.Contains(err.Error(), "Rachel") {
		t.Fatalf("unknown voice: %v", err)
	}
	// the default voice is the account's first when none is configured
	if _, err := s.Speak(context.Background(), "hi", "", "Atlas"); err != nil || f.ttsBodies[len(f.ttsBodies)-1]["_voice"] != "voiceidAAAAAAAAAAAAAA" {
		t.Fatalf("default voice: %v %v", err, f.ttsBodies)
	}
}

func TestCreditGuardStopsSpendingBeforeTheAPICall(t *testing.T) {
	f := newFake(t)
	cfg := settings.DefaultElevenLabs()
	cfg.MonthlyCap = 150 // 100 already used
	s, st := f.service(t, cfg)
	ctx := context.Background()
	long := strings.Repeat("x", 200) // 100 credits on Flash
	if _, err := s.Speak(ctx, long, "", "Atlas"); err == nil || !strings.Contains(err.Error(), "monthly cap") {
		t.Fatalf("the cap must refuse: %v", err)
	}
	if len(f.ttsBodies) != 0 {
		t.Fatal("a refused request must not reach the API")
	}
	// the multilingual model costs twice as much per character
	cfg.MonthlyCap = 0
	cfg.ModelID = "eleven_multilingual_v2"
	_ = st.Set(ctx, settings.KeyElevenLabs, func() settings.ElevenLabs { cfg.APIKey = "test-key"; return cfg }())
	f.mu.Lock()
	f.used = 9950
	f.mu.Unlock()
	if _, err := s.Speak(ctx, strings.Repeat("y", 100), "", "Atlas"); err == nil || !strings.Contains(err.Error(), "not enough ElevenLabs credits") {
		t.Fatalf("the plan limit must refuse: %v", err)
	}
}

func TestErrorsAreFriendly(t *testing.T) {
	f := newFake(t)
	s, st := f.service(t, settings.DefaultElevenLabs())
	ctx := context.Background()
	f.failImage = 402
	_, err := s.Image(ctx, "a cat in a hat", "", "", "", "Atlas")
	if err == nil || !strings.Contains(err.Error(), "paid ElevenLabs plan") || !strings.Contains(err.Error(), "Pro plan") {
		t.Fatalf("the free-plan 402 must say what to do: %v", err)
	}
	// no key at all
	_ = st.Set(ctx, settings.KeyElevenLabs, settings.DefaultElevenLabs())
	if _, err := s.Speak(ctx, "hi", "", "Atlas"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing key: %v", err)
	}
	// error body shapes
	for body, want := range map[string]string{
		`{"detail":"quota exceeded"}`:                      "quota exceeded",
		`{"detail":[{"msg":"text too long","loc":["x"]}]}`: "text too long",
		`plain text failure`:                               "plain text failure",
	} {
		if e := apiError(422, []byte(body)).Error(); !strings.Contains(e, want) {
			t.Errorf("%s → %q", body, e)
		}
	}
	if e := apiError(401, []byte(`{}`)).Error(); !strings.Contains(e, "rejected the API key") {
		t.Errorf("401: %q", e)
	}
}

func TestImageGenerationPollsThenStores(t *testing.T) {
	f := newFake(t)
	s, _ := f.service(t, settings.DefaultElevenLabs())
	m, err := s.Image(context.Background(), "a lighthouse at dusk", "", "16:9", "2K", "Atlas")
	if err != nil {
		t.Fatal(err)
	}
	if m.MIME != "image/png" || !strings.HasPrefix(string(f.saved[m.ID]), "\x89PNG") || f.imagePoll != 3 {
		t.Fatalf("%+v polls=%d", m, f.imagePoll)
	}
}

func TestImageToolPassesItsParameters(t *testing.T) {
	f := newFake(t)
	s, _ := f.service(t, settings.DefaultElevenLabs())
	reg := tools.NewRegistry(nil)
	RegisterTools(reg, s)
	tool, _ := reg.Get("image_generate")
	out, err := tool.Run(context.Background(), &tools.Env{Agent: "Atlas"}, []byte(`{"prompt":"a red kite","aspect_ratio":"16:9","resolution":"2K"}`))
	if err != nil || !strings.Contains(out, "[image:1]") {
		t.Fatalf("%q %v", out, err)
	}
	if f.imageBody["aspect_ratio"] != "16:9" || f.imageBody["resolution"] != "2K" || f.imageBody["prompt"] != "a red kite" || f.imageBody["model_id"] != "gemini-2.5-flash-image" {
		t.Fatalf("snake_case parameters must reach the API: %v", f.imageBody)
	}
}

func TestToolsAskBeforeLongSpeechAndReturnMarkers(t *testing.T) {
	f := newFake(t)
	cfg := settings.DefaultElevenLabs()
	cfg.ConfirmOver = 50
	s, _ := f.service(t, cfg)
	reg := tools.NewRegistry(nil)
	RegisterTools(reg, s)
	tool, _ := reg.Get("tts_speak")
	run := func(text string, answer string, asked *int) (string, error) {
		env := &tools.Env{Agent: "Atlas", Ask: func(ctx context.Context, q tools.Question) (string, error) { *asked++; return answer, nil }}
		b, _ := json.Marshal(map[string]any{"text": text})
		return tool.Run(context.Background(), env, b)
	}
	asked := 0
	out, err := run("short one", "deny", &asked)
	if err != nil || asked != 0 || !strings.Contains(out, "[audio:1]") {
		t.Fatalf("short text speaks without asking: %q %v asked=%d", out, err, asked)
	}
	before := len(f.ttsBodies)
	if _, err := run(strings.Repeat("long ", 30), "deny", &asked); err == nil || asked != 1 || len(f.ttsBodies) != before {
		t.Fatalf("a denied long text must not be spoken: %v asked=%d", err, asked)
	}
	if out, err := run(strings.Repeat("long ", 30), "allow", &asked); err != nil || !strings.Contains(out, "[audio:2]") {
		t.Fatalf("approved long text: %q %v", out, err)
	}
	sfx, _ := reg.Get("sound_effect")
	if out, err := sfx.Run(context.Background(), &tools.Env{Agent: "Atlas"}, []byte(`{"prompt":"rain on a tin roof","duration_s":3}`)); err != nil || !strings.Contains(out, "[audio:3]") {
		t.Fatalf("sfx: %q %v", out, err)
	}
	vl, _ := reg.Get("voice_list")
	if out, err := vl.Run(context.Background(), &tools.Env{}, nil); err != nil || !strings.Contains(out, "Rachel") || !strings.Contains(out, "female") {
		t.Fatalf("voice_list: %q %v", out, err)
	}
}

func TestSlug(t *testing.T) {
	if got := slug("Hello, World! It's 5 o'clock", 32); got != "hello-world-it-s-5-o-clock" {
		t.Fatal(got)
	}
	if slug("!!!", 10) != "audio" {
		t.Fatal("empty slug falls back")
	}
	if s := slug("Привет, мир", 32); s != "привет-мир" {
		t.Fatal(s)
	}
}
