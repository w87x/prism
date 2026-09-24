package llm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/settings"
)

// Router resolves a model reference (model name, list name, or "role:<x>") into a
// chain of endpoints and executes requests with key rotation and fallback.
type Router struct {
	db       *pgxpool.Pool
	store    *Store
	settings *settings.Store
	client   *Client

	mu      sync.RWMutex
	snap    *snapshot
	cool    map[int64]time.Time // key id → cooldown end
	learned map[string]int      // model name → context window learned from overflow errors
	usage   func(model string, u Usage)
	onCall  func(Call)

	active atomic.Int32                   // completions in flight (any caller: agents, memory, knowledge base…)
	busy   func(active int, model string) // notified when the count changes
}

type snapshot struct {
	providers map[int64]Provider
	models    map[string]Model
	lists     map[string]List
	order     []string // model names in id order, for defaults
}

func NewRouter(db *pgxpool.Pool, st *settings.Store) *Router {
	r := &Router{db: db, settings: st, client: NewClient(), cool: map[int64]time.Time{}, learned: map[string]int{}}
	r.store = NewStore(db, r.Invalidate)
	return r
}

func (r *Router) Store() *Store   { return r.store }
func (r *Router) Client() *Client { return r.client }

// OnUsage registers a callback invoked after each successful chat completion.
func (r *Router) OnUsage(fn func(model string, u Usage)) { r.usage = fn }

// OnCall registers a callback invoked after every finished chat completion (success or failure), except
// those the caller cancelled.
func (r *Router) OnCall(fn func(Call)) { r.onCall = fn }

// OnBusy registers a callback fired whenever a chat completion starts or ends, with the number in flight.
func (r *Router) OnBusy(fn func(active int, model string)) { r.busy = fn }

// Active reports how many chat completions are in flight.
func (r *Router) Active() int { return int(r.active.Load()) }

func (r *Router) Invalidate() {
	r.mu.Lock()
	r.snap = nil
	r.mu.Unlock()
}

func (r *Router) load(ctx context.Context) (*snapshot, error) {
	r.mu.RLock()
	s := r.snap
	r.mu.RUnlock()
	if s != nil {
		return s, nil
	}
	provs, err := r.store.Providers(ctx)
	if err != nil {
		return nil, err
	}
	models, err := r.store.Models(ctx)
	if err != nil {
		return nil, err
	}
	lists, err := r.store.Lists(ctx)
	if err != nil {
		return nil, err
	}
	s = &snapshot{providers: map[int64]Provider{}, models: map[string]Model{}, lists: map[string]List{}}
	for _, p := range provs {
		s.providers[p.ID] = p
	}
	for _, m := range models {
		s.models[m.Name] = m
		s.order = append(s.order, m.Name)
	}
	for _, l := range lists {
		s.lists[l.Name] = l
	}
	r.mu.Lock()
	r.snap = s
	r.mu.Unlock()
	return s, nil
}

// RoleRef returns the configured model reference for a role (chat, fast, embedding).
func (r *Router) RoleRef(ctx context.Context, role string) string {
	roles := settings.Load(ctx, r.settings, settings.KeyModelRoles, settings.ModelRoles{})
	var ref string
	switch role {
	case "chat":
		ref = roles.Chat
	case "fast":
		ref = roles.Fast
		if ref == "" {
			ref = roles.Chat
		}
	case "embedding":
		ref = roles.Embedding
	}
	if ref != "" {
		return ref
	}
	// fall back to the first model of the right kind
	snap, err := r.load(ctx)
	if err != nil {
		return ""
	}
	want := "chat"
	if role == "embedding" {
		want = "embedding"
	}
	for _, n := range snap.order {
		if snap.models[n].Kind == want {
			return n
		}
	}
	return ""
}

type target struct {
	model Model
	prov  Provider
}

func (r *Router) resolve(ctx context.Context, ref, want string) ([]target, error) {
	snap, err := r.load(ctx)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(ref, "role:") {
		ref = r.RoleRef(ctx, strings.TrimPrefix(ref, "role:"))
	}
	if ref == "" {
		ref = r.RoleRef(ctx, map[string]string{"chat": "chat", "embedding": "embedding"}[want])
	}
	if ref == "" {
		return nil, ErrNoModel
	}
	var names []string
	if l, ok := snap.lists[ref]; ok {
		names = l.Models
	} else {
		names = []string{ref}
	}
	var out []target
	for _, n := range names {
		m, ok := snap.models[n]
		if !ok || m.Kind != want {
			continue
		}
		p, ok := snap.providers[m.ProviderID]
		if !ok || !p.Enabled {
			continue
		}
		out = append(out, target{m, p})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %q resolves to no enabled %s model", ErrNoModel, ref, want)
	}
	return out, nil
}

