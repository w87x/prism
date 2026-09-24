// Package elevenlabs connects PRISM to ElevenLabs: speech (text-to-speech), sound effects and image
// generation as agent tools, with a credit guard so a chatty agent cannot burn a small plan.
//
// The generated media is stored as artifacts; tools return a marker ([audio:ID] / [image:ID]) that the chat
// UI turns into a player / picture and Telegram turns into an attachment.
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"prism/internal/netguard"
	"prism/internal/settings"
)

const defaultBase = "https://api.elevenlabs.io"

// SaveFunc stores generated media as an artifact and returns its id.
type SaveFunc func(ctx context.Context, name, mime string, data []byte, by string) (int64, error)

type Service struct {
	Settings *settings.Store
	Save     SaveFunc
	// Base overrides the API root (tests).
	Base string
	HTTP *http.Client
	// PollEvery is the image status polling interval (tests shorten it).
	PollEvery time.Duration

	mu     sync.Mutex
	voices []Voice
	voiceT time.Time
}

func (s *Service) cfg(ctx context.Context) settings.ElevenLabs {
	return settings.Load(ctx, s.Settings, settings.KeyElevenLabs, settings.DefaultElevenLabs())
}

func (s *Service) base() string {
	if s.Base != "" {
		return strings.TrimRight(s.Base, "/")
	}
	return defaultBase
}

func (s *Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 3 * time.Minute}
}

// Configured reports whether an API key is set.
func (s *Service) Configured(ctx context.Context) bool {
	return strings.TrimSpace(s.cfg(ctx).APIKey) != ""
}

// Error is an API failure with the message ElevenLabs gave.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	switch {
	case e.Status == 401:
		return "ElevenLabs rejected the API key (check Settings → Integrations → ElevenLabs)"
	case e.Status == 402 || e.Code == "paid_plan_required":
		return "this needs a paid ElevenLabs plan: " + firstNonEmpty(e.Message, "the free plan does not include it")
	case e.Status == 429:
		return "ElevenLabs is rate-limiting this key (the free plan allows very few parallel requests): " + e.Message
	}
	return fmt.Sprintf("ElevenLabs error %d: %s", e.Status, firstNonEmpty(e.Message, e.Code))
}

func firstNonEmpty(a ...string) string {
	for _, x := range a {
		if x != "" {
			return x
		}
	}
	return ""
}

