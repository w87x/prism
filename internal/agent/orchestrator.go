package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"prism/internal/llm"
	"prism/internal/memory"
	"prism/internal/tasks"
	"prism/internal/tools"
)

// ── asks (clarifications & confirmations) ──────────────────────────────────

type pendingAsk struct {
	ID    int64          `json:"id"`
	Q     tools.Question `json:"q"`
	Run   RunInfo        `json:"run"`
	ch    chan string
	Since time.Time `json:"since"`
}

// NoticeSink delivers notices and asks to an external channel (Telegram, macOS…).
type NoticeSink interface {
	Notice(ctx context.Context, n Notice)
	AskUser(ctx context.Context, id int64, agent string, q tools.Question)
}

type Notice struct {
	Agent string `json:"agent"`
	Text  string `json:"text"`
	Level string `json:"level"` // info | attention | warning | error
	Topic string `json:"topic"` // routing hint for Telegram topics
}

func (e *Engine) ask(ctx context.Context, info RunInfo, q tools.Question) (string, error) {
	pa := &pendingAsk{ID: e.askSeq.Add(1), Q: q, Run: info, ch: make(chan string, 1), Since: time.Now()}
	e.mu.Lock()
	e.asks[pa.ID] = pa
	e.mu.Unlock()
	e.Emit("ask.request", map[string]any{"id": pa.ID, "run": info.ID, "agent": info.Agent, "kind": q.Kind, "text": q.Text,
		"options": q.Options, "tool": q.Tool, "args": q.Args, "since": pa.Since.UnixMilli()})
	// persisted so a restart while this is outstanding is reconciled on the next start instead of silently
	// vanishing (see ReconcilePendingAsks); best-effort — losing this row only means a worse restart
	// experience, never a lost answer channel, so a DB hiccup here must not fail the ask itself.
	if e.DB != nil {
		var taskID *int64
		if info.TaskID != 0 {
			taskID = &info.TaskID
		}
		_, _ = e.DB.Exec(context.WithoutCancel(ctx), `INSERT INTO pending_asks(id,run_id,task_id,agent,kind,question,options,tool,args) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			pa.ID, info.ID, taskID, info.Agent, q.Kind, q.Text, q.Options, q.Tool, q.Args)
	}
	for _, s := range e.Sinks {
		go s.AskUser(context.WithoutCancel(ctx), pa.ID, info.Agent, q)
	}
	timeout := 60 * time.Minute
	if q.Kind == "confirm" {
		timeout = 10 * time.Minute
	}
	defer func() {
		e.mu.Lock()
		delete(e.asks, pa.ID)
		e.mu.Unlock()
		if e.DB != nil {
			_, _ = e.DB.Exec(context.WithoutCancel(ctx), `DELETE FROM pending_asks WHERE id=$1`, pa.ID)
		}
		e.Emit("ask.done", map[string]any{"id": pa.ID})
	}()
	select {
	case a := <-pa.ch:
		return a, nil
	case <-time.After(timeout):
		return "", errors.New("no answer from the user (timed out)")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// AnswerAsk resolves a pending ask; it reports whether the ask existed.
func (e *Engine) AnswerAsk(id int64, answer string) bool {
	e.mu.Lock()
	pa := e.asks[id]
	e.mu.Unlock()
	if pa == nil {
		return false
	}
	select {
	case pa.ch <- answer:
	default:
	}
	return true
}

// ReconcilePendingAsks runs once at startup, before RequeueRunning. Any row still in pending_asks at this
// point is necessarily orphaned — the in-memory channel it was waiting on died with the previous process —
// so it cannot simply be "answered". Its task (if any) is moved to waiting_input carrying the actual
// question that was pending, so the user sees precisely what PRISM was asking instead of the task silently
// restarting from its original input as if nothing had happened. A task with no ask.Task (e.g. an
// ask_colleague run) has nothing to reconcile into; its row is just cleared.
func (e *Engine) ReconcilePendingAsks(ctx context.Context) (int, error) {
	if e.DB == nil {
		return 0, nil
	}
	rows, err := e.DB.Query(ctx, `SELECT id, task_id, question FROM pending_asks`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id       int64
		taskID   *int64
		question string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.taskID, &r.question); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, r)
	}
	rows.Close()
	n := 0
	for _, r := range pending {
		if r.taskID != nil {
			q := r.question
			if q == "" {
				q = "PRISM restarted while waiting for your confirmation on a risky action; review before continuing."
			}
			_ = e.Tasks.Finish(ctx, *r.taskID, tasks.WaitingInput, "", "", q)
		}
		if _, err := e.DB.Exec(ctx, `DELETE FROM pending_asks WHERE id=$1`, r.id); err == nil {
			n++
		}
	}
	return n, nil
}

func (e *Engine) PendingAsks() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []map[string]any
	for _, pa := range e.asks {
		out = append(out, map[string]any{"id": pa.ID, "run": pa.Run.ID, "agent": pa.Run.Agent, "kind": pa.Q.Kind, "text": pa.Q.Text,
			"options": pa.Q.Options, "tool": pa.Q.Tool, "args": pa.Q.Args, "since": pa.Since.UnixMilli()})
	}
	return out
}

// ── chat ───────────────────────────────────────────────────────────────────

type ChatMsg struct {
	ID     int64   `json:"id"`
	Role   string  `json:"role"`
	Agent  string  `json:"agent"`
	Text   string  `json:"text"`
	Images []int64 `json:"images"` // artifact ids of attached pictures
	// Steered marks a user message sent while an agent was already working: it steers the running turn.
	Steered bool `json:"steered,omitempty"`
	// MovedFrom is the chat a message came from when chats were merged (MovedTitle its title).
	MovedFrom  string    `json:"moved_from,omitempty"`
	MovedTitle string    `json:"moved_title,omitempty"`
	Channel    string    `json:"channel"`
	Topic      string    `json:"topic"`
	TaskID     *int64    `json:"task_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func (e *Engine) logChat(ctx context.Context, role, agent, text, channel, topic string, taskID int64) ChatMsg {
	return e.logChatImages(ctx, role, agent, text, channel, topic, taskID, nil)
}

func (e *Engine) logChatImages(ctx context.Context, role, agent, text, channel, topic string, taskID int64, images []int64) ChatMsg {
	return e.logChatFull(ctx, role, agent, text, channel, topic, taskID, images, false)
}

func (e *Engine) logChatFull(ctx context.Context, role, agent, text, channel, topic string, taskID int64, images []int64, steered bool) ChatMsg {
	m := ChatMsg{Role: role, Agent: agent, Text: text, Channel: channel, Topic: topic, Images: imgs(images), Steered: steered}
	var tid *int64
	if taskID != 0 {
		tid = &taskID
		m.TaskID = tid
	}
	_ = e.DB.QueryRow(ctx, `INSERT INTO chat_messages(role,agent,text,channel,topic,task_id,images,steered) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,created_at`,
		role, agent, text, channel, topic, tid, m.Images, steered).Scan(&m.ID, &m.CreatedAt)
	e.Emit("chat.message", m)
	return m
}

// ChatHistory returns the latest messages of a channel/topic in chronological order.
func (e *Engine) ChatHistory(ctx context.Context, channel, topic string, limit int) ([]ChatMsg, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := e.DB.Query(ctx, `SELECT x.id,x.role,x.agent,x.text,x.channel,x.topic,x.task_id,x.created_at,x.images,x.steered,x.moved_from,CASE WHEN x.moved_from='' THEN '' ELSE COALESCE(NULLIF(c.title,''),'a merged chat') END FROM
		(SELECT * FROM chat_messages WHERE channel=$1 AND topic=$2 ORDER BY id DESC LIMIT $3) x LEFT JOIN chats c ON c.topic=x.moved_from AND x.moved_from<>'' ORDER BY x.id`, channel, topic, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatMsg
	for rows.Next() {
		var m ChatMsg
		if err := rows.Scan(&m.ID, &m.Role, &m.Agent, &m.Text, &m.Channel, &m.Topic, &m.TaskID, &m.CreatedAt, &m.Images, &m.Steered, &m.MovedFrom, &m.MovedTitle); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func ChatKey(channel, topic string) string {
	switch {
	case channel == "telegram" && topic != "":
		return "tg:topic:" + topic
	case channel == "telegram":
		return "tg:dm"
	case topic != "":
		return "web:" + topic // one of the user's several web chats (see chats.go)
	}
	return "web"
}

// Upload is a picture that came with a user message.
type Upload struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data []byte `json:"data"` // base64 on the wire
}

// Limits for pictures attached to one message.
const (
	MaxUploads     = 4
	MaxUploadBytes = 6 << 20 // 4 × 6 MB as base64 stays under the socket limit
)

// checkUploads validates pictures by content (not by the claimed type) and returns their real MIME types.
func checkUploads(us []Upload) ([]string, error) {
	if len(us) > MaxUploads {
		return nil, fmt.Errorf("at most %d images per message", MaxUploads)
	}
	mimes := make([]string, len(us))
	for i, u := range us {
		if len(u.Data) == 0 || len(u.Data) > MaxUploadBytes {
			return nil, fmt.Errorf("image %d: empty or larger than %d MB", i+1, MaxUploadBytes>>20)
		}
		switch m := http.DetectContentType(u.Data); m {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
			mimes[i] = m
		default:
			return nil, fmt.Errorf("image %d: %s is not a supported picture (JPEG, PNG, GIF or WebP)", i+1, m)
		}
	}
	return mimes, nil
}

type UserMsg struct {
	Text    string
	Images  []Upload
	Channel string // web | telegram
	Topic   string
	// Reply receives the assistant's final answer (used by Telegram); may be nil.
	Reply func(agent, text string)
}

// UserMessage is the entry point for everything the user types. If Atlas is
// already working on this conversation the message steers the running turn
// instead of being queued or failing.
func (e *Engine) UserMessage(ctx context.Context, m UserMsg) error {
	m.Text = strings.TrimSpace(m.Text)
	if m.Text == "" && len(m.Images) == 0 {
		return nil
	}
	if m.Channel == "" {
		m.Channel = "web"
	}
	var imageIDs []int64
	if len(m.Images) > 0 {
		if e.SaveImage == nil {
			return errors.New("image input is not available")
		}
		mimes, err := checkUploads(m.Images)
		if err != nil {
			return err
		}
		for i, u := range m.Images {
			name := strings.TrimSpace(u.Name)
			if name == "" {
				name = fmt.Sprintf("image-%d", i+1)
			}
			id, err := e.SaveImage(ctx, name, mimes[i], u.Data)
			if err != nil {
				return err
			}
			imageIDs = append(imageIDs, id)
		}
	}
	key := ChatKey(m.Channel, m.Topic)
	e.mu.Lock()
	working := e.chatRun[key] != nil
	e.mu.Unlock()
	e.logChatFull(ctx, "user", "", m.Text, m.Channel, m.Topic, 0, imageIDs, working)
	e.touchChat(ctx, m.Channel, m.Topic)
	if m.Channel == "web" && m.Topic != "" && m.Text != "" { // a provisional title straight from the first message; a model refines it later
		if r, err := e.DB.Exec(ctx, `UPDATE chats SET title=$2 WHERE topic=$1 AND auto_title AND title=''`, m.Topic, briefText(m.Text, 40)); err == nil && r.RowsAffected() > 0 {
			e.Emit("chats.update", map[string]any{"topic": m.Topic})
		}
	}
	if e.Memory != nil && e.ChatRemembers(ctx, m.Channel, m.Topic) {
		note := m.Text
		if len(imageIDs) > 0 {
			note = strings.TrimSpace(note + fmt.Sprintf(" [%d image(s) attached]", len(imageIDs)))
		}
		_ = e.Memory.AddRaw(ctx, memory.RawMsg{From: "user", To: "Atlas", Channel: m.Channel, Topic: m.Topic, Text: note, Agent: "Atlas"})
	}
	e.mu.Lock()
	if ar := e.chatRun[key]; ar != nil {
		steerText := m.Text
		if steerText == "" {
			steerText = "(the user sent an image)"
		}
		ar.addImages(imageIDs)
		select {
		case ar.steer <- steerText:
			e.mu.Unlock()
			e.Emit("chat.steered", map[string]any{"key": key})
			return nil
		default:
			e.mu.Unlock()
			return errors.New("too many pending messages; wait for Atlas")
		}
	}
	atlas, err := e.Profiles.Get(ctx, "Atlas")
	if err != nil {
		e.mu.Unlock()
		return err
	}
	sess, err := e.Sessions.Chat(ctx, atlas.Name, key)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	steer := make(chan string, 32)
	ar := &activeRun{steer: steer, started: time.Now()}
	ar.Info = RunInfo{ID: e.runSeq.Add(1), Agent: atlas.Name, SessionID: sess.ID, IsChat: true, ChatTopic: m.Topic, ChatChannel: m.Channel}
	e.chatRun[key] = ar
	e.mu.Unlock()
	go e.chatLoop(context.WithoutCancel(ctx), m, imageIDs, key, atlas, sess, ar)
	return nil
}

func (e *Engine) chatLoop(ctx context.Context, m UserMsg, images []int64, key string, atlas *Profile, sess *Session, ar *activeRun) {
	input := m.Text
	for {
		task, terr := e.Tasks.Create(ctx, tasks.Task{FromKind: "user", FromName: "user", ToAgent: atlas.Name, Input: input, SessionID: &sess.ID}, true)
		ar.Info.TaskID = task.ID
		ar.Info.Title = task.Title
		ar.task.Store(brief(input, 90))
		spec := RunSpec{Profile: atlas, Session: sess, Input: input, InputImages: images, Provenance: "user", Channel: m.Channel, Topic: m.Topic,
			Interactive: true, Steer: ar.steer, Run: ar}
		if terr == nil {
			spec.Task = &task
		}
		res, err := e.Run(ctx, spec)
		switch {
		case err != nil && errors.Is(err, context.Canceled):
			e.logChat(ctx, "system", "", "Stopped.", m.Channel, m.Topic, task.ID)
			_ = e.Tasks.Finish(ctx, task.ID, tasks.Cancelled, "", "stopped by user", "")
		case err != nil:
			e.logChat(ctx, "system", "", "Error: "+err.Error(), m.Channel, m.Topic, task.ID)
			_ = e.Tasks.Finish(ctx, task.ID, tasks.Failed, "", err.Error(), "")
		default:
			text := res.Text
			if res.NeedsInput != "" {
				text = res.NeedsInput
			}
			if text == "" {
				text = "(no answer)"
			}
			e.logChat(ctx, "agent", atlas.Name, text, m.Channel, m.Topic, task.ID)
			e.touchChat(ctx, m.Channel, m.Topic)
			if m.Channel == "web" && m.Topic != "" {
				go e.nameChat(context.WithoutCancel(ctx), m.Topic) // a title and a project suggestion after the first exchange
			}
			if e.Memory != nil && e.ChatRemembers(ctx, m.Channel, m.Topic) {
				_ = e.Memory.AddRaw(ctx, memory.RawMsg{From: atlas.Name, To: "user", Channel: m.Channel, Topic: m.Topic, Text: text, TaskID: task.ID, Agent: atlas.Name, Tainted: res.Tainted})
			}
			_ = e.Tasks.Finish(ctx, task.ID, tasks.Done, text, "", "")
			if m.Reply != nil {
				m.Reply(atlas.Name, text)
			}
		}
		// messages that raced in after the last drain start another turn
		e.mu.Lock()
		left := drain(ar.steer)
		if len(left) == 0 {
			delete(e.chatRun, key)
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
		input = strings.Join(left, "\n\n")
		images = ar.takeImages()
		// the run entry stays registered; a new activeRun id is not needed for steering
	}
}

// NotifySinks pushes a notice to the external channels (macOS, Telegram) without touching the chat log.
func (e *Engine) NotifySinks(ctx context.Context, n Notice) {
	for _, s := range e.Sinks {
		go s.Notice(context.WithoutCancel(ctx), n)
	}
}

// PostSystem writes a system message (command output) into the visible conversation.
func (e *Engine) PostSystem(ctx context.Context, channel, topic, text string) {
	e.logChat(ctx, "system", "", text, channel, topic, 0)
}

// QuickAsk runs one minimal-context turn: a fresh, never-reused session and no automatic memory recall —
// for a small, self-contained request (e.g. "create an agent that uses the tracker tools") that doesn't
// need the conversational/recall overhead of a normal chat turn. Unlike UserMessage this is synchronous
// (blocks until the run finishes) and never steers an already-running turn — there is never one, since every
// call gets its own fresh session — and it is not interactive: a clarifying question the run can't answer on
// its own surfaces as an error inside that tool call, same as any other non-interactive/background run
// (crons, intents), not a hang. A confirm-required action still raises a normal ask notification (the bell),
// just not inside the popup itself.
func (e *Engine) QuickAsk(ctx context.Context, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("empty request")
	}
	atlas, err := e.Profiles.Get(ctx, "Atlas")
	if err != nil {
		return "", err
	}
	sess, err := e.Sessions.Create(ctx, atlas.Name, "quick", "", 0)
	if err != nil {
		return "", err
	}
	task, terr := e.Tasks.Create(ctx, tasks.Task{FromKind: "user", FromName: "user", ToAgent: atlas.Name, Input: text, SessionID: &sess.ID}, true)
	spec := RunSpec{Profile: atlas, Session: sess, Input: text, Provenance: "user", NoRecall: true, Kind: "quick"}
	if terr == nil {
		spec.Task = &task
	}
	res, err := e.Run(ctx, spec)
	if err != nil {
		if terr == nil {
			_ = e.Tasks.Finish(ctx, task.ID, tasks.Failed, "", err.Error(), "")
		}
		return "", err
	}
	out := res.Text
	if res.NeedsInput != "" {
		out = res.NeedsInput
	}
	if out == "" {
		out = "(no answer)"
	}
	if terr == nil {
		_ = e.Tasks.Finish(ctx, task.ID, tasks.Done, out, "", "")
	}
	return out, nil
}

// Greet posts Atlas's opening message when the web conversation is still empty, so the
// conversation starts with Atlas rather than an empty box.
func (e *Engine) Greet(ctx context.Context, text string) {
	var n int
	_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM chat_messages WHERE channel='web' AND topic=''`).Scan(&n)
	if n > 0 {
		return
	}
	e.logChat(ctx, "agent", "Atlas", text, "web", "", 0)
	e.NoteToChat(ctx, "web", "Atlas", text)
}

// StopChat cancels the active Atlas turn of a conversation.
func (e *Engine) StopChat(key string) bool {
	e.mu.Lock()
	ar := e.chatRun[key]
	e.mu.Unlock()
	if ar == nil || ar.cancel == nil {
		return false
	}
	ar.cancel()
	go e.cancelDescendants(ar.Info.TaskID) // delegated work started by this turn stops with it
	return true
}

// cancelDescendants stops the running or queued tasks below a task.
func (e *Engine) cancelDescendants(taskID int64) {
	if taskID == 0 {
		return
	}
	ctx := context.Background()
	root := taskID
	if t, err := e.Tasks.Get(ctx, taskID); err == nil && t.RootID != 0 {
		root = t.RootID
	}
	rows, err := e.DB.Query(ctx, `SELECT id FROM tasks WHERE root_id=$1 AND id<>$2 AND status IN ('queued','running')`, root, taskID)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_ = e.CancelTask(ctx, id)
	}
}

func (e *Engine) ChatBusy(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.chatRun[key] != nil
}

// ClearChat wipes a conversation's agent context (chat log is kept unless purge).
func (e *Engine) ClearChat(ctx context.Context, channel, topic string, purge bool) error {
	key := ChatKey(channel, topic)
	e.StopChat(key)
	sess, err := e.Sessions.Chat(ctx, "Atlas", key)
	if err != nil {
		return err
	}
	if err := e.Sessions.Clear(ctx, sess.ID); err != nil {
		return err
	}
	_ = e.Sessions.SetScratch(ctx, sess.ID, "")
	if purge {
		_, err = e.DB.Exec(ctx, `DELETE FROM chat_messages WHERE channel=$1 AND topic=$2`, channel, topic)
	}
	e.Emit("chat.cleared", map[string]any{"channel": channel, "topic": topic})
	return err
}

// CompactChat compacts a chat session on demand.
func (e *Engine) CompactChat(ctx context.Context, channel, topic string) error {
	atlas, err := e.Profiles.Get(ctx, "Atlas")
	if err != nil {
		return err
	}
	sess, err := e.Sessions.Chat(ctx, atlas.Name, ChatKey(channel, topic))
	if err != nil {
		return err
	}
	return e.CompactNow(ctx, sess, atlas)
}

// NoteToChat appends an assistant note to a chat session so later user replies have context
// (e.g. an agent dropped a message into a Telegram topic).
func (e *Engine) NoteToChat(ctx context.Context, key, agent, text string) {
	sess, err := e.Sessions.Chat(ctx, "Atlas", key)
	if err != nil {
		return
	}
	_, _ = e.Sessions.Append(ctx, sess.ID, Msg{Message: llmMsg("assistant", fmt.Sprintf("[message from %s to the user] %s", agent, text)), Provenance: "agent"})
}

// Notify delivers an agent-initiated message: chat log + UI event + external sinks.
func (e *Engine) Notify(ctx context.Context, n Notice) {
	if n.Agent == "" {
		n.Agent = "Atlas"
	}
	if n.Topic == "" {
		e.logChat(ctx, "agent", n.Agent, n.Text, "web", "", 0)
		e.NoteToChat(ctx, "web", n.Agent, n.Text)
	}
	e.Emit("notice", n)
	for _, s := range e.Sinks {
		go s.Notice(context.WithoutCancel(ctx), n)
	}
}

// ── tasks: dispatcher, execution, delegation ──────────────────────────────

var wakeMu sync.Mutex

// Enqueue puts a task on the queue and wakes the dispatcher.
func (e *Engine) Enqueue(ctx context.Context, t tasks.Task) (tasks.Task, error) {
	out, err := e.Tasks.Create(ctx, t, false)
	if err == nil {
		e.wake()
	}
	return out, err
}

var wakeCh = make(chan struct{}, 1)

func (e *Engine) wake() {
	select {
	case wakeCh <- struct{}{}:
	default:
	}
}

// StartDispatcher processes the queue until ctx ends. Tasks that were running
// when the process stopped are requeued so nothing is lost across restarts.
func (e *Engine) StartDispatcher(ctx context.Context, maxRuns int) {
	if maxRuns <= 0 {
		maxRuns = 6
	}
	_, _ = e.ReconcilePendingAsks(ctx)
	_, _ = e.Tasks.RequeueRunning(ctx)
	sem := make(chan struct{}, maxRuns)
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			for {
				select {
				case sem <- struct{}{}:
				default:
					goto wait
				}
				t, err := e.Tasks.ClaimNext(ctx)
				if err != nil || t == nil {
					<-sem
					break
				}
				go func(t tasks.Task) {
					defer func() { <-sem }()
					e.RunTask(ctx, t, TaskOpts{})
				}(*t)
			}
		wait:
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			case <-wakeCh:
			}
		}
	}()
}

type TaskOpts struct {
	Input      string // overrides t.Input for this turn (multi-turn continuation)
	Tainted    bool
	ParentRun  int64
	Leaf       bool // a colleague's one-off help: no further delegation
	Restricted bool // inherited from a probationary requester
}

// RunTask executes a claimed (running) task to completion and records the outcome.
func (e *Engine) RunTask(ctx context.Context, t tasks.Task, o TaskOpts) tasks.Task {
	tctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.cancels[t.ID] = cancel
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.cancels, t.ID)
		e.mu.Unlock()
	}()
	fail := func(err error) tasks.Task {
		st := tasks.Failed
		if errors.Is(err, context.Canceled) {
			st = tasks.Cancelled
		}
		_ = e.Tasks.Finish(ctx, t.ID, st, "", err.Error(), "")
		out, _ := e.Tasks.Get(ctx, t.ID)
		return out
	}
	p, err := e.Profiles.Get(tctx, t.ToAgent)
	if err != nil {
		return fail(err)
	}
	if !p.Enabled {
		return fail(fmt.Errorf("agent %s is disabled", p.Name))
	}
	var sess *Session
	if t.SessionID != nil {
		sess, err = e.Sessions.Get(tctx, *t.SessionID, "", "", "")
	} else {
		sess, err = e.Sessions.Create(tctx, p.Name, "task", "", t.ID)
		if err == nil {
			_ = e.Tasks.SetSession(tctx, t.ID, sess.ID)
		}
	}
	if err != nil {
		return fail(err)
	}
	// A restart-requeued task (restarts > 0, RequeueRunning) resumes the same session it had before, tool
	// calls and all — it must not treat whatever it was doing as unstarted. A note in the session, not a
	// rewritten instruction, tells it to reconcile before repeating anything with a real effect.
	if t.Restarts > 0 {
		if hist, herr := e.Sessions.Messages(tctx, sess.ID); herr == nil && len(hist) > 0 {
			note := "[system] PRISM restarted while you were working on this task. Before continuing, check the tool calls and results already above: if something with a real effect (a message sent, a file written, a purchase, a deletion…) already appears to have completed, do NOT repeat it — confirm its outcome first if you can, or say plainly what you are unsure happened. If nothing had actually happened yet, continue normally."
			_, _ = e.Sessions.Append(tctx, sess.ID, Msg{Message: llm.Message{Role: "user", Content: note}, Provenance: "system"})
		}
	}
	input := o.Input
	if input == "" {
		input = t.Input
		if t.FromKind == "agent" && t.RootID != 0 && t.RootID != t.ID {
			if root, rerr := e.Tasks.Get(tctx, t.RootID); rerr == nil && root.FromKind == "user" && root.Input != "" && root.Input != t.Input {
				input += "\n\n(Original user request, for context only: " + brief(root.Input, 400) + ")"
			}
		}
	}
	prov := "agent"
	if t.FromKind == "cron" || t.FromKind == "intent" || t.FromKind == "system" {
		prov = "system"
	}
	spec := RunSpec{Profile: p, Session: sess, Task: &t, Input: input, Provenance: prov, Tainted: o.Tainted, Leaf: o.Leaf, Restricted: o.Restricted,
		Channel: "task", Interactive: t.Depth == 0 && (t.FromKind == "user" || t.FromKind == "telegram"), Depth: t.Depth, ParentRun: o.ParentRun}
	if t.Depth > 0 { // a delegated task can be redirected by whoever delegated it (task_steer)
		steerCh := make(chan string, 8)
		spec.Steer, spec.SteerFrom = steerCh, t.FromName
		e.mu.Lock()
		if e.taskSteer == nil {
			e.taskSteer = map[int64]chan string{}
		}
		e.taskSteer[t.ID] = steerCh
		e.mu.Unlock()
		defer func() {
			e.mu.Lock()
			delete(e.taskSteer, t.ID)
			e.mu.Unlock()
		}()
	}
	res, err := e.Run(tctx, spec)
	if err != nil {
		return fail(err)
	}
	var recov *recovery
	if res.Aborted != "" && res.NeedsInput == "" && tctx.Err() == nil && e.autoRetryOn(tctx) {
		var plan recovery
		res, plan = e.recoverRun(tctx, t, p, sess, res, spec)
		recov = &plan
	}
	if e.Memory != nil && t.FromKind == "agent" {
		_ = e.Memory.AddRaw(ctx, memory.RawMsg{From: t.FromName, To: p.Name, Text: t.Input, TaskID: t.ID, Agent: p.Name, Tainted: res.Tainted})
		_ = e.Memory.AddRaw(ctx, memory.RawMsg{From: p.Name, To: t.FromName, Text: res.Text, TaskID: t.ID, Agent: p.Name, Tainted: res.Tainted})
	}
	switch {
	case res.NeedsInput != "":
		_ = e.Tasks.Finish(ctx, t.ID, tasks.WaitingInput, res.Text, "", res.NeedsInput)
	case res.Aborted != "": // budget exhausted or stopped by the loop guard: a partial result, not a real answer
		if t.Depth > 0 && recov != nil { // delegated: the parent gets a failure with the analysis and decides what to tell the user
			msg := fmt.Sprintf("%s — gave up%s. Why: %s Achieved so far: %s", res.Aborted, map[bool]string{true: " after an automatic retry"}[recov.Retried], recov.Lesson, brief(recov.Achieved, 600))
			_ = e.Tasks.Finish(ctx, t.ID, tasks.Failed, res.Text, msg, "")
			break
		}
		_ = e.Tasks.Finish(ctx, t.ID, tasks.Partial, res.Text, res.Aborted, "")
		if t.Depth == 0 && (t.FromKind == "user" || t.FromKind == "telegram") {
			e.Notify(ctx, Notice{Agent: p.Name, Level: "attention", Text: fmt.Sprintf("“%s” stopped before finishing (%s). Open Today → Needs your attention to review what happened and choose how to proceed.", brief(t.Title, 60), res.Aborted)})
		}
	default:
		_ = e.Tasks.Finish(ctx, t.ID, tasks.Done, res.Text, "", "")
	}
	out, _ := e.Tasks.Get(ctx, t.ID)
	if e.OnTaskDone != nil && out.Status == tasks.Done {
		e.OnTaskDone(ctx, out)
	}
	// autonomous top-level tasks report back to the user unless the agent stays silent
	if t.Depth == 0 && (t.FromKind == "cron" || t.FromKind == "intent" || t.FromKind == "system") && res.NeedsInput == "" {
		txt := strings.TrimSpace(res.Text)
		if txt != "" && !strings.EqualFold(strings.Trim(txt, ". \n"), "NO_REPLY") && !strings.HasPrefix(txt, "[partial") {
			e.Notify(ctx, Notice{Agent: p.Name, Text: txt, Level: "info"})
		}
	}
	return out
}

// CancelTask cancels a task; a running one has its context cancelled.
func (e *Engine) CancelTask(ctx context.Context, id int64) error {
	e.mu.Lock()
	c := e.cancels[id]
	e.mu.Unlock()
	if c != nil {
		c()
	}
	t, err := e.Tasks.Get(ctx, id)
	if err != nil {
		return err
	}
	if !t.Terminal() {
		return e.Tasks.Cancel(ctx, id)
	}
	return nil
}
