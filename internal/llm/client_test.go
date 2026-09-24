package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		w.(http.Flusher).Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestChatStreamThinkAndTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"content":"<thi"}}]}`,
			`{"choices":[{"delta":{"content":"nk>plan it</think>Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"clock","arguments":"{\"tz\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"UTC\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
	}))
	defer srv.Close()
	var reasoning, content strings.Builder
	res, err := NewClient().Chat(context.Background(), Endpoint{BaseURL: srv.URL, Model: "m", Tools: true},
		Request{Messages: []Message{{Role: "user", Content: "hi"}}, Tools: []ToolSpec{{Name: "clock"}}},
		func(d Delta) { reasoning.WriteString(d.Reasoning); content.WriteString(d.Content) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "Hello" || reasoning.String() != "plan it" || content.String() != "Hello" {
		t.Fatalf("content=%q reasoning=%q", res.Content, reasoning.String())
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "clock" || res.ToolCalls[0].Arguments != `{"tz":"UTC"}` {
		t.Fatalf("tool calls: %+v", res.ToolCalls)
	}
	if res.Usage.Prompt != 11 || res.Usage.Completion != 7 {
		t.Fatalf("usage %+v", res.Usage)
	}
}

func TestThinkSplitterPartialTags(t *testing.T) {
	ts := &thinkSplitter{}
	var c, r strings.Builder
	for _, ch := range []string{"a<", "th", "ink>x<", "/think", ">b"} {
		cc, rr := ts.feed(ch)
		c.WriteString(cc)
		r.WriteString(rr)
	}
	cc, rr := ts.flush()
	c.WriteString(cc)
	r.WriteString(rr)
	if c.String() != "ab" || r.String() != "x" {
		t.Fatalf("c=%q r=%q", c.String(), r.String())
	}
}

func TestContextLengthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 8192 tokens"}}`)
	}))
	defer srv.Close()
	_, err := NewClient().Chat(context.Background(), Endpoint{BaseURL: srv.URL, Model: "m"}, Request{}, nil)
	if err == nil || !strings.Contains(err.Error(), ErrContextLength.Error()) {
		t.Fatalf("want context error, got %v", err)
	}
}