// Window returns the context window (tokens) of the first model in ref's chain.
func (r *Router) Window(ctx context.Context, ref string) int {
	ts, err := r.resolve(ctx, ref, "chat")
	if err != nil || len(ts) == 0 {
		return 32768
	}
	w := ts[0].model.ContextWindow
	r.mu.RLock()
	defer r.mu.RUnlock()
	if l := r.learned[ts[0].model.Name]; l > 0 && l < w {
		return l
	}
	return w
}

var ctxLimitRe = regexp.MustCompile(`(?i)(?:context (?:length|size|window)|n_ctx|max(?:imum)? context)[^0-9]{0,50}(\d{3,8})`)

// learnWindow extracts the real limit from an overflow error ("…n_ctx 4096…") so that
// compaction thresholds follow what the server actually loaded, not the model's nominal maximum.
func (r *Router) learnWindow(model string, err error) {
	m := ctxLimitRe.FindStringSubmatch(err.Error())
	if m == nil {
		return
	}
	n, _ := strconv.Atoi(m[1])
	if n < 512 {
		return
	}
	r.mu.Lock()
	if cur := r.learned[model]; cur == 0 || n < cur {
		r.learned[model] = n
	}
	r.mu.Unlock()
}

// SupportsTools reports whether the first model in ref's chain supports tools.
func (r *Router) SupportsTools(ctx context.Context, ref string) bool {
	ts, err := r.resolve(ctx, ref, "chat")
	return err == nil && len(ts) > 0 && ts[0].model.SupportsTools
}

func (r *Router) keysFor(p Provider) []Key {
	now := time.Now()
	r.mu.RLock()
	defer r.mu.RUnlock()
	var ks []Key
	for _, k := range p.Keys {
		if !k.Enabled {
			continue
		}
		if until, ok := r.cool[k.ID]; ok && until.After(now) {
			continue
		}
		ks = append(ks, k)
	}
	sort.SliceStable(ks, func(i, j int) bool { return ks[i].Priority < ks[j].Priority })
	return ks
}

func (r *Router) penalize(k Key, d time.Duration, msg string) {
	r.mu.Lock()
	r.cool[k.ID] = time.Now().Add(d)
	r.mu.Unlock()
	if k.ID != 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = r.db.Exec(ctx, `UPDATE provider_keys SET last_error=$2, cooldown_until=$3 WHERE id=$1`, k.ID, msg, time.Now().Add(d))
		}()
	}
}

func (r *Router) markUse(k Key) {
	if k.ID == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = r.db.Exec(ctx, `UPDATE provider_keys SET uses=uses+1, last_error='' WHERE id=$1`, k.ID)
	}()
}

func endpoint(t target, k Key) Endpoint {
	return Endpoint{BaseURL: t.prov.BaseURL, APIKey: k.APIKey, Model: t.model.ModelID, Kind: t.prov.Kind,
		Tools: t.model.SupportsTools, Temp: t.model.Temperature, MaxOut: t.model.MaxOutput}
}

