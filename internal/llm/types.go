// Package llm talks to OpenAI-compatible endpoints (OpenAI, LM Studio, Gemini,
// Grok, OpenRouter, custom). Streaming, tool calls and embeddings are supported;
// a Router adds multi-key rotation and model-list fallback.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	// Images are attached to a user message; they are sent to the model as image parts alongside Content.
	Images []Image `json:"-"`
}

// Image is picture data for a message. The bytes are loaded just before the request (they live on disk as artifacts).
type Image struct {
	MIME string
	Data []byte
}

// ImageTokens is the rough cost of one image in the context window (vision encoders use ~500–1500 tokens
// for a downscaled photo); used for budgeting only.
const ImageTokens = 900

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Request struct {
	Messages    []Message
	Tools       []ToolSpec
	Temperature *float64
	MaxTokens   int
	JSON        bool // ask for a JSON object response
}

type Usage struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
}

type Delta struct {
	Content   string
	Reasoning string
}

type Response struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	Usage        Usage
	FinishReason string
	Model        string // model name used (after fallback)
}

// Endpoint is one concrete (provider, key, model) target.
type Endpoint struct {
	BaseURL string
	APIKey  string
	Model   string
	Kind    string
	Tools   bool // model supports tool calls
	Temp    *float64
	MaxOut  int
}

type APIError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 400 {
		b = b[:400] + "…"
	}
	return fmt.Sprintf("llm: HTTP %d: %s", e.Status, b)
}

var ErrContextLength = errors.New("llm: context length exceeded")
var ErrNoModel = errors.New("llm: no model configured")

// EstimateTokens is a cheap heuristic (no tokenizer dependency): ~4 bytes/token
// for ASCII, ~1.6 chars/token for other scripts.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	ascii, other := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	_ = utf8.RuneError
	return ascii/4 + int(float64(other)/1.6) + 1
}

// EstimateMessages sums estimated tokens for a message list.
func EstimateMessages(ms []Message) int {
	n := 0
	for _, m := range ms {
		n += 4 + EstimateTokens(m.Content) + len(m.Images)*ImageTokens
		for _, tc := range m.ToolCalls {
			n += EstimateTokens(tc.Name) + EstimateTokens(tc.Arguments) + 4
		}
	}
	return n
}

func EstimateTools(ts []ToolSpec) int {
	n := 0
	for _, t := range ts {
		n += EstimateTokens(t.Name) + EstimateTokens(t.Description) + EstimateTokens(string(t.Parameters)) + 6
	}
	return n
}

// Call is one finished model request, for the usage dashboard.
type Call struct {
	TS      time.Time
	Model   string
	Agent   string // who asked ("" = background work such as memory digestion)
	In, Out int
	MS      int // request start to last token
	WaitMS  int // time spent queued for a model slot before the request
	OK      bool
	Err     string
	// TaskID/RunID/ParentRun attribute this call to the run that made it and, when it is itself a delegated
	// run, the run that delegated to it (0/0/0 = not tied to a run, e.g. background memory work). Mirrors
	// Engine.RunInfo exactly, so a query can walk parent_run back to a root run without a separate "runs"
	// table — see internal/metrics.DelegationReport.
	TaskID, RunID, ParentRun int64
}

// CallMeta travels in the context so the router can attribute a call to an agent.
type CallMeta struct {
	Agent                    string
	WaitMS                   int
	TaskID, RunID, ParentRun int64
}

type callMetaKey struct{}

// WithMeta attaches attribution for the router's call log.
func WithMeta(ctx context.Context, m *CallMeta) context.Context {
	return context.WithValue(ctx, callMetaKey{}, m)
}

func metaFrom(ctx context.Context) *CallMeta {
	m, _ := ctx.Value(callMetaKey{}).(*CallMeta)
	return m
}
