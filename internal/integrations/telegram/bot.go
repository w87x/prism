// Package telegram connects PRISM to Telegram: direct messages with the owner,
// a forum group whose topics receive agent notices (replies there keep the
// topic's context), pairing, inline-button confirmations and typing indicators.
package telegram

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/agent"
	"prism/internal/settings"
	"prism/internal/tools"
)

type Bot struct {
	Settings *settings.Store
	Engine   *agent.Engine
	DB       *pgxpool.Pool
	Logf     func(level, source, format string, args ...any)
	// APIBase overrides https://api.telegram.org (tests).
	APIBase string
	// UploadDir is where files the user sends are saved for the agents (the workspace uploads folder); empty: files are not accepted.
	UploadDir string

	mu       sync.Mutex
	state    string
	lastErr  string
	username string
	asks     map[int64]*pendingAsk
	http     *http.Client
	badPairs int
}

type pendingAsk struct {
	msgID   int64
	chatID  int64
	kind    string
	options []string
}

func (b *Bot) cfg(ctx context.Context) settings.Telegram {
	return settings.Load(ctx, b.Settings, settings.KeyTelegram, settings.Telegram{MirrorNotices: true})
}

func (b *Bot) logf(level, format string, args ...any) {
	if b.Logf != nil {
		b.Logf(level, "telegram", format, args...)
	}
}

// State reports the connection for the status bar.
func (b *Bot) State() (state, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == "" {
		return "off", "disabled"
	}
	if b.state == "ok" {
		return "ok", "@" + b.username
	}
	return b.state, b.lastErr
}

func (b *Bot) set(state, err string) {
	b.mu.Lock()
	b.state, b.lastErr = state, err
	b.mu.Unlock()
}

