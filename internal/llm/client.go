package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is a stateless OpenAI-compatible HTTP client.
type Client struct {
	HTTP *http.Client
	// IdleTimeout aborts a stream that goes silent (0 → 10 minutes). Local servers can be quiet for a long
	// time while they process a big prompt, so it is generous; it only exists so a hung server cannot hold a
	// model slot forever.
	IdleTimeout time.Duration
}

// ErrTruncated means the reply was cut off (dropped stream, or the output limit hit in the middle of tool calls).
var ErrTruncated = errors.New("llm: reply was cut off")

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 10 * time.Minute, // local models may need to load first
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
	}}}
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}
type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func hasImageData(ims []Image) bool {
	for _, im := range ims {
		if len(im.Data) > 0 {
			return true
		}
	}
	return false
}

func toWire(ms []Message) []wireMessage {
	out := make([]wireMessage, 0, len(ms))
	for _, m := range ms {
		w := wireMessage{Role: m.Role, ToolCallID: m.ToolCallID, Name: m.Name}
		switch {
		case hasImageData(m.Images):
			// OpenAI-style multimodal content: text first, then each image as a data URL
			parts := make([]map[string]any, 0, len(m.Images)+1)
			if m.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": m.Content})
			}
			for _, im := range m.Images {
				if len(im.Data) == 0 {
					continue
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{
					"url": "data:" + im.MIME + ";base64," + base64.StdEncoding.EncodeToString(im.Data)}})
			}
			w.Content = parts
		case m.Content == "" && len(m.ToolCalls) > 0:
			w.Content = nil
		default:
			w.Content = m.Content
		}
		for _, tc := range m.ToolCalls {
			var c wireToolCall
			c.ID, c.Type = tc.ID, "function"
			c.Function.Name = tc.Name
			c.Function.Arguments = tc.Arguments
			if c.Function.Arguments == "" {
				c.Function.Arguments = "{}"
			}
			w.ToolCalls = append(w.ToolCalls, c)
		}
		out = append(out, w)
	}
	return out
}

func (c *Client) newReq(ctx context.Context, ep Endpoint, path string, body any) (*http.Request, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(ep.BaseURL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if ep.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	return req, nil
}

func apiError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	e := &APIError{Status: resp.StatusCode, Body: string(b)}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if n, err := strconv.Atoi(ra); err == nil {
			e.RetryAfter = time.Duration(n) * time.Second
		}
	}
	return e
}

func isContextErr(e *APIError) bool {
	l := strings.ToLower(e.Body)
	return e.Status == 400 && (strings.Contains(l, "context length") || strings.Contains(l, "context_length") ||
		strings.Contains(l, "maximum context") || strings.Contains(l, "too many tokens") ||
		strings.Contains(l, "exceeds the context") || strings.Contains(l, "context window"))
}