// apiError decodes the {"detail": {...}} / {"detail": "..."} / {"detail":[{msg}]} shapes the API uses.
func apiError(status int, body []byte) error {
	e := &Error{Status: status}
	var d struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &d) == nil && len(d.Detail) > 0 {
		var obj struct {
			Status  string `json:"status"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		var str string
		var list []struct {
			Msg string `json:"msg"`
		}
		switch {
		case json.Unmarshal(d.Detail, &obj) == nil && (obj.Message != "" || obj.Status != ""):
			e.Code, e.Message = firstNonEmpty(obj.Status, obj.Code), obj.Message
		case json.Unmarshal(d.Detail, &str) == nil:
			e.Message = str
		case json.Unmarshal(d.Detail, &list) == nil && len(list) > 0:
			e.Message = list[0].Msg
		}
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(body[:min(len(body), 200)]))
	}
	return e
}

// do performs a request with the account key. out (if non-nil) receives decoded JSON; raw returns the body.
func (s *Service) do(ctx context.Context, key, method, path string, in any, out any) ([]byte, string, error) {
	var rd io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base()+path, rd)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("xi-api-key", key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, "", errors.New(strings.ReplaceAll(err.Error(), key, "<key>")) // the header is not in the URL, but be safe
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 60<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode/100 != 2 {
		return nil, "", apiError(resp.StatusCode, body)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return nil, "", fmt.Errorf("unexpected reply from ElevenLabs: %w", err)
		}
	}
	return body, resp.Header.Get("Content-Type"), nil
}

func (s *Service) key(ctx context.Context) (string, error) {
	k := strings.TrimSpace(s.cfg(ctx).APIKey)
	if k == "" {
		return "", errors.New("ElevenLabs is not configured: add an API key in Settings → Integrations → ElevenLabs")
	}
	return k, nil
}

// ── account ─────────────────────────────────────────────────────────────────

type Subscription struct {
	Tier      string `json:"tier"`
	Used      int64  `json:"used"`
	Limit     int64  `json:"limit"`
	ResetUnix int64  `json:"reset_unix"`
	Status    string `json:"status"`
}

// Remaining is what the plan still allows this cycle.
func (s Subscription) Remaining() int64 { return max(0, s.Limit-s.Used) }

// Subscription reads the plan and credit usage for key (empty → the saved key).
func (s *Service) Subscription(ctx context.Context, key string) (*Subscription, error) {
	if key == "" {
		var err error
		if key, err = s.key(ctx); err != nil {
			return nil, err
		}
	}
	var r struct {
		Tier       string `json:"tier"`
		Count      int64  `json:"character_count"`
		Limit      int64  `json:"character_limit"`
		ResetUnix  int64  `json:"next_character_count_reset_unix"`
		Status     string `json:"status"`
		CanExtend  bool   `json:"can_extend_character_limit"`
		AllowedAdd bool   `json:"allowed_to_extend_character_limit"`
	}
	if _, _, err := s.do(ctx, key, http.MethodGet, "/v1/user/subscription", nil, &r); err != nil {
		return nil, err
	}
	return &Subscription{Tier: r.Tier, Used: r.Count, Limit: r.Limit, ResetUnix: r.ResetUnix, Status: r.Status}, nil
}

type Voice struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Category string            `json:"category"`
	Labels   map[string]string `json:"labels,omitempty"`
	Preview  string            `json:"preview,omitempty"`
}

// Voices lists the account's voices (cached for a few minutes).
func (s *Service) Voices(ctx context.Context, key string) ([]Voice, error) {
	saved := key == "" // only the saved key's list is cached; a key typed into the settings form is just being tried
	if key == "" {
		s.mu.Lock()
		if len(s.voices) > 0 && time.Since(s.voiceT) < 10*time.Minute {
			v := s.voices
			s.mu.Unlock()
			return v, nil
		}
		s.mu.Unlock()
		var err error
		if key, err = s.key(ctx); err != nil {
			return nil, err
		}
	}
	var r struct {
		Voices []struct {
			ID       string            `json:"voice_id"`
			Name     string            `json:"name"`
			Category string            `json:"category"`
			Labels   map[string]string `json:"labels"`
			Preview  string            `json:"preview_url"`
		} `json:"voices"`
	}
	if _, _, err := s.do(ctx, key, http.MethodGet, "/v1/voices", nil, &r); err != nil {
		return nil, err
	}
	out := make([]Voice, 0, len(r.Voices))
	for _, v := range r.Voices {
		out = append(out, Voice{ID: v.ID, Name: v.Name, Category: v.Category, Labels: v.Labels, Preview: v.Preview})
	}
	if saved {
		s.mu.Lock()
		s.voices, s.voiceT = out, time.Now()
		s.mu.Unlock()
	}
	return out, nil
}

// resolveVoice turns a name or id into a voice id; empty → the configured default → the account's first voice.
func (s *Service) resolveVoice(ctx context.Context, want string) (id, name string, err error) {
	want = strings.TrimSpace(want)
	if want == "" {
		want = s.cfg(ctx).VoiceID
	}
	vs, verr := s.Voices(ctx, "")
	if want != "" {
		for _, v := range vs {
			if strings.EqualFold(v.Name, want) || v.ID == want {
				return v.ID, v.Name, nil
			}
		}
		if verr == nil && len(vs) > 0 && !looksLikeID(want) {
			names := make([]string, 0, 5)
			for _, v := range vs[:min(5, len(vs))] {
				names = append(names, v.Name)
			}
			return "", "", fmt.Errorf("no voice called %q (try one of: %s — or call voice_list)", want, strings.Join(names, ", "))
		}
		return want, want, nil // an id the list did not show (shared/library voice)
	}
	if verr != nil {
		return "", "", verr
	}
	if len(vs) == 0 {
		return "", "", errors.New("this ElevenLabs account has no voices available")
	}
	return vs[0].ID, vs[0].Name, nil
}

func looksLikeID(s string) bool { return len(s) >= 16 && !strings.ContainsAny(s, " ") }

// ── credits ─────────────────────────────────────────────────────────────────

// costPerChar is how many credits one character costs on a model (Flash/Turbo halve it).
func costPerChar(model string) float64 {
	m := strings.ToLower(model)
	if strings.Contains(m, "flash") || strings.Contains(m, "turbo") {
		return 0.5
	}
	return 1
}

// guard refuses spending that would pass the user's monthly cap or the plan itself.
func (s *Service) guard(ctx context.Context, cost int64) (*Subscription, error) {
	sub, err := s.Subscription(ctx, "")
	if err != nil {
		return nil, err
	}
	if sub.Used+cost > sub.Limit {
		return sub, fmt.Errorf("not enough ElevenLabs credits: this needs about %d, %d left on the %s plan this cycle", cost, sub.Remaining(), sub.Tier)
	}
	if cap := int64(s.cfg(ctx).MonthlyCap); cap > 0 && sub.Used+cost > cap {
		return sub, fmt.Errorf("PRISM's ElevenLabs monthly cap (%d credits) would be passed: %d used, this needs about %d (raise the cap in Settings → Integrations)", cap, sub.Used, cost)
	}
	return sub, nil
}

// ── generation ──────────────────────────────────────────────────────────────

// Media is generated content, already stored as an artifact.
type Media struct {
	ID     int64
	Name   string
	MIME   string
	Bytes  int
	Credit int64 // credits this call cost (estimate)
	Left   int64 // credits left on the plan afterwards (estimate)
	Voice  string
}

func (s *Service) store(ctx context.Context, name, mime string, data []byte, by string) (int64, error) {
	if s.Save == nil {
		return 0, errors.New("cannot store the result: no artifact storage")
	}
	return s.Save(ctx, name, mime, data, by)
}

// Speak turns text into an mp3 artifact.
func (s *Service) Speak(ctx context.Context, text, voice, by string) (*Media, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("nothing to say")
	}
	cfg := s.cfg(ctx)
	if n := len([]rune(text)); n > 5000 {
		return nil, fmt.Errorf("text is %d characters; keep it under 5000 per call (split it up)", n)
	}
	key, err := s.key(ctx)
	if err != nil {
		return nil, err
	}
	cost := int64(float64(len([]rune(text))) * costPerChar(cfg.ModelID))
	sub, err := s.guard(ctx, cost)
	if err != nil {
		return nil, err
	}
	vid, vname, err := s.resolveVoice(ctx, voice)
	if err != nil {
		return nil, err
	}
	audio, _, err := s.do(ctx, key, http.MethodPost, "/v1/text-to-speech/"+url.PathEscape(vid)+"?output_format=mp3_44100_128",
		map[string]any{"text": text, "model_id": firstNonEmpty(cfg.ModelID, "eleven_flash_v2_5")}, nil)
	if err != nil {
		return nil, err
	}
	name := "speech-" + slug(text, 32) + ".mp3"
	id, err := s.store(ctx, name, "audio/mpeg", audio, by)
	if err != nil {
		return nil, err
	}
	return &Media{ID: id, Name: name, MIME: "audio/mpeg", Bytes: len(audio), Credit: cost, Left: sub.Remaining() - cost, Voice: vname}, nil
}

// SoundEffect generates a sound from a description (durationS 0 → let the model decide).
func (s *Service) SoundEffect(ctx context.Context, prompt string, durationS float64, by string) (*Media, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, errors.New("describe the sound")
	}
	key, err := s.key(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"text": prompt}
	cost := int64(200) // automatic duration: roughly a few seconds
	if durationS > 0 {
		durationS = min(max(durationS, 0.5), 22)
		body["duration_seconds"] = durationS
		cost = int64(durationS * 40)
	}
	sub, err := s.guard(ctx, cost)
	if err != nil {
		return nil, err
	}
	audio, _, err := s.do(ctx, key, http.MethodPost, "/v1/sound-generation?output_format=mp3_44100_128", body, nil)
	if err != nil {
		return nil, err
	}
	name := "sfx-" + slug(prompt, 32) + ".mp3"
	id, err := s.store(ctx, name, "audio/mpeg", audio, by)
	if err != nil {
		return nil, err
	}
	return &Media{ID: id, Name: name, MIME: "audio/mpeg", Bytes: len(audio), Credit: cost, Left: sub.Remaining() - cost}, nil
}

// Image generates a picture. ElevenLabs' image API is asynchronous and needs a Pro plan (or above): on the
// free plan the first call fails with a clear message.
func (s *Service) Image(ctx context.Context, prompt, model, aspect, resolution, by string) (*Media, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, errors.New("describe the image")
	}
	key, err := s.key(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"model_id": firstNonEmpty(model, s.cfg(ctx).ImageModel, "gemini-2.5-flash-image"), "prompt": prompt}
	if aspect != "" {
		body["aspect_ratio"] = aspect
	}
	if resolution != "" {
		body["resolution"] = resolution
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if _, _, err := s.do(ctx, key, http.MethodPost, "/v1/flows/image", body, &created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, errors.New("ElevenLabs did not return a generation id")
	}
	every := s.PollEvery
	if every <= 0 {
		every = 2 * time.Second // their docs: poll no more than once every 2 seconds
	}
	deadline := time.Now().Add(4 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(every):
		}
		var st struct {
			Status     string `json:"status"`
			ContentURL string `json:"content_url"`
			MIME       string `json:"content_mime_type"`
			Error      string `json:"error_message"`
			Reason     string `json:"failure_reason"`
		}
		if _, _, err := s.do(ctx, key, http.MethodGet, "/v1/flows/image/"+url.PathEscape(created.ID), nil, &st); err != nil {
			return nil, err
		}
		switch st.Status {
		case "completed":
			img, mime, err := s.fetchSigned(ctx, st.ContentURL)
			if err != nil {
				return nil, err
			}
			if st.MIME != "" {
				mime = st.MIME
			}
			if !strings.HasPrefix(mime, "image/") {
				mime = "image/png"
			}
			ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp"}[mime]
			name := "image-" + slug(prompt, 32) + ext
			id, err := s.store(ctx, name, mime, img, by)
			if err != nil {
				return nil, err
			}
			return &Media{ID: id, Name: name, MIME: mime, Bytes: len(img)}, nil
		case "failed":
			msg := firstNonEmpty(st.Error, st.Reason, "generation failed")
			if st.Reason == "moderated" {
				msg = "the image was blocked by content moderation: " + msg
			}
			return nil, errors.New("ElevenLabs image generation failed: " + msg)
		}
		if time.Now().After(deadline) {
			return nil, errors.New("ElevenLabs image generation is taking too long; try again later")
		}
	}
}

// fetchSigned downloads the signed result URL (no API key: it is a pre-signed link on their CDN).
func (s *Service) fetchSigned(ctx context.Context, raw string) ([]byte, string, error) {
	if raw == "" {
		return nil, "", errors.New("the finished image has no download link")
	}
	cl := s.HTTP
	if cl == nil {
		cl = netguard.Client(false, 2*time.Minute) // a link we were handed: never let it lead into the local network
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	resp, err := cl.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("downloading the image failed: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 40<<20))
	return b, resp.Header.Get("Content-Type"), err
}

// slug makes a short file-name fragment out of free text.
func slug(s string, n int) string {
	var sb strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r > 127:
			sb.WriteRune(r)
			dash = false
		case !dash && sb.Len() > 0:
			sb.WriteByte('-')
			dash = true
		}
		if len([]rune(sb.String())) >= n {
			break
		}
	}
	out := strings.Trim(sb.String(), "-")
	if out == "" {
		return "audio"
	}
	return out
}
