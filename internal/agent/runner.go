package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/llm"
	"prism/internal/memory"
	"prism/internal/settings"
	"prism/internal/tasks"
	"prism/internal/tasksum"
	"prism/internal/tools"
)

// SkillSummary is the progressive-disclosure view of a skill (name + description only).
type SkillSummary struct{ Name, Description string }

// SkillSource supplies skill summaries for the system prompt.
type SkillSource interface {
	Summaries(ctx context.Context, names []string) []SkillSummary
}

// Deps are the collaborators an Engine needs.
type Deps struct {
	DB       *pgxpool.Pool
	LLM      *llm.Router
	Tools    *tools.Registry
	Profiles *ProfileStore
	Sessions *SessionStore
	Tasks    *tasks.Store
	Memory   *memory.Service
	// TaskSum stores/searches task summaries distilled from finished tasks (see SummarizeTask, SummarizeDue,
	// internal/tasksum) — "what did we try, and why?", a question a single atomic memory fact cannot answer.
	TaskSum  *tasksum.Store
	Settings *settings.Store
	// DefaultBanks is the same bank list memory_find falls back to when the caller gives none (user's bank,
	// the agent's own profile bank, its configured banks, active project banks). Used for automatic recall
	// (see (*Engine).recall) so the two stay in lockstep; nil falls back to just user + own profile.
	DefaultBanks func(ctx context.Context, agent string) []string
	Skills       SkillSource
	Emit         func(typ string, data any)
	// Export saves text as an artifact and returns a confirmation (used by /export).
	Export func(ctx context.Context, name, content string) (string, error)
	// SaveImage stores an uploaded picture (as an artifact) and returns its id; LoadImage reads it back.
	SaveImage func(ctx context.Context, name, mime string, data []byte) (int64, error)
	LoadImage func(ctx context.Context, id int64) (mime string, data []byte, err error)
	// OnTool is told about every executed tool call (for the usage dashboard and delegation-cost
	// measurement); may be nil. taskID/runID/parentRun are 0 when not tied to one.
	OnTool func(agent, tool string, ms int64, ok bool, errText string, taskID, runID, parentRun int64)
}

// Engine runs agents: the runner loop, sessions, delegation and asks.
type Engine struct {
	Deps

	semMu     sync.Mutex
	llmSem    chan struct{}
	runSeq    atomic.Int64
	askSeq    atomic.Int64
	mu        sync.Mutex
	runs      map[int64]*activeRun
	chatRun   map[string]*activeRun // chat session key → active Atlas run (for steering)
	taskSteer map[int64]chan string // delegated task id → its steering channel (task_steer)
	asks      map[int64]*pendingAsk
	cancels   map[int64]context.CancelFunc // task id → cancel
	Sinks     []NoticeSink
	// OnTaskDone, when set, is called after a task finished successfully (used to review coding workspaces).
	OnTaskDone func(ctx context.Context, t tasks.Task)
}

func NewEngine(d Deps) *Engine {
	en := &Engine{Deps: d}
	en.llmSem = make(chan struct{}, 4)
	en.runs = map[int64]*activeRun{}
	en.chatRun = map[string]*activeRun{}
	en.asks = map[int64]*pendingAsk{}
	en.cancels = map[int64]context.CancelFunc{}
	if en.Emit == nil {
		en.Emit = func(string, any) {}
	}
	return en
}

// SetLLMConcurrency bounds simultaneous model calls (local models usually want 1–2).
func (e *Engine) SetLLMConcurrency(n int) {
	if n < 1 {
		n = 1
	}
	e.semMu.Lock()
	e.llmSem = make(chan struct{}, n)
	e.semMu.Unlock()
}

// sem returns the current model-call semaphore. A call must release on the channel it acquired
// (not on whatever e.llmSem is by then), so resizing at runtime cannot strand a running agent.
func (e *Engine) sem() chan struct{} {
	e.semMu.Lock()
	defer e.semMu.Unlock()
	return e.llmSem
}

type activeRun struct {
	Info    RunInfo
	steer   chan string
	cancel  context.CancelFunc
	started time.Time
	// live counters shown in the UI
	tokensIn, tokensOut atomic.Int64
	ctxTokens, window   atomic.Int64
	task                atomic.Value // string: brief current task
	approved            sync.Map     // tool name → true: user chose "allow for this task"
	imgMu               sync.Mutex
	pendingImages       []int64 // pictures sent while the run was working, delivered with the next steering message
}

func (ar *activeRun) addImages(ids []int64) {
	ar.imgMu.Lock()
	ar.pendingImages = append(ar.pendingImages, ids...)
	ar.imgMu.Unlock()
}

func (ar *activeRun) takeImages() []int64 {
	ar.imgMu.Lock()
	defer ar.imgMu.Unlock()
	ids := ar.pendingImages
	ar.pendingImages = nil
	return ids
}

// RunInfo identifies a run in events.
type RunInfo struct {
	ID        int64  `json:"run"`
	Agent     string `json:"agent"`
	TaskID    int64  `json:"task"`
	Depth     int    `json:"depth"`
	ParentRun int64  `json:"parent_run,omitempty"`
	SessionID int64  `json:"session"`
	Title     string `json:"title,omitempty"`
	Kind      string `json:"kind,omitempty"` // "ask": a colleague answering ask_colleague (the graph draws it as a request line)
	// IsChat marks the Atlas turn of a user chat; ChatTopic says which web chat ("" is the main one).
	IsChat      bool   `json:"is_chat,omitempty"`
	ChatTopic   string `json:"chat_topic,omitempty"`
	ChatChannel string `json:"chat_channel,omitempty"`
}