// Chat performs one streaming completion against ep. onDelta may be nil.
func (c *Client) Chat(ctx context.Context, ep Endpoint, r Request, onDelta func(Delta)) (*Response, error) {
	body := map[string]any{
		"model":    ep.Model,
		"messages": toWire(r.Messages),
		"stream":   true,
		// ask for usage in the final chunk; servers that don't know it ignore it
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(r.Tools) > 0 && ep.Tools {
		tools := make([]map[string]any, 0, len(r.Tools))
		for _, t := range r.Tools {
			params := t.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": params}})
		}
		body["tools"] = tools
	}
	switch {
	case r.Temperature != nil:
		body["temperature"] = *r.Temperature
	case ep.Temp != nil:
		body["temperature"] = *ep.Temp
	}
	if mt := r.MaxTokens; mt > 0 {
		body["max_tokens"] = mt
	} else if ep.MaxOut > 0 {
		body["max_tokens"] = ep.MaxOut
	}
	// JSON mode: LM Studio only accepts json_schema/text, so it relies on the prompt (+ ExtractJSON);
	// other providers get json_object, retried without it if a compatible-but-picky server rejects it.
	if r.JSON && ep.Kind != "lmstudio" {
		body["response_format"] = map[string]any{"type": "json_object"}
	}
	idle := c.IdleTimeout
	if idle <= 0 {
		idle = 10 * time.Minute
	}
	rctx, cancelReq := context.WithCancel(ctx)
	defer cancelReq()
	stalled := false
	watchdog := time.AfterFunc(idle, func() { stalled = true; cancelReq() })
	defer watchdog.Stop()
	do := func() (*http.Response, error) {
		req, err := c.newReq(rctx, ep, "/chat/completions", body)
		if err != nil {
			return nil, err
		}
		return c.HTTP.Do(req)
	}
	resp, err := do()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 400 && body["response_format"] != nil {
		ae := apiError(resp).(*APIError)
		if strings.Contains(ae.Body, "response_format") {
			delete(body, "response_format")
			resp.Body.Close()
			if resp, err = do(); err != nil {
				return nil, err
			}
			defer resp.Body.Close()
		} else {
			return nil, ae
		}
	}
	if resp.StatusCode/100 != 2 {
		ae := apiError(resp).(*APIError)
		if isContextErr(ae) {
			return nil, fmt.Errorf("%w: %s", ErrContextLength, ae.Body)
		}
		return nil, ae
	}
	out := &Response{Model: ep.Model}
	ts := &thinkSplitter{}
	emit := func(content, reasoning string) {
		if reasoning != "" {
			out.Reasoning += reasoning
		}
		if content != "" {
			out.Content += content
		}
		if onDelta != nil && (content != "" || reasoning != "") {
			onDelta(Delta{Content: content, Reasoning: reasoning})
		}
	}
	calls := map[int]*ToolCall{}
	var order []int
	addCall := func(idx int, id, name, args string) {
		tc, ok := calls[idx]
		if !ok {
			tc = &ToolCall{}
			calls[idx] = tc
			order = append(order, idx)
		}
		if id != "" {
			tc.ID = id
		}
		if name != "" {
			tc.Name += name
		}
		tc.Arguments += args
	}

	type chunk struct {
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			Message *struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	handle := func(data []byte) error {
		var ch chunk
		if err := json.Unmarshal(data, &ch); err != nil {
			return nil // ignore keep-alives / malformed
		}
		if ch.Error != nil && ch.Error.Message != "" {
			return fmt.Errorf("llm: stream error: %s", ch.Error.Message)
		}
		if ch.Usage != nil {
			out.Usage = Usage{Prompt: ch.Usage.PromptTokens, Completion: ch.Usage.CompletionTokens}
		}
		for _, choice := range ch.Choices {
			d := choice.Delta
			if rs := d.ReasoningContent + d.Reasoning; rs != "" {
				emit("", rs)
			}
			if d.Content != "" {
				emit(ts.feed(d.Content))
			}
			for i, tc := range d.ToolCalls {
				idx := i
				if tc.Index != nil {
					idx = *tc.Index
				}
				addCall(idx, tc.ID, tc.Function.Name, tc.Function.Arguments)
			}
			if m := choice.Message; m != nil { // non-streaming fallback
				if m.Reasoning != "" {
					emit("", m.Reasoning)
				}
				if m.Content != "" {
					emit(ts.feed(m.Content))
				}
				for i, tc := range m.ToolCalls {
					addCall(i, tc.ID, tc.Function.Name, tc.Function.Arguments)
				}
			}
			if choice.FinishReason != "" {
				out.FinishReason = choice.FinishReason
			}
		}
		return nil
	}

	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if err := handle(b); err != nil {
			return nil, err
		}
	} else {
		rd := bufio.NewReaderSize(resp.Body, 64<<10)
		done := false
		for {
			line, err := rd.ReadString('\n')
			watchdog.Reset(idle)
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if data == "[DONE]" {
					done = true
					break
				}
				if herr := handle([]byte(data)); herr != nil {
					return nil, herr
				}
			}
			if err != nil {
				if stalled {
					return nil, fmt.Errorf("llm: no data from the model for %s (server stalled)", idle)
				}
				if err == io.EOF {
					if !done && out.FinishReason == "" {
						return nil, fmt.Errorf("%w: the stream ended before the model finished", ErrTruncated)
					}
					break
				}
				return nil, err
			}
		}
	}
	emit(ts.flush())
	for i, idx := range order {
		tc := calls[idx]
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano()%1e9, i)
		}
		if strings.TrimSpace(tc.Arguments) == "" {
			tc.Arguments = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, *tc)
	}
	if out.FinishReason == "length" && len(out.ToolCalls) > 0 {
		return nil, fmt.Errorf("%w: the output limit was hit in the middle of a tool call; raise “max output” for this model or ask for less", ErrTruncated)
	}
	if out.Usage.Prompt == 0 && out.Usage.Completion == 0 {
		out.Usage.Prompt = EstimateMessages(r.Messages) + EstimateTools(r.Tools)
		out.Usage.Completion = EstimateTokens(out.Content + out.Reasoning)
	}
	return out, nil
}