// ── API ─────────────────────────────────────────────────────────────────────

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Params      struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (b *Bot) call(ctx context.Context, token, method string, params any, out any) error {
	base := b.APIBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	body, _ := json.Marshal(params)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+token+"/"+method, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if b.http == nil {
		b.http = &http.Client{Timeout: 45 * time.Second}
	}
	resp, err := b.http.Do(req)
	if err != nil {
		// *url.Error embeds the request URL, which contains the bot token: never let it reach logs or the UI
		return errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
	}
	defer resp.Body.Close()
	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		if r.Params.RetryAfter > 0 {
			sleep(ctx, time.Duration(min(r.Params.RetryAfter, 30))*time.Second)
		}
		return fmt.Errorf("telegram %s: %s (%d)", method, r.Description, r.ErrorCode)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// download fetches a file the user sent (getFile, then the file endpoint). Errors never contain the token.
func (b *Bot) download(ctx context.Context, token, fileID string, limit int64) ([]byte, error) {
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := b.call(ctx, token, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return nil, err
	}
	base := b.APIBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/file/bot"+token+"/"+f.FilePath, nil)
	if b.http == nil {
		b.http = &http.Client{Timeout: 45 * time.Second}
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("telegram file download: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the file is larger than %d MB", limit>>20)
	}
	return data, nil
}

// pictures collects the images of a message: a photo (largest reasonable size) or an image sent as a file.
func (b *Bot) pictures(ctx context.Context, token string, m *message) ([]agent.Upload, error) {
	var fileID, name string
	switch {
	case len(m.Photo) > 0:
		for _, p := range m.Photo { // ascending sizes: take the largest that is not absurd
			if p.FileSize == 0 || p.FileSize <= agent.MaxUploadBytes {
				fileID = p.FileID
			}
		}
		name = "telegram-photo.jpg"
	case m.Document != nil && strings.HasPrefix(m.Document.MimeType, "image/"):
		if m.Document.FileSize > agent.MaxUploadBytes {
			return nil, fmt.Errorf("that image is larger than %d MB", agent.MaxUploadBytes>>20)
		}
		fileID, name = m.Document.FileID, m.Document.FileName
	}
	if fileID == "" {
		return nil, nil
	}
	data, err := b.download(ctx, token, fileID, agent.MaxUploadBytes)
	if err != nil {
		return nil, err
	}
	return []agent.Upload{{Name: name, Data: data}}, nil
}

// maxTelegramFile is what the Bot API lets a bot download.
const maxTelegramFile = 20 << 20

// attachments saves a document that is not a picture (PDF, Office file, text, archive…) in the workspace and
// returns the note that tells the agents where it is. ok is false when the message carries no such document.
func (b *Bot) attachments(ctx context.Context, token string, m *message) (note string, ok bool, err error) {
	d := m.Document
	if d == nil || strings.HasPrefix(d.MimeType, "image/") {
		return "", false, nil
	}
	if b.UploadDir == "" {
		return "", true, errors.New("files are not enabled here")
	}
	if d.FileSize > maxTelegramFile {
		return "", true, fmt.Errorf("that file is larger than %d MB, which is all Telegram lets a bot download; put it in a folder on the Mac and attach it from there in the web UI", maxTelegramFile>>20)
	}
	data, err := b.download(ctx, token, d.FileID, maxTelegramFile)
	if err != nil {
		return "", true, err
	}
	notes, err := agent.WriteUploads(filepath.Join(b.UploadDir, "tg-"+time.Now().Format("20060102-150405.000")), []agent.Upload{{Name: d.FileName, Data: data}})
	if err != nil {
		return "", true, err
	}
	return agent.AttachNote(notes), true, nil
}

// Test validates a token and returns the bot username.
func (b *Bot) Test(ctx context.Context, token string) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := b.call(ctx, token, "getMe", map[string]any{}, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

// pairTTL is how long a pairing code works; maxBadPairs wrong guesses burn the code (6 digits are guessable
// by a determined stranger who knows the bot's username).
const (
	pairTTL     = 10 * time.Minute
	maxBadPairs = 5
)

// NewPairCode generates and stores a one-time pairing code.
func (b *Bot) NewPairCode(ctx context.Context) (string, error) {
	n, _ := rand.Int(rand.Reader, big.NewInt(900000))
	code := strconv.Itoa(int(n.Int64()) + 100000)
	c := b.cfg(ctx)
	c.PairCode, c.PairExpires = code, time.Now().Add(pairTTL).Unix()
	b.mu.Lock()
	b.badPairs = 0
	b.mu.Unlock()
	return code, b.Settings.Set(ctx, settings.KeyTelegram, c)
}

// message is a Telegram message as far as PRISM reads it.
type message struct {
	MessageID int64 `json:"message_id"`
	ThreadID  int64 `json:"message_thread_id"`
	IsTopic   bool  `json:"is_topic_message"`
	From      *struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Chat struct {
		ID    int64  `json:"id"`
		Type  string `json:"type"`
		Forum bool   `json:"is_forum"`
	} `json:"chat"`
	Text    string `json:"text"`
	Caption string `json:"caption"`
	Photo   []struct {
		FileID   string `json:"file_id"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		FileSize int64  `json:"file_size"`
	} `json:"photo"` // the same picture in several sizes, smallest first
	Document *struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	} `json:"document"`
	ReplyTo *struct {
		MessageID int64 `json:"message_id"`
	} `json:"reply_to_message"`
	HasMedia bool `json:"-"`
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
	Callback *struct {
		ID      string             `json:"id"`
		Data    string             `json:"data"`
		From    struct{ ID int64 } `json:"from"`
		Message *struct {
			MessageID int64              `json:"message_id"`
			Chat      struct{ ID int64 } `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// Run supervises the bot until ctx ends: it re-reads the configuration continuously.
func (b *Bot) Run(ctx context.Context) {
	b.mu.Lock()
	b.asks = map[int64]*pendingAsk{}
	b.mu.Unlock()
	for ctx.Err() == nil {
		c := b.cfg(ctx)
		if !c.Enabled || c.Token == "" {
			b.set("", "")
			sleep(ctx, 4*time.Second)
			continue
		}
		user, err := b.Test(ctx, c.Token)
		if err != nil {
			b.set("error", err.Error())
			sleep(ctx, 15*time.Second)
			continue
		}
		b.mu.Lock()
		b.username = user
		b.mu.Unlock()
		b.set("ok", "")
		b.logf("info", "connected as @%s", user)
		b.poll(ctx, c.Token)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (b *Bot) poll(ctx context.Context, token string) {
	var offset int64
	fails := 0
	for ctx.Err() == nil {
		if c := b.cfg(ctx); !c.Enabled || c.Token != token {
			return
		}
		var ups []update
		pctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		err := b.call(pctx, token, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"}}, &ups)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fails++
			b.set("error", err.Error())
			if fails > 3 {
				return // outer loop re-validates the token
			}
			sleep(ctx, time.Duration(fails)*3*time.Second)
			continue
		}
		if fails > 0 {
			fails = 0
			b.set("ok", "")
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			b.handle(ctx, token, u)
		}
	}
}

// mediaRe finds the markers agents put in replies for generated media ([audio:12], [image:12]).
var mediaRe = regexp.MustCompile(`\[(audio|image):(\d+)\]`)

type mediaRef struct {
	kind string
	id   int64
}

// splitMedia removes media markers from text and returns them in order.
func splitMedia(text string) (string, []mediaRef) {
	var refs []mediaRef
	for _, m := range mediaRe.FindAllStringSubmatch(text, -1) {
		id, _ := strconv.ParseInt(m[2], 10, 64)
		refs = append(refs, mediaRef{m[1], id})
	}
	if refs == nil {
		return text, nil
	}
	return strings.TrimSpace(mediaRe.ReplaceAllString(text, "")), refs
}

// upload posts a file to a Telegram send* method (multipart).
func (b *Bot) upload(ctx context.Context, token, method, field, filename string, data []byte, params map[string]string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range params {
		_ = mw.WriteField(k, v)
	}
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		return err
	}
	_, _ = fw.Write(data)
	_ = mw.Close()
	base := b.APIBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+token+"/"+method, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if b.http == nil {
		b.http = &http.Client{Timeout: 45 * time.Second}
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
	}
	defer resp.Body.Close()
	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s (%d)", method, r.Description, r.ErrorCode)
	}
	return nil
}

// sendMedia delivers a stored artifact (audio → sendAudio, picture → sendPhoto).
func (b *Bot) sendMedia(ctx context.Context, token string, chatID, thread int64, ref mediaRef) error {
	if b.DB == nil {
		return errors.New("no database")
	}
	var name, mime, path string
	if err := b.DB.QueryRow(ctx, `SELECT name,mime,path FROM artifacts WHERE id=$1`, ref.id).Scan(&name, &mime, &path); err != nil {
		return fmt.Errorf("artifact %d not found", ref.id)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > 45<<20 {
		return fmt.Errorf("%s is too large to send (%d MB)", name, len(data)>>20)
	}
	params := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	if thread != 0 {
		params["message_thread_id"] = strconv.FormatInt(thread, 10)
	}
	switch {
	case ref.kind == "audio" || strings.HasPrefix(mime, "audio/"):
		return b.upload(ctx, token, "sendAudio", "audio", name, data, params)
	case strings.HasPrefix(mime, "image/"):
		return b.upload(ctx, token, "sendPhoto", "photo", name, data, params)
	}
	return b.upload(ctx, token, "sendDocument", "document", name, data, params)
}

func (b *Bot) send(ctx context.Context, token string, chatID, thread int64, text string, markup any) (int64, error) {
	text, media := splitMedia(text)
	var last int64
	if text == "" && len(media) > 0 {
		for _, m := range media {
			if err := b.sendMedia(ctx, token, chatID, thread, m); err != nil {
				b.logf("warn", "cannot send %s %d: %v", m.kind, m.id, err)
			}
		}
		return 0, nil
	}
	defer func() { // the media follows the words
		for _, m := range media {
			if err := b.sendMedia(ctx, token, chatID, thread, m); err != nil {
				b.logf("warn", "cannot send %s %d: %v", m.kind, m.id, err)
			}
		}
	}()
	for i, part := range Split(text, 3800) {
		params := map[string]any{"chat_id": chatID, "text": ToHTML(part), "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
		if thread != 0 {
			params["message_thread_id"] = thread
		}
		if markup != nil && i == len(Split(text, 3800))-1 {
			params["reply_markup"] = markup
		}
		var msg struct {
			MessageID int64 `json:"message_id"`
		}
		err := b.call(ctx, token, "sendMessage", params, &msg)
		if err != nil && strings.Contains(err.Error(), "parse entities") { // formatting failed: resend plain
			params["text"] = part
			delete(params, "parse_mode")
			err = b.call(ctx, token, "sendMessage", params, &msg)
		}
		if err != nil {
			return last, err
		}
		last = msg.MessageID
	}
	return last, nil
}

func (b *Bot) handle(ctx context.Context, token string, u update) {
	c := b.cfg(ctx)
	if u.Callback != nil {
		b.handleCallback(ctx, token, c, u)
		return
	}
	m := u.Message
	if m == nil || m.From == nil {
		return
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}
	chatID := m.Chat.ID
	thread := int64(0)
	if m.IsTopic || (m.Chat.Forum && m.ThreadID != 0) {
		thread = m.ThreadID
	}
	reply := func(t string) { _, _ = b.send(ctx, token, chatID, thread, t, nil) }

	// pairing: /start <code> or /pair <code> from anyone while a code is pending
	if strings.HasPrefix(text, "/start") || strings.HasPrefix(text, "/pair") {
		f := strings.Fields(text)
		if len(f) > 1 && c.PairCode != "" {
			switch {
			case time.Now().Unix() > c.PairExpires:
				c.PairCode = ""
				_ = b.Settings.Set(ctx, settings.KeyTelegram, c)
			case f[1] == c.PairCode:
				c.OwnerID, c.PairCode, c.PairExpires = m.From.ID, "", 0
				_ = b.Settings.Set(ctx, settings.KeyTelegram, c)
				reply("Paired ✔ — I'm PRISM. Talk to me here; I'll also drop notices for you.")
				b.logf("info", "paired with Telegram user %d", m.From.ID)
				return
			default:
				b.mu.Lock()
				b.badPairs++
				burnt := b.badPairs >= maxBadPairs
				b.mu.Unlock()
				if burnt {
					c.PairCode = ""
					_ = b.Settings.Set(ctx, settings.KeyTelegram, c)
					b.logf("warn", "pairing code burnt after %d wrong guesses (last from Telegram user %d)", maxBadPairs, m.From.ID)
				}
				return
			}
		}
		if c.OwnerID == 0 {
			reply("This PRISM instance is not paired yet. Generate a pairing code in the web UI (Settings → Telegram) and send /pair <code>.")
			return
		}
	}
	if c.OwnerID == 0 || m.From.ID != c.OwnerID {
		return // single-user: everyone else is ignored silently
	}
	// bind a forum group for topics
	if strings.HasPrefix(text, "/setgroup") && m.Chat.Type != "private" {
		c.GroupID = chatID
		_ = b.Settings.Set(ctx, settings.KeyTelegram, c)
		reply("Group bound. Agents can now open topics here (make sure I'm an admin with “Manage topics”).")
		return
	}
	channelTopic := ""
	switch {
	case m.Chat.Type == "private":
	case c.GroupID != 0 && chatID == c.GroupID:
		channelTopic = strconv.FormatInt(thread, 10) // "0" = General
	default:
		return // not the configured group
	}
	key := agent.ChatKey("telegram", channelTopic)

	if out, ok := b.Engine.RunCommand(ctx, "telegram", channelTopic, text); ok {
		reply(out)
		return
	}
	ups, perr := b.pictures(ctx, token, m)
	if perr != nil {
		reply("⚠ I could not fetch that picture: " + perr.Error())
		return
	}
	note, isFile, ferr := b.attachments(ctx, token, m)
	if ferr != nil {
		reply("⚠ I could not take that file: " + ferr.Error())
		return
	}
	if isFile {
		if text == "" {
			text = "Please have a look at this file."
		}
		text += "\n\n" + note
	}
	if text == "" && len(ups) == 0 {
		reply("I can read text messages, pictures and files.")
		return
	}

	// an answer to a pending clarification?
	if id, ok := b.matchAsk(m.ReplyTo, chatID, m.Chat.Type == "private"); ok {
		if b.Engine.AnswerAsk(id, text) {
			return
		}
	}
	// typing indicator while Atlas works
	go func() {
		for i := 0; i < 150; i++ {
			p := map[string]any{"chat_id": chatID, "action": "typing"}
			if thread != 0 {
				p["message_thread_id"] = thread
			}
			_ = b.call(ctx, token, "sendChatAction", p, nil)
			sleep(ctx, 4*time.Second)
			if !b.Engine.ChatBusy(key) {
				return
			}
		}
	}()
	err := b.Engine.UserMessage(ctx, agent.UserMsg{Text: text, Images: ups, Channel: "telegram", Topic: channelTopic, Reply: func(ag, t string) { reply(t) }})
	if err != nil {
		reply("⚠ " + err.Error())
	}
}

func (b *Bot) matchAsk(replyTo *struct {
	MessageID int64 `json:"message_id"`
}, chatID int64, private bool) (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var only int64
	n := 0
	for id, a := range b.asks {
		if a.kind != "clarify" {
			continue
		}
		if replyTo != nil && replyTo.MessageID == a.msgID {
			return id, true
		}
		if private && a.chatID == chatID {
			only, n = id, n+1
		}
	}
	if n == 1 {
		return only, true
	}
	return 0, false
}

func (b *Bot) handleCallback(ctx context.Context, token string, c settings.Telegram, u update) {
	cb := u.Callback
	_ = b.call(ctx, token, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID}, nil)
	if cb.From.ID != c.OwnerID {
		return
	}
	parts := strings.Split(cb.Data, ":") // ask:<id>:<idx>
	if len(parts) != 3 || parts[0] != "ask" {
		return
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	idx, _ := strconv.Atoi(parts[2])
	b.mu.Lock()
	a := b.asks[id]
	b.mu.Unlock()
	if a == nil || idx < 0 || idx >= len(a.options) {
		return
	}
	choice := a.options[idx]
	if b.Engine.AnswerAsk(id, choice) && cb.Message != nil {
		_ = b.call(ctx, token, "editMessageReplyMarkup", map[string]any{"chat_id": cb.Message.Chat.ID, "message_id": cb.Message.MessageID, "reply_markup": map[string]any{"inline_keyboard": [][]any{}}}, nil)
		_, _ = b.send(ctx, token, cb.Message.Chat.ID, 0, "→ "+choice, nil)
	}
}

// ── sink ────────────────────────────────────────────────────────────────────

func (b *Bot) active(ctx context.Context) (settings.Telegram, bool) {
	c := b.cfg(ctx)
	st, _ := b.State()
	return c, c.Enabled && c.Token != "" && c.OwnerID != 0 && st == "ok"
}

// Notice delivers an agent-initiated message to the DM or to a named forum topic.
func (b *Bot) Notice(ctx context.Context, n agent.Notice) { _, _ = b.Deliver(ctx, n) }

// Deliver is Notice that says what actually happened, so an agent asking for a topic is told the truth: where the
// message went, or why it did not go where it was asked to.
func (b *Bot) Deliver(ctx context.Context, n agent.Notice) (string, error) {
	c, ok := b.active(ctx)
	if !ok {
		return "", fmt.Errorf("%w: Telegram is disabled, has no owner set, or the bot is offline", agent.ErrSinkInactive)
	}
	prefix := ""
	if n.Level == "attention" || n.Level == "warning" || n.Level == "error" {
		prefix = map[string]string{"attention": "🟠 ", "warning": "🟡 ", "error": "🔴 "}[n.Level]
	}
	text := fmt.Sprintf("%s**%s:** %s", prefix, n.Agent, n.Text)
	why := ""
	if n.Topic != "" && c.GroupID != 0 {
		thread, err := b.topic(ctx, c, n.Topic, n.Agent, n.Text)
		if err == nil {
			if _, err := b.send(ctx, c.Token, c.GroupID, thread, text, nil); err == nil {
				b.Engine.NoteToChat(ctx, agent.ChatKey("telegram", strconv.FormatInt(thread, 10)), n.Agent, n.Text)
				return fmt.Sprintf("Delivered to the Telegram topic %q.", n.Topic), nil
			} else {
				why = fmt.Sprintf("could not post in topic %q: %v", n.Topic, err)
				b.logf("warn", "topic send failed: %v", err)
			}
		} else {
			why = fmt.Sprintf("could not open topic %q: %v", n.Topic, err)
			b.logf("warn", "cannot open topic %q: %v", n.Topic, err)
		}
		text = fmt.Sprintf("[%s] %s", n.Topic, text) // fall back to the DM
	} else if n.Topic != "" {
		why = "no Telegram group is configured, so topics are unavailable"
		text = fmt.Sprintf("[%s] %s", n.Topic, text)
	}
	if !c.MirrorNotices {
		if why != "" {
			return "", errors.New("NOT delivered: " + why + " (and DM mirroring of notices is off)")
		}
		return "Not sent: mirroring notices to Telegram is switched off.", nil
	}
	if _, err := b.send(ctx, c.Token, c.OwnerID, 0, text, nil); err != nil {
		return "", fmt.Errorf("NOT delivered: %s%v", map[bool]string{true: why + "; DM failed too: "}[why != ""], err)
	}
	b.Engine.NoteToChat(ctx, "tg:dm", n.Agent, n.Text)
	if why != "" {
		return "Sent to the owner's DM instead of the topic, because " + why + ".", nil
	}
	return "Delivered to the Telegram DM.", nil
}

// topic finds or creates a forum topic by name.
func (b *Bot) topic(ctx context.Context, c settings.Telegram, name, by, purpose string) (int64, error) {
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	var thread int64
	err := b.DB.QueryRow(ctx, `SELECT thread_id FROM telegram_topics WHERE chat_id=$1 AND lower(name)=lower($2)`, c.GroupID, name).Scan(&thread)
	if err == nil {
		return thread, nil
	}
	var res struct {
		ThreadID int64 `json:"message_thread_id"`
	}
	if err := b.call(ctx, c.Token, "createForumTopic", map[string]any{"chat_id": c.GroupID, "name": name}, &res); err != nil {
		switch m := err.Error(); {
		case strings.Contains(m, "not enough rights"):
			err = fmt.Errorf("%w — make the bot an admin of the group with the \"Manage Topics\" right switched on (Group → Administrators → the bot → Manage Topics)", err)
		case strings.Contains(m, "not a forum"):
			err = fmt.Errorf("%w — turn on Topics in the group settings (Group → Edit → Topics)", err)
		case strings.Contains(m, "chat not found"):
			err = fmt.Errorf("%w — the group id must be the supergroup id (starts with -100…) and the bot must be a member", err)
		}
		return 0, err
	}
	if len([]rune(purpose)) > 300 {
		purpose = string([]rune(purpose)[:300])
	}
	_, _ = b.DB.Exec(ctx, `INSERT INTO telegram_topics(chat_id,thread_id,name,purpose,created_by) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, c.GroupID, res.ThreadID, name, purpose, by)
	return res.ThreadID, nil
}

// AskUser presents a question or confirmation in the DM (buttons when there are options).
func (b *Bot) AskUser(ctx context.Context, id int64, ag string, q tools.Question) {
	c, ok := b.active(ctx)
	if !ok {
		return
	}
	opts := q.Options
	if q.Kind == "confirm" && len(opts) == 0 {
		opts = []string{"allow", "deny"}
	}
	text := fmt.Sprintf("❓ **%s** asks:\n%s", ag, q.Text)
	if q.Args != "" {
		text += "\n`" + q.Args + "`"
	}
	var markup any
	kind := "clarify"
	if q.Kind == "confirm" {
		kind = "confirm"
	}
	if len(opts) > 0 {
		var rows [][]map[string]string
		for i, o := range opts {
			rows = append(rows, []map[string]string{{"text": o, "callback_data": fmt.Sprintf("ask:%d:%d", id, i)}})
		}
		markup = map[string]any{"inline_keyboard": rows}
	} else {
		text += "\n(reply to this message with your answer)"
	}
	msgID, err := b.send(ctx, c.Token, c.OwnerID, 0, text, markup)
	if err != nil {
		return
	}
	b.mu.Lock()
	b.asks[id] = &pendingAsk{msgID: msgID, chatID: c.OwnerID, kind: kind, options: opts}
	b.mu.Unlock()
	go func() { // forget once resolved
		for i := 0; i < 720; i++ {
			sleep(ctx, 5*time.Second)
			resolved := true
			for _, p := range b.Engine.PendingAsks() {
				if p["id"].(int64) == id {
					resolved = false
				}
			}
			if resolved {
				break
			}
		}
		b.mu.Lock()
		delete(b.asks, id)
		b.mu.Unlock()
	}()
}