// attempt tries each usable key of the target; it returns done=true when the
// caller should stop (success or non-recoverable), and err for the last failure.
func (r *Router) each(ctx context.Context, ref, want string, fn func(ep Endpoint, t target) error) error {
	ts, err := r.resolve(ctx, ref, want)
	if err != nil {
		return err
	}
	var last error
	for _, t := range ts {
		keys := r.keysFor(t.prov)
		if len(keys) == 0 {
			if len(t.prov.Keys) > 0 {
				last = fmt.Errorf("provider %s: all keys are disabled or cooling down", t.prov.Name)
				continue
			}
			keys = []Key{{}} // keyless (LM Studio / local)
		}
		for _, k := range keys {
			err := fn(endpoint(t, k), t)
			if err == nil {
				r.markUse(k)
				return nil
			}
			last = fmt.Errorf("%s/%s: %w", t.prov.Name, t.model.Name, err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var ae *APIError
			switch {
			case errors.Is(err, ErrContextLength):
				r.learnWindow(t.model.Name, err)
				return err // the caller must shrink the prompt; another key won't help
			case errors.Is(err, ErrTruncated):
				// not the key's fault: no cooldown, just try the next key/model
			case errors.As(err, &ae):
				switch {
				case ae.Status == 401 || ae.Status == 403:
					r.penalize(k, time.Hour, ae.Error())
				case ae.Status == 429:
					d := ae.RetryAfter
					if d == 0 {
						d = 60 * time.Second
					}
					r.penalize(k, d, ae.Error())
				case ae.Status >= 500:
					r.penalize(k, 15*time.Second, ae.Error())
				default:
					goto nextModel // 4xx: bad request for this model, try the next one
				}
			default:
				r.penalize(k, 10*time.Second, err.Error())
			}
		}
	nextModel:
	}
	if last == nil {
		last = ErrNoModel
	}
	return last
}

// Chat runs a completion for ref with key rotation and model fallback. Fallback
// only happens while nothing has been streamed to the caller yet.
func (r *Router) Chat(ctx context.Context, ref string, req Request, onDelta func(Delta)) (final *Response, callErr error) {
	var resp *Response
	model := ref
	if r.busy != nil {
		r.busy(int(r.active.Add(1)), model)
		defer func() { r.busy(int(r.active.Add(-1)), model) }()
	} else {
		r.active.Add(1)
		defer r.active.Add(-1)
	}
	started := time.Now()
	defer func() {
		if r.onCall == nil || ctx.Err() != nil {
			return // nobody is counting, or the caller gave up (a stop button is not a failure)
		}
		c := Call{TS: started, Model: model, MS: int(time.Since(started).Milliseconds()), OK: final != nil && callErr == nil}
		if final != nil {
			c.Model, c.In, c.Out = final.Model, final.Usage.Prompt, final.Usage.Completion
		}
		if callErr != nil {
			c.Err = callErr.Error()
			if len(c.Err) > 300 {
				c.Err = c.Err[:300]
			}
		}
		if m := metaFrom(ctx); m != nil {
			c.Agent, c.WaitMS, c.TaskID, c.RunID, c.ParentRun = m.Agent, m.WaitMS, m.TaskID, m.RunID, m.ParentRun
		}
		r.onCall(c)
	}()
	err := r.each(ctx, ref, "chat", func(ep Endpoint, t target) error {
		streamed := false
		wrapped := onDelta
		if onDelta != nil {
			wrapped = func(d Delta) { streamed = true; onDelta(d) }
		}
		rq := req
		if !t.model.Vision { // a text-only model would reject image parts: tell it instead of failing the turn
			rq.Messages = stripImages(req.Messages)
		}
		res, err := r.client.Chat(ctx, ep, rq, wrapped)
		if err != nil {
			if streamed { // cannot safely retry; surface as terminal
				return &terminalErr{err}
			}
			return err
		}
		res.Model = t.model.Name
		resp = res
		return nil
	})
	var te *terminalErr
	if errors.As(err, &te) {
		return nil, te.err
	}
	if err != nil {
		return nil, err
	}
	if r.usage != nil {
		r.usage(resp.Model, resp.Usage)
	}
	return resp, nil
}

// stripImages removes image attachments (leaving a note in their place) for models that cannot see.
func stripImages(ms []Message) []Message {
	has := false
	for _, m := range ms {
		if len(m.Images) > 0 {
			has = true
		}
	}
	if !has {
		return ms
	}
	out := make([]Message, len(ms))
	for i, m := range ms {
		if n := len(m.Images); n > 0 {
			m.Images = nil
			note := fmt.Sprintf("[%d image(s) attached by the user were left out: this model cannot see images — tell the user, and if it matters suggest switching the chat model to a vision-capable one in Settings → Models]", n)
			if m.Content != "" {
				m.Content += "\n\n"
			}
			m.Content += note
		}
		out[i] = m
	}
	return out
}

type terminalErr struct{ err error }

func (e *terminalErr) Error() string { return e.err.Error() }

// Complete is a convenience for a single-shot text completion (no tools).
func (r *Router) Complete(ctx context.Context, ref, system, user string, jsonOut bool) (string, error) {
	msgs := []Message{}
	if system != "" {
		msgs = append(msgs, Message{Role: "system", Content: system})
	}
	msgs = append(msgs, Message{Role: "user", Content: user})
	res, err := r.Chat(ctx, ref, Request{Messages: msgs, JSON: jsonOut}, nil)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// Embed embeds inputs with the embedding model ref (empty → role default).
func (r *Router) Embed(ctx context.Context, ref string, inputs []string) ([][]float32, error) {
	if ref == "" {
		ref = r.RoleRef(ctx, "embedding")
	}
	var out [][]float32
	err := r.each(ctx, ref, "embedding", func(ep Endpoint, t target) error {
		v, err := r.client.Embed(ctx, ep, inputs)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// HasEmbedding reports whether an embedding model is configured.
func (r *Router) HasEmbedding(ctx context.Context) bool {
	ref := r.RoleRef(ctx, "embedding")
	if ref == "" {
		return false
	}
	_, err := r.resolve(ctx, ref, "embedding")
	return err == nil
}

// HasChat reports whether any chat model is configured.
func (r *Router) HasChat(ctx context.Context) bool {
	_, err := r.resolve(ctx, "", "chat")
	return err == nil
}
