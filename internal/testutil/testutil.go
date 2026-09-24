// Package testutil provides a throwaway Postgres database and a scriptable fake
// OpenAI-compatible server for integration tests.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"prism/internal/db"
	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/textmatch"
)

// DB creates a fresh database on the test server (PRISM_TEST_DSN, default the
// local scratch instance), migrates it, and drops it on cleanup.
func DB(t testing.TB) *db.DB {
	t.Helper()
	base := os.Getenv("PRISM_TEST_DSN")
	if base == "" {
		base = "postgres://postgres@127.0.0.1:5433/postgres?sslmode=disable"
	}
	var rb [4]byte
	_, _ = rand.Read(rb[:])
	name := "prism_test_" + hex.EncodeToString(rb[:])
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Skipf("test postgres unavailable: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	cfg, _ := pgx.ParseConfig(base)
	cfg.Database = name
	dsn := fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Host, cfg.Port, name)
	if cfg.Password != "" {
		dsn = fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port, name)
	}
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	return d
}

// FakeLLM is an OpenAI-compatible server. Chat replies come from Script (one
// entry per call, last one repeats); embeddings are deterministic hashed bags of words.
type FakeLLM struct {
	*httptest.Server
	mu     sync.Mutex
	Script []Reply
	Calls  []llm.Request
	n      int
	// Handler, if set, decides the reply from the request instead of Script.
	Handler func(req map[string]any, call int) Reply
}

type Reply struct {
	Content   string
	Reasoning string
	Tools     []llm.ToolCall
	Status    int // non-zero: fail with this HTTP status
	DelayMS   int
}

const EmbedDim = 96

func Embed(text string) []float32 {
	v := make([]float32, EmbedDim)
	for _, tok := range textmatch.Tokens(text) {
		h := fnv.New32a()
		h.Write([]byte(tok))
		v[h.Sum32()%EmbedDim] += 1
	}
	var n float64
	for _, x := range v {
		n += float64(x * x)
	}
	if n > 0 {
		inv := float32(1 / math.Sqrt(n))
		for i := range v {
			v[i] *= inv
		}
	}
	return v
}

// Recorded returns a copy of the requests received so far.
func (f *FakeLLM) Recorded() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Request(nil), f.Calls...)
}

func NewFakeLLM(t testing.TB, script ...Reply) *FakeLLM {
	f := &FakeLLM{Script: script}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		for i, s := range req.Input {
			data = append(data, item{i, Embed(s)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		call := f.n
		f.n++
		var reply Reply
		if f.Handler != nil {
			reply = f.Handler(req, call)
		} else if len(f.Script) > 0 {
			i := call
			if i >= len(f.Script) {
				i = len(f.Script) - 1
			}
			reply = f.Script[i]
		}
		rec := llm.Request{}
		if ms, ok := req["messages"].([]any); ok {
			for _, m := range ms {
				mm := m.(map[string]any)
				c, _ := mm["content"].(string)
				role, _ := mm["role"].(string)
				rec.Messages = append(rec.Messages, llm.Message{Role: role, Content: c})
			}
		}
		if ts, ok := req["tools"].([]any); ok {
			for _, x := range ts {
				fn := x.(map[string]any)["function"].(map[string]any)
				rec.Tools = append(rec.Tools, llm.ToolSpec{Name: fn["name"].(string)})
			}
		}
		f.Calls = append(f.Calls, rec)
		f.mu.Unlock()
		if reply.DelayMS > 0 {
			time.Sleep(time.Duration(reply.DelayMS) * time.Millisecond)
		}
		if reply.Status != 0 {
			w.WriteHeader(reply.Status)
			fmt.Fprint(w, `{"error":{"message":"fake failure"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		if reply.Reasoning != "" {
			send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": reply.Reasoning}}}})
		}
		for _, w := range strings.SplitAfter(reply.Content, " ") {
			if w != "" {
				send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": w}}}})
			}
		}
		for i, tc := range reply.Tools {
			send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": i, "id": tc.ID, "function": map[string]any{"name": tc.Name, "arguments": tc.Arguments}}}}}}})
		}
		fin := "stop"
		if len(reply.Tools) > 0 {
			fin = "tool_calls"
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": fin}},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// Setup wires a fake server into a fresh router: provider, one chat model
// ("chat"), one embedding model ("embed"), and role defaults.
func Setup(t testing.TB, d *db.DB, f *FakeLLM) (*llm.Router, *settings.Store) {
	t.Helper()
	ctx := context.Background()
	st := settings.New(d.Pool)
	r := llm.NewRouter(d.Pool, st)
	pid, err := r.Store().SaveProvider(ctx, llm.Provider{Name: "fake", Kind: "custom", BaseURL: f.URL + "/v1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store().SaveModel(ctx, llm.Model{Name: "chat", ProviderID: pid, ModelID: "fake-chat", Kind: "chat", ContextWindow: 8000, SupportsTools: true, Vision: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store().SaveModel(ctx, llm.Model{Name: "embed", ProviderID: pid, ModelID: "fake-embed", Kind: "embedding", ContextWindow: 512}); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, settings.KeyModelRoles, settings.ModelRoles{Chat: "chat", Fast: "chat", Embedding: "embed"}); err != nil {
		t.Fatal(err)
	}
	return r, st
}