func TestJSONModeAdaptsToServer(t *testing.T) {
	var sawFormat []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, has := body["response_format"]
		sawFormat = append(sawFormat, has)
		if has { // a picky server
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"'response_format.type' must be 'json_schema' or 'text'"}`)
			return
		}
		sse(w, `{"choices":[{"delta":{"content":"{\"ok\":true}"}}]}`)
	}))
	defer srv.Close()
	// generic provider: tries json_object, retries without on rejection
	res, err := NewClient().Chat(context.Background(), Endpoint{BaseURL: srv.URL, Model: "m", Kind: "custom"}, Request{JSON: true}, nil)
	if err != nil || res.Content != `{"ok":true}` || len(sawFormat) != 2 || !sawFormat[0] || sawFormat[1] {
		t.Fatalf("retry path: %v %+v %v", err, res, sawFormat)
	}
	// LM Studio: never sent
	sawFormat = nil
	if _, err := NewClient().Chat(context.Background(), Endpoint{BaseURL: srv.URL, Model: "m", Kind: "lmstudio"}, Request{JSON: true}, nil); err != nil || len(sawFormat) != 1 || sawFormat[0] {
		t.Fatalf("lmstudio: %v %v", err, sawFormat)
	}
}

func TestLearnWindowFromOverflowError(t *testing.T) {
	r := &Router{learned: map[string]int{}}
	r.learnWindow("m", fmt.Errorf("llm: context length exceeded: {\"error\":\"the request exceeds the available context size, n_ctx 4096, try increasing it\"}"))
	if r.learned["m"] != 4096 {
		t.Fatalf("learned %v", r.learned)
	}
	r.learnWindow("m", fmt.Errorf("maximum context length is 8192 tokens"))
	if r.learned["m"] != 4096 { // only ever shrinks
		t.Fatalf("learned %v", r.learned)
	}
}

func TestDroppedStreamIsReportedNotAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"half an ans\"}}]}\n\n") // no finish_reason, no [DONE]
	}))
	defer srv.Close()
	_, err := NewClient().Chat(context.Background(), Endpoint{BaseURL: srv.URL, Model: "m"}, Request{}, nil)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("a stream that just stops must be an error, got %v", err)
	}
}

func TestOutputLimitInsideToolCallIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"shell","arguments":"{\"command\":\"rm -r"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"length"}]}`)
	}))
	defer srv.Close()
	if _, err := NewClient().Chat(context.Background(), Endpoint{BaseURL: srv.URL, Model: "m", Tools: true}, Request{Tools: []ToolSpec{{Name: "shell"}}}, nil); !errors.Is(err, ErrTruncated) {
		t.Fatalf("half a tool call must not be executed: %v", err)
	}
	// plain text cut by the limit is returned, flagged for the caller
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"partial"},"finish_reason":"length"}]}`)
	}))
	defer srv2.Close()
	res, err := NewClient().Chat(context.Background(), Endpoint{BaseURL: srv2.URL, Model: "m"}, Request{}, nil)
	if err != nil || res.FinishReason != "length" || res.Content != "partial" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestStalledServerIsCutOff(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c := NewClient()
	c.IdleTimeout = 300 * time.Millisecond
	start := time.Now()
	_, err := c.Chat(context.Background(), Endpoint{BaseURL: srv.URL, Model: "m"}, Request{}, nil)
	if err == nil || !strings.Contains(err.Error(), "stalled") || time.Since(start) > 5*time.Second {
		t.Fatalf("a silent stream must be aborted quickly: %v after %s", err, time.Since(start))
	}
}

var tinyPNG = append([]byte("\x89PNG\r\n\x1a\n"), []byte("not really a picture, just bytes")...)

func TestImagesGoOutAsImageParts(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		sse(w, `{"choices":[{"delta":{"content":"a cat"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	msgs := []Message{
		{Role: "user", Content: "what is this?", Images: []Image{{MIME: "image/png", Data: tinyPNG}, {MIME: "image/jpeg"}}}, // the second has no bytes yet: skipped
		{Role: "user", Content: "plain"},
	}
	if _, err := NewClient().Chat(context.Background(), Endpoint{BaseURL: srv.URL, Model: "m"}, Request{Messages: msgs}, nil); err != nil {
		t.Fatal(err)
	}
	ms := body["messages"].([]any)
	parts, ok := ms[0].(map[string]any)["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("a picture message must be content parts (text + 1 image): %v", ms[0])
	}
	if parts[0].(map[string]any)["type"] != "text" || parts[0].(map[string]any)["text"] != "what is this?" {
		t.Fatalf("text part first: %v", parts[0])
	}
	url := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image part must be a data URL: %.40s", url)
	}
	if s, ok := ms[1].(map[string]any)["content"].(string); !ok || s != "plain" {
		t.Fatalf("messages without pictures stay plain strings: %v", ms[1])
	}
}

func TestTextOnlyModelsGetANoteNotImages(t *testing.T) {
	in := []Message{{Role: "user", Content: "look", Images: []Image{{MIME: "image/png", Data: tinyPNG}}}, {Role: "assistant", Content: "ok"}}
	out := stripImages(in)
	if len(out[0].Images) != 0 || !strings.Contains(out[0].Content, "cannot see images") || !strings.HasPrefix(out[0].Content, "look") {
		t.Fatalf("%+v", out[0])
	}
	if len(in[0].Images) != 1 {
		t.Fatal("stripping must not mutate the caller's history")
	}
	if got := stripImages(in[1:]); &got[0] != &in[1:][0] {
		t.Fatal("no pictures → no copy")
	}
	if n := EstimateMessages(in[:1]); n < ImageTokens {
		t.Fatalf("a picture must count toward the context budget: %d", n)
	}
}