// Embed returns one vector per input.
func (c *Client) Embed(ctx context.Context, ep Endpoint, inputs []string) ([][]float32, error) {
	req, err := c.newReq(ctx, ep, "/embeddings", map[string]any{"model": ep.Model, "input": inputs})
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, apiError(resp)
	}
	var r struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	out := make([][]float32, len(inputs))
	for _, d := range r.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	for i := range out {
		if out[i] == nil {
			return nil, fmt.Errorf("llm: embedding %d missing in response", i)
		}
	}
	return out, nil
}

// Discovered describes a model reported by a provider.
type Discovered struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`        // chat | embedding
	Context    int    `json:"context"`     // suggested window: the model's maximum
	MaxContext int    `json:"max_context"` // architectural maximum, if known
	Loaded     bool   `json:"loaded"`      // LM Studio: currently in memory
	Tools      bool   `json:"tools"`       // advertises tool calling (unknown → true)
	Vision     bool   `json:"vision"`
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

func looksLikeEmbedding(id string) bool {
	l := strings.ToLower(id)
	return strings.Contains(l, "embed") || strings.Contains(l, "bge-") || strings.Contains(l, "e5-") || strings.Contains(l, "minilm")
}

// realtimeOnly reports model ids that only speak a streaming/audio protocol (Gemini Live over WebSocket, text
// to speech), which fail every ordinary chat-completions call with HTTP 400.
func realtimeOnly(id string) bool {
	l := strings.ToLower(id)
	for _, p := range strings.FieldsFunc(l, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == '/' || r == ':' }) {
		if p == "live" || p == "tts" || p == "realtime" {
			return true
		}
	}
	return strings.Contains(l, "native-audio")
}

// ListModels queries GET /models. For LM Studio it enriches the result from
// its native /api/v0/models endpoint (type, load state, context, capabilities).
func (c *Client) ListModels(ctx context.Context, baseURL, apiKey string) ([]Discovered, error) {
	get := func(u string, out any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return apiError(resp)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	base := strings.TrimRight(baseURL, "/")
	var native struct {
		Data []struct {
			ID           string   `json:"id"`
			Type         string   `json:"type"`
			State        string   `json:"state"`
			MaxCtx       int      `json:"max_context_length"`
			LoadedCtx    int      `json:"loaded_context_length"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if root := strings.TrimSuffix(base, "/v1"); root != base {
		if err := get(root+"/api/v0/models", &native); err == nil && len(native.Data) > 0 {
			var out []Discovered
			for _, m := range native.Data {
				d := Discovered{ID: m.ID, Kind: "chat", MaxContext: m.MaxCtx, Loaded: m.State == "loaded", Vision: m.Type == "vlm" || hasCap(m.Capabilities, "vision")}
				// use the model's full window; if the server actually loaded it smaller, the router
				// learns the real limit from the first overflow error (see Router.learnWindow)
				switch {
				case m.MaxCtx > 0:
					d.Context = m.MaxCtx
				case m.LoadedCtx > 0:
					d.Context = m.LoadedCtx
				}
				if m.Type == "embeddings" || looksLikeEmbedding(m.ID) {
					d.Kind = "embedding"
				}
				for _, cp := range m.Capabilities {
					if cp == "tool_use" {
						d.Tools = true
					}
				}
				d.Tools = d.Tools && d.Kind == "chat"
				out = append(out, d)
			}
			return out, nil
		}
	}
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := get(base+"/models", &r); err != nil {
		return nil, err
	}
	var out []Discovered
	for _, m := range r.Data {
		id := strings.TrimPrefix(m.ID, "models/")
		if realtimeOnly(id) {
			continue
		}
		d := Discovered{ID: id, Kind: "chat", Tools: true, Vision: true} // unknown → assume yes; the model list lets you turn it off
		if looksLikeEmbedding(id) {
			d.Kind, d.Tools, d.Vision = "embedding", false, false
		}
		out = append(out, d)
	}
	return out, nil
}