// NewRunID hands out a run id for work that shows up as an agent in the UI without being an
// engine run (the knowledge-base writer streams its pages this way).
func (e *Engine) NewRunID() int64 { return e.runSeq.Add(1) }

// ActiveRun is the serialisable snapshot of an in-flight run.
type ActiveRun struct {
	RunInfo
	TokensIn  int64  `json:"tokens_in"`
	TokensOut int64  `json:"tokens_out"`
	Context   int64  `json:"context"`
	Window    int64  `json:"window"`
	Task      string `json:"task_text"`
	Started   int64  `json:"started"`
}

func (e *Engine) ActiveRuns() []ActiveRun {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ActiveRun, 0, len(e.runs))
	for _, r := range e.runs {
		t, _ := r.task.Load().(string)
		out = append(out, ActiveRun{RunInfo: r.Info, TokensIn: r.tokensIn.Load(), TokensOut: r.tokensOut.Load(),
			Context: r.ctxTokens.Load(), Window: r.window.Load(), Task: t, Started: r.started.UnixMilli()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type RunSpec struct {
	Profile *Profile
	Session *Session
	Task    *tasks.Task
	Input   string
	// Restricted removes exec tools (an agent on probation, or a run started on its behalf).
	Restricted bool
	// Leaf marks a run started by ask_colleague: it can use its tools but cannot delegate or ask colleagues in
	// turn, so requests for help never chain.
	Leaf        bool
	InputImages []int64 // pictures that came with Input (artifact ids)
	Provenance  string  // origin of Input: user | agent | system
	Tainted     bool
	Channel     string
	Topic       string
	Interactive bool
	Steer       chan string
	SteerFrom   string // set for a delegated task: the delegating agent whose messages arrive on Steer (not the user)
	ExtraTools  []string
	Depth       int
	Preamble    string
	ParentRun   int64
	Run         *activeRun // pre-registered by the caller (chat steering); nil → created here
	// Kind tags what kind of run this is for the UI (e.g. "quick" for QuickAsk) — inherited by every run it
	// delegates to (see register()), so the whole resulting tree can be told apart from an unrelated chat
	// conversation's own run tree even after several hops of delegation. Leaf sets "ask" the same way.
	Kind string
	// NoRecall skips the automatic memory-recall pack for this turn (see (*Engine).recall) — for a small,
	// self-contained request that doesn't need the user's history pulled in, the recall lookup itself is
	// pure overhead. Used by QuickAsk.
	NoRecall bool
	// recall is the auto-recalled context pack for this turn (see (*Engine).recall), computed once before the
	// iteration loop and read by buildSystem on every iteration — never re-fetched mid-turn.
	recall string
}

type RunResult struct {
	Text       string
	NeedsInput string
	Aborted    string
	Iterations int
	TokensIn   int
	TokensOut  int
	// Tainted is true when untrusted content (a web page, mail, an MCP result…) entered this run's context at
	// any point. Callers that log this run's text to memory's raw bank must carry it through, or a fact
	// distilled later has no way to know it rests on unverified ground.
	Tainted bool
	// Sources are the web hosts this run read (see tools.Sources); the chat uses them to decide which image links in
	// an answer may be fetched and shown.
	Sources *tools.Sources
}

const (
	maxToolResultChars = 14000
	defaultToolTimeout = 3 * time.Minute
)

func (e *Engine) register(spec RunSpec) *activeRun {
	if spec.Run != nil {
		e.mu.Lock()
		e.runs[spec.Run.Info.ID] = spec.Run
		e.mu.Unlock()
		return spec.Run
	}
	ar := &activeRun{steer: spec.Steer, started: time.Now()}
	kind := spec.Kind
	if kind == "" && spec.Leaf {
		kind = "ask"
	}
	e.mu.Lock()
	// a delegated run with no kind of its own inherits its parent's — so a whole tree started by, say, a
	// QuickAsk stays identifiable as such even several delegation hops down, and a chat page (or anything
	// else) that only wants its own conversation's activity can filter the entire tree out in one check.
	if kind == "" && spec.ParentRun != 0 {
		if parent, ok := e.runs[spec.ParentRun]; ok {
			kind = parent.Info.Kind
		}
	}
	ar.Info = RunInfo{ID: e.runSeq.Add(1), Agent: spec.Profile.Name, Depth: spec.Depth, ParentRun: spec.ParentRun, SessionID: spec.Session.ID, Kind: kind}
	if spec.Task != nil {
		ar.Info.TaskID, ar.Info.Title = spec.Task.ID, spec.Task.Title
	}
	e.runs[ar.Info.ID] = ar
	e.mu.Unlock()
	return ar
}

func brief(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Run executes one agent turn-sequence (until a final answer, a needed input, or budget exhaustion).
func (e *Engine) Run(ctx context.Context, spec RunSpec) (*RunResult, error) {
	p := spec.Profile
	if spec.Channel == "web" { // memory recall in this run (and its delegates) favours the chat's project
		ctx = withChat(ctx, spec.Channel, spec.Topic)
	}
	spec.Restricted = spec.Restricted || p.Probation
	ar := e.register(spec)
	ctx, cancel := context.WithCancel(ctx)
	ar.cancel = cancel
	defer cancel()
	if ar.task.Load() == nil {
		ar.task.Store(brief(spec.Input, 90))
	}
	e.Emit("run.start", ar.Info)
	res := &RunResult{}
	status, errMsg := "done", ""
	defer func() {
		e.mu.Lock()
		delete(e.runs, ar.Info.ID)
		e.mu.Unlock()
		e.Emit("run.end", map[string]any{"run": ar.Info.ID, "agent": p.Name, "task": ar.Info.TaskID, "depth": spec.Depth,
			"status": status, "error": errMsg, "tokens_in": res.TokensIn, "tokens_out": res.TokensOut,
			"is_chat": ar.Info.IsChat, "chat_topic": ar.Info.ChatTopic})
	}()

	sess := spec.Session
	history, err := e.Sessions.Messages(ctx, sess.ID)
	if err != nil {
		status, errMsg = "failed", err.Error()
		return res, err
	}
	// taint: the run starts tainted when the input is, or when untrusted content is
	// still in scope since the last trusted user message.
	tainted := spec.Tainted
	for i := len(history) - 1; i >= 0 && !tainted; i-- {
		if history[i].Provenance == "user" {
			break
		}
		tainted = history[i].Tainted
	}
	defer func() { res.Tainted = tainted }() // whatever tainted ends at when this Run returns, however it returns

	modelRef := p.Model
	window := e.LLM.Window(ctx, modelRef)
	ar.window.Store(int64(window))

	add := func(m Msg) error {
		id, err := e.Sessions.Append(ctx, sess.ID, m)
		if err != nil {
			return err
		}
		m.ID = id
		history = append(history, m)
		return nil
	}
	if spec.Input != "" || len(spec.InputImages) > 0 {
		prov := spec.Provenance
		if prov == "" {
			prov = "user"
		}
		if err := add(Msg{Message: llm.Message{Role: "user", Content: spec.Input}, Provenance: prov, Tainted: spec.Tainted, ImageIDs: spec.InputImages}); err != nil {
			status, errMsg = "failed", err.Error()
			return res, err
		}
	}

	active := e.initialTools(ctx, p, spec)
	activate := func(names ...string) {
		for _, n := range names {
			if spec.Leaf && (n == "delegate" || n == "ask_colleague") {
				continue // tool_search must not hand a leaf run a way to pass the request on
			}
			if t, ok := e.Tools.Get(n); ok && !(spec.Restricted && t.Risk == tools.RiskExec) && t.AllowedFor(p.Name) {
				active[n] = true
			}
		}
	}
	// Sherpa: pick extra tools for this task from the repository (progressive disclosure).
	if p.AutoTools && p.Role != RoleEntry && spec.Input != "" {
		picked := e.selectTools(ctx, spec.Input, active, p.Name)
		activate(picked...)
	}

	env := &tools.Env{
		Agent: p.Name, Profile: p, Restricted: spec.Restricted, SessionID: sess.ID, Depth: spec.Depth, Channel: spec.Channel, Topic: spec.Topic,
		Emit:     func(kind string, data any) { e.Emit(kind, data) },
		Activate: activate,
		Sources:  &tools.Sources{},
	}
	res.Sources = env.Sources
	if spec.Task != nil {
		env.TaskID = spec.Task.ID
	}
	env.Ask = func(ctx context.Context, q tools.Question) (string, error) {
		if q.Kind != "confirm" && spec.Depth > 0 {
			return "", &tools.NeedsInput{Question: q.Text}
		}
		if q.Kind != "confirm" && !spec.Interactive {
			return "", errors.New("no interactive user is available for this autonomous task; continue with your best judgement and state your assumptions")
		}
		return e.ask(ctx, ar.Info, q)
	}

	// Automatic bounded recall: a small, budgeted pack of the most relevant durable facts for this turn's
	// instruction, fetched once and reused for every iteration — agents are told to call memory_find, but
	// nothing was ever actually supplied to them, so an agent that didn't think to search started cold.
	if spec.Input != "" && !spec.NoRecall {
		spec.recall = e.recall(ctx, spec, p.Name)
		if spec.recall != "" {
			e.Emit("run.recall", map[string]any{"run": ar.Info.ID, "agent": p.Name})
		}
	}

	grCfg := settings.Load(ctx, e.Settings, settings.KeyGuardrails, settings.DefaultGuardrails())
	maxIter := p.MaxIterations
	if maxIter <= 0 {
		maxIter = 24
	}
	// Autonomous work (a cron firing, a standing intent waking its owner) runs unattended: nobody is there
	// to ask for more time or to notice a "partial" result buried in the task list, so it gets real headroom
	// rather than the same budget as an interactive turn where a human can just ask again if it runs long.
	if spec.Task != nil && (spec.Task.FromKind == "cron" || spec.Task.FromKind == "intent") {
		maxIter += maxIter * grCfg.AutonomousBoostPct / 100
		if cap := grCfg.AutonomousMaxIterations; cap > 0 && maxIter > cap {
			maxIter = cap
		}
	}
	guard := &loopGuard{toolWarnAt: grCfg.ToolRepeatWarn, toolAbortAt: grCfg.ToolRepeatAbort, textAbortAt: grCfg.TextRepeatAbort}
	calib := 1.0
	compactedThisRun := 0
	ctxCfg := settings.Load(ctx, e.Settings, settings.KeyContext, settings.DefaultContext())

	for iter := 0; ; iter++ {
		if err := ctx.Err(); err != nil {
			status, errMsg = "cancelled", "cancelled"
			res.Aborted = "cancelled"
			return res, err
		}
		// steering: user messages that arrived while we were working
		for _, s := range drain(spec.Steer) {
			content, prov := s, "user"
			if spec.SteerFrom != "" { // a delegator's redirect: guidance from a colleague, not the user speaking
				content, prov = "[Message from "+spec.SteerFrom+" while you work — it refines or replaces your instruction] "+s, "agent"
			}
			_ = add(Msg{Message: llm.Message{Role: "user", Content: content}, Provenance: prov, ImageIDs: ar.takeImages()})
			// taint stays: the untrusted content is still in the context window, whatever the user says next
		}
		if iter >= maxIter {
			res.Aborted = "iteration budget exhausted"
			break
		}
		if iter == maxIter-2 && maxIter > 4 {
			_ = add(Msg{Message: llm.Message{Role: "user", Content: "[system] Two steps remain in your iteration budget. Wrap up now: give your best final answer with what you have."}, Provenance: "system"})
		}

		specs := e.toolSpecs(active)
		sysPrompt := e.buildSystem(ctx, spec, env, active)
		// ── compaction (80% → 20% of the window) ──
		est := int(float64(llm.EstimateMessages(msgsOf(history))+llm.EstimateTools(specs)+llm.EstimateTokens(sysPrompt)) * calib)
		ar.ctxTokens.Store(int64(est))
		if float64(est) > float64(window)*ctxCfg.CompactAt && len(history) > 6 {
			if nh, err := e.compact(ctx, spec, sess, history, int(float64(window)*ctxCfg.Target), false); err == nil {
				history = nh
				compactedThisRun++
				e.Emit("run.compacted", map[string]any{"run": ar.Info.ID, "agent": p.Name})
			}
		}

		resp, err := e.chat(ctx, modelRef, sysPrompt, history, specs, ar, p.Name, &calib, func() {
			// context overflow: compact hard and let the caller retry
			if nh, cerr := e.compact(ctx, spec, sess, history, int(float64(window)*ctxCfg.Target), true); cerr == nil {
				history = nh
			}
		})
		if err != nil {
			status, errMsg = "failed", err.Error()
			if errors.Is(err, context.Canceled) {
				status = "cancelled"
			}
			return res, err
		}
		res.TokensIn += resp.Usage.Prompt
		res.TokensOut += resp.Usage.Completion
		ar.tokensIn.Add(int64(resp.Usage.Prompt))
		ar.tokensOut.Add(int64(resp.Usage.Completion))
		e.Sessions.AddTokens(ctx, sess.ID, resp.Usage.Prompt, resp.Usage.Completion)
		if spec.Task != nil {
			e.Tasks.AddTokens(ctx, spec.Task.ID, resp.Usage.Prompt, resp.Usage.Completion)
		}
		e.Emit("run.usage", map[string]any{"run": ar.Info.ID, "agent": p.Name, "tokens_in": ar.tokensIn.Load(), "tokens_out": ar.tokensOut.Load(),
			"context": ar.ctxTokens.Load(), "window": window})
		res.Iterations = iter + 1

		am := Msg{Message: llm.Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls}, Provenance: "agent", Tainted: tainted}
		if err := add(am); err != nil {
			status, errMsg = "failed", err.Error()
			return res, err
		}
		if len(resp.ToolCalls) == 0 {
			if pending := drainPeek(spec.Steer); pending {
				continue // a steering message arrived during generation: take another turn
			}
			if warn := guard.noteText(resp.Content); warn != "" {
				_ = add(Msg{Message: llm.Message{Role: "user", Content: warn}, Provenance: "system"})
				if guard.abort {
					res.Aborted = "stopped by loop detection (repeated text)"
					break
				}
				continue // give it one more turn to recover before giving up on it
			}
			res.Text = strings.TrimSpace(resp.Content)
			if res.Text == "" && strings.TrimSpace(resp.Reasoning) != "" {
				res.Text = strings.TrimSpace(resp.Reasoning)
			}
			if resp.FinishReason == "length" {
				res.Text += "\n\n[…the model hit its output limit and the answer was cut off; ask it to continue, or raise “max output” for this model]"
			}
			return res, nil
		}

		// ── tool execution ──
		results := e.execTools(ctx, spec, ar, env, resp.ToolCalls, &tainted, guard, spec.Steer)
		needs := ""
		for i, tc := range resp.ToolCalls {
			r := results[i]
			if r.needsInput != "" {
				needs = r.needsInput
			}
			prov := "tool"
			if r.untrusted != "" {
				prov = r.untrusted
			}
			if err := add(Msg{Message: llm.Message{Role: "tool", Content: r.text, ToolCallID: tc.ID, Name: tc.Name}, Provenance: prov, Tainted: r.untrusted != "" || tainted}); err != nil {
				status, errMsg = "failed", err.Error()
				return res, err
			}
		}
		if guard.abort {
			res.Aborted = "stopped by loop detection"
			break
		}
		if needs != "" {
			res.NeedsInput = needs
			return res, nil
		}
	}

	// budget exhausted or aborted: ask for a final, tool-less summary of progress
	_ = add(Msg{Message: llm.Message{Role: "user", Content: "[system] You cannot use more tools. Summarize what you accomplished, what is unfinished and any partial results, concisely."}, Provenance: "system"})
	resp, err := e.chat(ctx, modelRef, e.buildSystem(ctx, spec, env, active), history, nil, ar, p.Name, &calib, nil)
	if err != nil {
		status, errMsg = "failed", err.Error()
		return res, err
	}
	res.TokensIn += resp.Usage.Prompt
	res.TokensOut += resp.Usage.Completion
	res.Text = "[partial — " + res.Aborted + "] " + strings.TrimSpace(resp.Content)
	_, _ = e.Sessions.Append(ctx, sess.ID, Msg{Message: llm.Message{Role: "assistant", Content: resp.Content}, Provenance: "agent"})
	return res, nil
}

func msgsOf(ms []Msg) []llm.Message {
	out := make([]llm.Message, len(ms))
	seen := 0
	for i := len(ms) - 1; i >= 0; i-- {
		out[i] = ms[i].Message
		if n := len(ms[i].ImageIDs); n > 0 {
			if seen++; seen <= keepImages { // placeholders (no bytes): the pictures that will be sent count toward the budget
				out[i].Images = make([]llm.Image, n)
			}
		}
	}
	return out
}

// keepImages is how many of the most recent picture-bearing messages are sent as pictures; older ones
// become a short note (a picture costs ~1k tokens every turn for as long as it stays in the history).
const keepImages = 3

// msgsForModel is msgsOf with the picture bytes loaded, for the request to the model.
func (e *Engine) msgsForModel(ctx context.Context, ms []Msg) []llm.Message {
	out := msgsOf(ms)
	seen := 0
	for i := len(ms) - 1; i >= 0; i-- {
		if len(ms[i].ImageIDs) == 0 {
			continue
		}
		seen++
		out[i].Images = nil
		if seen > keepImages || e.LoadImage == nil {
			out[i].Content = strings.TrimSpace(out[i].Content + "\n[an earlier image was attached here; it is no longer shown]")
			continue
		}
		var missing int
		for _, id := range ms[i].ImageIDs {
			mime, data, err := e.LoadImage(ctx, id)
			if err != nil || len(data) == 0 {
				missing++
				continue
			}
			out[i].Images = append(out[i].Images, llm.Image{MIME: mime, Data: data})
		}
		if missing > 0 {
			out[i].Content = strings.TrimSpace(out[i].Content + fmt.Sprintf("\n[%d attached image(s) could not be loaded]", missing))
		}
	}
	return out
}

func drain(ch <-chan string) []string {
	if ch == nil {
		return nil
	}
	var out []string
	for {
		select {
		case s := <-ch:
			out = append(out, s)
		default:
			return out
		}
	}
}

func drainPeek(ch chan string) bool { return ch != nil && len(ch) > 0 }

// chat performs one model call with the concurrency semaphore, streaming
// deltas to the UI. onOverflow is invoked once on a context-length error.
func (e *Engine) chat(ctx context.Context, modelRef, system string, history []Msg, specs []llm.ToolSpec, ar *activeRun, agent string, calib *float64, onOverflow func()) (*llm.Response, error) {
	var lastErr error
	overflowed := false
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * 1500 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		msgs := append([]llm.Message{{Role: "system", Content: system}}, e.msgsForModel(ctx, history)...)
		sanitizeToolPairs(&msgs)
		queued := time.Now()
		sem := e.sem()
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		meta := &llm.CallMeta{Agent: agent, WaitMS: int(time.Since(queued).Milliseconds()), TaskID: ar.Info.TaskID, RunID: ar.Info.ID, ParentRun: ar.Info.ParentRun}
		est := llm.EstimateMessages(msgs) + llm.EstimateTools(specs)
		resp, err := e.LLM.Chat(llm.WithMeta(ctx, meta), modelRef, llm.Request{Messages: msgs, Tools: specs}, func(d llm.Delta) {
			if d.Reasoning != "" {
				e.Emit("run.delta", map[string]any{"run": ar.Info.ID, "agent": agent, "kind": "thinking", "text": d.Reasoning})
			}
			if d.Content != "" {
				e.Emit("run.delta", map[string]any{"run": ar.Info.ID, "agent": agent, "kind": "content", "text": d.Content})
			}
		})
		<-sem
		if err == nil {
			if resp.Usage.Prompt > 0 && est > 0 {
				r := float64(resp.Usage.Prompt) / float64(est)
				if r > 0.4 && r < 2.5 {
					*calib = 0.6**calib + 0.4*r
				}
			}
			e.Emit("run.delta", map[string]any{"run": ar.Info.ID, "agent": agent, "kind": "break"})
			return resp, nil
		}
		lastErr = err
		if errors.Is(err, llm.ErrContextLength) && onOverflow != nil && !overflowed {
			overflowed = true
			onOverflow()
			attempt-- // retry without burning an attempt
			continue
		}
		if ctx.Err() != nil || errors.Is(err, llm.ErrNoModel) {
			return nil, err
		}
		var ae *llm.APIError
		if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != 429 {
			return nil, err
		}
	}
	return nil, lastErr
}

// sanitizeToolPairs guarantees every assistant tool_call has a matching tool
// message and vice versa — providers reject dangling pairs, which can appear
// after interruption or compaction.
func sanitizeToolPairs(msgs *[]llm.Message) {
	in := *msgs
	byID := map[string]llm.Message{}
	for _, m := range in {
		if m.Role == "tool" {
			byID[m.ToolCallID] = m
		}
	}
	out := make([]llm.Message, 0, len(in))
	for _, m := range in {
		switch {
		case m.Role == "tool":
			continue // re-emitted right after their assistant message; orphans are dropped
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			out = append(out, m)
			for _, tc := range m.ToolCalls {
				if r, ok := byID[tc.ID]; ok {
					out = append(out, r)
				} else {
					out = append(out, llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Name, Content: "Skipped: interrupted before this tool ran."})
				}
			}
		default:
			out = append(out, m)
		}
	}
	*msgs = out
}

// ── tools ───────────────────────────────────────────────────────────────────

var minimalBase = map[string]bool{"memory_find": true, "memory_banks": true, "memory_store": true, "clock": true, "ask_user": true, "artifact_read": true}

func (e *Engine) initialTools(ctx context.Context, p *Profile, spec RunSpec) map[string]bool {
	active := map[string]bool{}
	for _, t := range e.Tools.All() {
		if !t.Base || t.Deferred {
			continue
		}
		if p.Role == RoleEntry && !minimalBase[t.Name] { // keep the main chat lean
			continue
		}
		active[t.Name] = true
	}
	for _, n := range p.Tools {
		active[n] = true
	}
	for _, n := range spec.ExtraTools {
		active[n] = true
	}
	if p.CanDelegate && spec.Depth < MaxDepth && !spec.Restricted {
		active["delegate"], active["agent_find"] = true, true
	}
	if spec.Depth >= MaxDepth || spec.Leaf {
		delete(active, "delegate")
	}
	if spec.Leaf {
		delete(active, "ask_colleague")
	}
	for n := range active {
		t, ok := e.Tools.Get(n)
		if !ok || (spec.Restricted && t.Risk == tools.RiskExec) || !t.AllowedFor(p.Name) {
			delete(active, n)
		}
	}
	if spec.Restricted {
		delete(active, "delegate") // a restricted agent must not obtain exec rights through someone else
	}
	return active
}

func (e *Engine) toolSpecs(active map[string]bool) []llm.ToolSpec {
	var out []llm.ToolSpec
	for _, t := range e.Tools.All() { // sorted → stable prompt prefix
		if active[t.Name] && e.Tools.State(t.Name).Enabled {
			out = append(out, t.Spec())
		}
	}
	return out
}

type toolResult struct {
	text       string
	untrusted  string // provenance label when output is untrusted
	needsInput string
}

// loopGuard implements loop detection on top of the hard iteration budget. The warn/abort thresholds come
// from settings.Guardrails (zero value here means "use the settings.DefaultGuardrails() numbers" — see
// the constructor site in Run — so a loopGuard{} literal used directly in a test behaves exactly as it did
// before these became configurable).
type loopGuard struct {
	mu          sync.Mutex // concurrent-safe tool calls note themselves in parallel
	recent      []string
	abort       bool
	textRepeats int // how many times noteText has caught the model stuck repeating its own output
	toolWarnAt  int // identical tool call: warn once it's been made this many times (default 3)
	toolAbortAt int // …and hard-abort at this many (default 5)
	textAbortAt int // stuck-repeating text: hard-abort after this many occurrences (default 2)
}

func (g *loopGuard) note(sig string) (warn string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	warnAt, abortAt := g.toolWarnAt, g.toolAbortAt
	if warnAt <= 0 {
		warnAt = 3
	}
	if abortAt <= 0 {
		abortAt = 5
	}
	g.recent = append(g.recent, sig)
	if len(g.recent) > 10 {
		g.recent = g.recent[1:]
	}
	n := 0
	for _, s := range g.recent {
		if s == sig {
			n++
		}
	}
	switch {
	case n >= abortAt:
		g.abort = true
		return fmt.Sprintf("[loop-guard] identical call repeated %d times — run will be stopped.", abortAt)
	case n >= warnAt:
		return "[loop-guard] you have made this identical call " + fmt.Sprint(n) + " times with no progress. Change your approach or give your final answer."
	}
	// ping-pong: A B A B A B
	if l := len(g.recent); l >= 6 {
		r := g.recent[l-6:]
		if r[0] == r[2] && r[2] == r[4] && r[1] == r[3] && r[3] == r[5] && r[0] != r[1] {
			return "[loop-guard] you are alternating between the same two calls. Stop and try something different or finish."
		}
	}
	return ""
}

func sig(tc llm.ToolCall) string {
	h := sha1.Sum([]byte(tc.Name + "\x00" + tc.Arguments))
	return tc.Name + ":" + hex.EncodeToString(h[:6])
}

// noteText checks a would-be-final response for a degenerate output loop — the model stuck regenerating the
// same phrase or pattern over and over instead of finishing — a different failure mode from repeated tool
// calls (see note/sig above), but escalated the same way: warn and give it one more turn to recover, then
// abort if it happens again in the same run. Text repetition is checked with a lower repeat tolerance than
// tool calls (2 vs 5) because regenerating a whole response is far more expensive than retrying a call.
func (g *loopGuard) noteText(content string) (warn string) {
	pat, reps := detectRepeat(content)
	if pat == "" {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.textRepeats++
	sample := pat
	if r := []rune(sample); len(r) > 40 {
		sample = string(r[:40]) + "…"
	}
	textAbortAt := g.textAbortAt
	if textAbortAt <= 0 {
		textAbortAt = 2
	}
	if g.textRepeats >= textAbortAt {
		g.abort = true
		return fmt.Sprintf("[loop-guard] stuck repeating %q (%dx) again — run will be stopped.", sample, reps)
	}
	return fmt.Sprintf("[loop-guard] your response got stuck repeating %q (%dx). Stop repeating yourself and give a single, concise final answer.", sample, reps)
}

// detectRepeat looks for a short pattern repeated at least 3 times contiguously at the end of s, covering a
// meaningful span of text — the signature of a model stuck in a degenerate generation loop ("the cat sat on
// the mat. the cat sat on the mat. …", or a single character/token run). Only the tail is scanned (repetition
// earlier in a long response, followed by real content, is not this failure mode) and pure-whitespace
// patterns are ignored (legitimate formatting, not a stuck model). Returns ("", 0) when nothing qualifies.
func detectRepeat(s string) (pattern string, count int) {
	const maxTail = 2000
	const minSpan = 60 // total repeated chars required before this counts as "stuck", not "coincidence"
	t := s
	if len(t) > maxTail {
		t = t[len(t)-maxTail:]
	}
	n := len(t)
	if n < minSpan {
		return "", 0
	}
	for p := 1; p <= n/3; p++ {
		pat := t[n-p:]
		if strings.TrimSpace(pat) == "" {
			continue
		}
		reps := 1
		for (reps+1)*p <= n && t[n-(reps+1)*p:n-reps*p] == pat {
			reps++
		}
		if reps >= 3 && reps*p >= minSpan {
			return pat, reps
		}
	}
	return "", 0
}

func (e *Engine) execTools(ctx context.Context, spec RunSpec, ar *activeRun, env *tools.Env, calls []llm.ToolCall, tainted *bool, guard *loopGuard, steer chan string) []toolResult {
	results := make([]toolResult, len(calls))
	skip := "Skipped: the user sent a new message while you were working. Re-read it and decide how to proceed."
	if spec.SteerFrom != "" {
		skip = "Skipped: " + spec.SteerFrom + " sent you a new message while you were working. Re-read it and decide how to proceed."
	}
	i := 0
	for i < len(calls) {
		if drainPeek(steer) { // steering: synthesize skipped results so tool_use/tool_result pairing stays valid
			for j := i; j < len(calls); j++ {
				results[j] = toolResult{text: skip}
				e.Emit("run.tool", map[string]any{"run": ar.Info.ID, "agent": env.Agent, "tool": calls[j].Name, "phase": "skipped"})
			}
			return results
		}
		tool, ok := e.Tools.Get(calls[i].Name)
		if ok && tool.ConcurrentSafe() && e.Tools.State(tool.Name).Enabled {
			// fan out the contiguous run of concurrent-safe calls
			j := i
			for j < len(calls) {
				t2, ok2 := e.Tools.Get(calls[j].Name)
				if !ok2 || !t2.ConcurrentSafe() || !e.Tools.State(t2.Name).Enabled {
					break
				}
				j++
			}
			var wg sync.WaitGroup
			for k := i; k < j; k++ {
				wg.Add(1)
				go func(k int) {
					defer wg.Done()
					results[k] = e.execOne(ctx, ar, env, calls[k], *tainted, guard, steer, spec)
				}(k)
			}
			wg.Wait()
			i = j
		} else {
			results[i] = e.execOne(ctx, ar, env, calls[i], *tainted, guard, steer, spec)
			i++
		}
		for k := 0; k < i; k++ {
			if results[k].untrusted != "" {
				*tainted = true
				env.Tainted = true
			}
		}
		if guard.abort {
			for j := i; j < len(calls); j++ {
				results[j] = toolResult{text: "Skipped: run stopped by loop detection."}
			}
			return results
		}
	}
	return results
}

func (e *Engine) execOne(ctx context.Context, ar *activeRun, env *tools.Env, tc llm.ToolCall, tainted bool, guard *loopGuard, steer chan string, spec RunSpec) (res toolResult) {
	start := time.Now()
	emit := func(phase string, ok bool, why ...string) {
		ev := map[string]any{"run": ar.Info.ID, "agent": env.Agent, "tool": tc.Name, "args": brief(tc.Arguments, 140),
			"phase": phase, "ok": ok, "ms": time.Since(start).Milliseconds()}
		if len(why) > 0 && why[0] != "" {
			ev["error"] = brief(why[0], 200)
		}
		e.Emit("run.tool", ev)
	}
	tool, ok := e.Tools.Get(tc.Name)
	if !ok {
		emit("end", false)
		return toolResult{text: fmt.Sprintf("Error: unknown tool %q. Use tool_search to find available tools.", tc.Name)}
	}
	if !tool.AllowedFor(env.Agent) { // defence in depth: however the tool got activated
		emit("denied", false)
		return toolResult{text: fmt.Sprintf("Error: %s is reserved for %s. Delegate the request to %s (delegate) instead of doing it yourself.", tc.Name, strings.Join(tool.Only, "/"), strings.Join(tool.Only, "/"))}
	}
	if env.Restricted && tool.Risk == tools.RiskExec { // defence in depth: however the tool got activated
		emit("denied", false)
		return toolResult{text: fmt.Sprintf("Error: %s is not available to you yet: you are on probation (hired by another agent) and the user has not confirmed the hire. Ask a colleague to help instead, or tell the requester.", tc.Name)}
	}
	if e.Tools.IgnoreTaint() { // master arm + "don't ask after web content": untrusted content no longer gates tools
		tainted = false
	}
	st := e.Tools.State(tc.Name)
	if !st.Enabled {
		emit("denied", false)
		return toolResult{text: fmt.Sprintf("Error: tool %q is disabled by the user.", tc.Name)}
	}
	warn := guard.note(sig(tc))
	if tool.Risk != tools.RiskRead {
		needConfirm := !st.Armed || (tainted && !tool.Auto)
		// "allow for this task" was given before untrusted content showed up: it does not carry over once tainted
		if _, ok := ar.approved.Load(tc.Name); ok && !tainted {
			needConfirm = false
		}
		if needConfirm {
			why := "tool is in SAFE mode"
			if tainted && st.Armed {
				why = "untrusted content (web/mcp/file) is in this turn's context"
			}
			ans, err := e.ask(ctx, ar.Info, tools.Question{Kind: "confirm", Tool: tc.Name, Args: brief(tc.Arguments, 600),
				Text: fmt.Sprintf("%s wants to run %s [%s] — %s.", env.Agent, tc.Name, tool.Risk, why), Options: []string{"allow", "allow for this task", "deny"}})
			if strings.EqualFold(strings.TrimSpace(ans), "allow for this task") {
				ar.approved.Store(tc.Name, true)
			}
			if err != nil || !isYes(ans) {
				emit("denied", false)
				return toolResult{text: "Error: the user did not approve running " + tc.Name + ". Do not retry it; find another way or explain."}
			}
		}
	}
	emit("start", true)
	tenv := *env
	tenv.Tainted = tainted
	tenv.RunID = ar.Info.ID
	marked := false
	tenv.Taint = func() { marked = true }
	runCtx := ctx
	if steer != nil && (tool.Name == "delegate" || tool.Name == "ask_colleague") {
		// specialists may outlive this turn when the user interrupts the wait; Stop cancels them explicitly
		runCtx = context.WithoutCancel(ctx)
	}
	var cancel context.CancelFunc
	if tool.Name != "delegate" && tool.Name != "ask_user" {
		limit := tool.Timeout
		if limit <= 0 {
			limit = defaultToolTimeout * 3
		}
		runCtx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	var out string
	var err error
	type toolReturn struct {
		out string
		err error
	}
	done := make(chan toolReturn, 1)
	go func() {
		var r toolReturn
		defer func() {
			if rec := recover(); rec != nil {
				r.err = fmt.Errorf("tool panicked: %v", rec)
			}
			done <- r
		}()
		r.out, r.err = tool.Run(runCtx, &tenv, []byte(tc.Arguments))
	}()
	// A user message that arrives while a tool is running (a specialist working for minutes, a long command) must
	// not wait for it: stop waiting, hand control back to the model, and let it read the message. Delegated work
	// keeps running in the background; other tools are cancelled.
	if steer == nil || tool.Name == "ask_user" { // a pending question is answered through its own channel, not by chatting
		r := <-done
		out, err = r.out, r.err
	} else {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
	wait:
		for {
			select {
			case r := <-done:
				out, err = r.out, r.err
				break wait
			case <-tick.C:
				if !drainPeek(steer) {
					continue
				}
				emit("interrupted", true)
				detached := tool.Name == "delegate" || tool.Name == "ask_colleague"
				go func() { // whatever the tool returns later is shown to the user, not fed back into the run
					r := <-done
					if detached {
						txt := strings.TrimSpace(r.out)
						if r.err != nil {
							txt = "failed: " + r.err.Error()
						}
						e.logChat(context.WithoutCancel(ctx), "system", "", fmt.Sprintf("%s finished in the background: %s", tc.Name, brief(txt, 600)), spec.Channel, spec.Topic, env.TaskID)
					}
				}()
				return toolResult{text: e.interruptedText(ctx, tool.Name, env.TaskID, detached, spec.SteerFrom)}
			}
		}
	}
	if e.OnTool != nil {
		var ni0 *tools.NeedsInput
		if !errors.As(err, &ni0) { // a question relayed to the user is not a tool failure
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			e.OnTool(env.Agent, tc.Name, time.Since(start).Milliseconds(), err == nil, msg, env.TaskID, ar.Info.ID, ar.Info.ParentRun)
		}
	}
	var ni *tools.NeedsInput
	switch {
	case errors.As(err, &ni):
		emit("end", true)
		return toolResult{text: "Your question was relayed to the requester: " + ni.Question, needsInput: ni.Question}
	case err != nil:
		out = "Error: " + err.Error()
		emit("end", false, err.Error())
	default:
		emit("end", true)
	}
	if n := len([]rune(out)); n > maxToolResultChars {
		r := []rune(out)
		out = string(r[:maxToolResultChars]) + fmt.Sprintf("\n…[truncated %d chars — narrow the request or page through it]", n-maxToolResultChars)
	}
	if warn != "" {
		out += "\n" + warn
	}
	res = toolResult{text: out}
	if (tool.Untrusted || marked) && err == nil { // marked: a delegate's answer that rests on web content
		env.Sources.Note(tc.Arguments, out) // which sites this turn has really seen
		env.Sources.NoteResult(out)         // and which exact addresses
	}
	if marked {
		res.untrusted = "agent"
	}
	if tool.Untrusted && err == nil {
		res.untrusted = "web"
		if strings.HasPrefix(tool.Source, "mcp:") {
			res.untrusted = "mcp"
		}
	}
	return res
}

func isYes(a string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	return a == "allow" || a == "allow for this task" || a == "yes" || a == "y" || a == "ok" || a == "approve" || a == "true"
}

func llmMsg(role, content string) llm.Message { return llm.Message{Role: role, Content: content} }

// interruptedText is what the model is told when a user message cut a tool call short.
func (e *Engine) interruptedText(ctx context.Context, tool string, taskID int64, detached bool, from string) string {
	who := "the user"
	if from != "" {
		who = from
	}
	txt := "Interrupted: " + who + " sent a new message while this was running. Read the message and decide what to do."
	if !detached {
		return txt + " The call was cancelled; repeat it later only if it is still needed."
	}
	txt += " The delegated work itself keeps running in the background and its result will be shown to the user when it finishes"
	if taskID != 0 {
		rows, err := e.DB.Query(ctx, `SELECT id,to_agent,title,status FROM tasks WHERE parent_id=$1 AND status IN ('queued','running') ORDER BY id`, taskID)
		if err == nil {
			defer rows.Close()
			var lines []string
			for rows.Next() {
				var id int64
				var agent, title, status string
				if rows.Scan(&id, &agent, &title, &status) == nil {
					lines = append(lines, fmt.Sprintf("#%d %s (%s): %s", id, agent, status, brief(title, 80)))
				}
			}
			if len(lines) > 0 {
				txt += ": " + strings.Join(lines, "; ") + ". Decide for each: if the new message changes or contradicts what it is doing, redirect it with task_steer(id, message) or stop it with task_cancel(id); if it is unrelated, leave it running; check on one with task_status(id) when relevant"
			}
		}
	}
	return txt + ". Tell the user it is still running if they ask."
}
