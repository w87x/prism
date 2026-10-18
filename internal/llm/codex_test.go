package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/codexapp"
)

// TestFakeAppServer is not a test: when PRISM_FAKE_CODEX is set, the test binary re-runs itself as a stand-in for `codex app-server`.
func TestFakeAppServer(t *testing.T) {
	if os.Getenv("PRISM_FAKE_CODEX") != "1" {
		return
	}
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	out := json.NewEncoder(os.Stdout)
	threads := 0
	reply := func(id json.RawMessage, v any) { _ = out.Encode(map[string]any{"id": id, "result": v}) }
	note := func(method string, params any) { _ = out.Encode(map[string]any{"method": method, "params": params}) }
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(line, &m) != nil || m.Method == "" {
			continue
		}
		switch m.Method {
		case "initialize":
			reply(m.ID, map[string]any{"userAgent": "fake", "codexHome": "/x"})
		case "account/read":
			if os.Getenv("PRISM_FAKE_NOLOGIN") == "1" {
				reply(m.ID, map[string]any{"account": nil, "requiresOpenaiAuth": true})
			} else {
				reply(m.ID, map[string]any{"account": map[string]any{"type": "chatgpt", "planType": "plus"}})
			}
		case "model/list":
			reply(m.ID, map[string]any{"data": []any{
				map[string]any{"id": "gpt-x", "model": "gpt-x", "displayName": "GPT X", "hidden": false, "isDefault": true, "inputModalities": []string{"text", "image"}, "supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "low"}}},
				map[string]any{"id": "secret", "model": "secret", "hidden": true},
			}, "nextCursor": nil})
		case "thread/start":
			if dt, ok := m.Params["dynamicTools"].([]any); ok && len(dt) > 0 {
				if os.Getenv("PRISM_FAKE_NO_DYNAMIC") == "1" {
					_ = out.Encode(map[string]any{"id": m.ID, "error": map[string]any{"code": -32602, "message": "unknown field dynamicTools"}})
					continue
				}
				os.Setenv(fmt.Sprintf("FAKE_TOOL_t%d", threads+1), fmt.Sprint(dt[0].(map[string]any)["name"]))
			}
			threads++
			reply(m.ID, map[string]any{"thread": map[string]any{"id": fmt.Sprintf("t%d", threads)}})
			// remember this thread's instructions for the turn
			os.Setenv(fmt.Sprintf("FAKE_INSTR_t%d", threads), fmt.Sprint(m.Params["baseInstructions"]))
		case "account/login/start":
			if m.Params["type"] == "chatgptDeviceCode" {
				reply(m.ID, map[string]any{"type": "chatgptDeviceCode", "loginId": "L2", "userCode": "ABCD-1234", "verificationUrl": "https://example.test/device"})
			} else {
				reply(m.ID, map[string]any{"type": "chatgpt", "loginId": "L1", "authUrl": "https://example.test/login"})
				note("account/login/completed", map[string]any{"loginId": "L1", "success": true, "error": nil})
			}
		case "turn/interrupt":
			tid := fmt.Sprint(m.Params["threadId"])
			reply(m.ID, map[string]any{})
			note("turn/completed", map[string]any{"threadId": tid, "turn": map[string]any{"id": "u1", "status": "interrupted", "items": []any{}}})
		case "turn/start":
			tid := fmt.Sprint(m.Params["threadId"])
			reply(m.ID, map[string]any{"turn": map[string]any{"id": "u1", "status": "inProgress"}})
			input := ""
			if items, ok := m.Params["input"].([]any); ok && len(items) > 0 {
				if it, ok := items[0].(map[string]any); ok {
					input = fmt.Sprint(it["text"])
				}
			}
			instr := os.Getenv("FAKE_INSTR_" + tid)
			if tool := os.Getenv("FAKE_TOOL_" + tid); tool != "" && !strings.Contains(input, "TOOL RESULT") {
				_ = out.Encode(map[string]any{"id": 9001, "method": "item/tool/call", "params": map[string]any{"threadId": tid, "turnId": "u1", "callId": "c1", "tool": tool, "arguments": map[string]any{"path": "a.txt"}}})
				continue // the turn waits for the answer; the client interrupts instead
			}
			text := "Hello"
			if os.Getenv("FAKE_TOOL_"+tid) != "" {
				text = "the file says hi"
			}
			schema, hasSchema := m.Params["outputSchema"].(map[string]any)
			switch {
			case strings.Contains(input, "FAIL"):
				note("turn/completed", map[string]any{"threadId": tid, "turn": map[string]any{"id": "u1", "status": "failed", "items": []any{}, "error": map[string]any{"message": "You've hit your usage limit. Try again later."}}})
				continue
			case hasSchema && strings.Contains(input, "TOOL RESULT"):
				text = `{"reply":"the file says hi","tool_calls":[]}`
			case hasSchema:
				names := schema["properties"].(map[string]any)["tool_calls"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["name"].(map[string]any)["enum"].([]any)
				text = fmt.Sprintf(`{"reply":"","tool_calls":[{"name":%q,"arguments":"{\"path\":\"a.txt\"}"}]}`, names[0])
			case strings.Contains(input, "WHO"):
				text = instr
			}
			if !hasSchema {
				note("item/agentMessage/delta", map[string]any{"threadId": tid, "turnId": "u1", "itemId": "i", "delta": text[:1]})
				note("item/agentMessage/delta", map[string]any{"threadId": tid, "turnId": "u1", "itemId": "i", "delta": text[1:]})
			}
			note("thread/tokenUsage/updated", map[string]any{"threadId": tid, "turnId": "u1", "tokenUsage": map[string]any{"last": map[string]any{"inputTokens": 11, "outputTokens": 7}, "modelContextWindow": 200000}})
			note("turn/completed", map[string]any{"threadId": tid, "turn": map[string]any{"id": "u1", "status": "completed", "items": []any{map[string]any{"type": "agentMessage", "id": "i", "text": text}}}})
		}
	}
}

func fakeCodex(t *testing.T, env ...string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	body := "#!/bin/sh\nPRISM_FAKE_CODEX=1 exec '" + exe + "' -test.run=TestFakeAppServer\n"
	for i := len(env) - 1; i >= 0; i-- {
		body = strings.Replace(body, "PRISM_FAKE_CODEX=1", env[i]+" PRISM_FAKE_CODEX=1", 1)
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRISM_CODEX_BIN", script)
	codexapp.Default.Close()
	t.Cleanup(codexapp.Default.Close)
}

func TestCodexAnswersPlainTurnsAndStreams(t *testing.T) {
	fakeCodex(t)
	c := NewClient()
	var got strings.Builder
	res, err := c.Chat(context.Background(), Endpoint{Kind: KindCodex, Model: "gpt-x"}, Request{Messages: []Message{
		{Role: "system", Content: "You are Atlas."}, {Role: "user", Content: "hi"}}}, func(d Delta) { got.WriteString(d.Content) })
	if err != nil || res.Content != "Hello" || got.String() != "Hello" {
		t.Fatalf("plain answer, streamed: %+v %v %q", res, err, got.String())
	}
	if res.Usage.Prompt < 3 || res.Usage.Prompt > 200 || res.Usage.Completion != 7 {
		t.Fatalf("the prompt side is PRISM's own estimate (Codex adds ~12k of its own), the completion is Codex's count: %+v", res.Usage)
	}
	res, _ = c.Chat(context.Background(), Endpoint{Kind: KindCodex, Model: "gpt-x"}, Request{Messages: []Message{
		{Role: "system", Content: "You are Atlas."}, {Role: "user", Content: "WHO are you"}}}, nil)
	if !strings.Contains(res.Content, "You are Atlas.") || !strings.Contains(res.Content, "no tools of your own") {
		t.Fatalf("PRISM's system prompt replaces Codex's own, and Codex is told it has no tools: %q", res.Content)
	}
}

func TestCodexToolCallsAreNative(t *testing.T) {
	fakeCodex(t)
	nativeToolsBroken.Store(false)
	c := NewClient()
	tools := []ToolSpec{{Name: "file_read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}}
	ep := Endpoint{Kind: KindCodex, Model: "gpt-x", Tools: true}
	res, err := c.Chat(context.Background(), ep, Request{Tools: tools, Messages: []Message{{Role: "system", Content: "S"}, {Role: "user", Content: "read a.txt"}}}, nil)
	if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "file_read" || res.ToolCalls[0].ID != "c1" || !strings.Contains(res.ToolCalls[0].Arguments, "a.txt") || res.FinishReason != "tool_calls" {
		t.Fatalf("the model's own function call comes back as one: %+v %v", res, err)
	}
	if nativeToolsBroken.Load() {
		t.Fatal("native tools worked, so they are not marked broken")
	}
	res, err = c.Chat(context.Background(), ep, Request{Tools: tools, Messages: []Message{{Role: "system", Content: "S"}, {Role: "user", Content: "read a.txt"},
		{Role: "assistant", ToolCalls: res.ToolCalls}, {Role: "tool", ToolCallID: "c1", Content: "hi"}}}, nil)
	if err != nil || len(res.ToolCalls) != 0 || res.Content != "the file says hi" {
		t.Fatalf("with the result in hand it answers: %+v %v", res, err)
	}
}

func TestCodexSignInStartsAndCompletes(t *testing.T) {
	fakeCodex(t)
	ctx := context.Background()
	l, err := codexapp.Default.LoginStart(ctx, false)
	if err != nil || l.URL != "https://example.test/login" || l.ID != "L1" {
		t.Fatalf("browser sign-in: %+v %v", l, err)
	}
	var st codexapp.LoginResult
	for i := 0; i < 50 && !st.Done; i++ {
		time.Sleep(20 * time.Millisecond)
		st = codexapp.Default.LoginStatus(l.ID)
	}
	if !st.Done || !st.Success {
		t.Fatalf("the sign-in's completion is remembered: %+v", st)
	}
	d, err := codexapp.Default.LoginStart(ctx, true)
	if err != nil || d.Code != "ABCD-1234" || d.URL != "https://example.test/device" {
		t.Fatalf("device-code sign-in: %+v %v", d, err)
	}
	if got := codexapp.Default.LoginStatus(d.ID); got.Done {
		t.Fatalf("not finished until Codex says so: %+v", got)
	}
}

func TestCodexToolCallsFallBackToTheOutputSchemaWhenToolsAreRefused(t *testing.T) {
	fakeCodex(t, "PRISM_FAKE_NO_DYNAMIC=1")
	nativeToolsBroken.Store(false)
	t.Cleanup(func() { nativeToolsBroken.Store(false) })
	c := NewClient()
	tools := []ToolSpec{{Name: "file_read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}}
	ep := Endpoint{Kind: KindCodex, Model: "gpt-x", Tools: true}
	res, err := c.Chat(context.Background(), ep, Request{Tools: tools, Messages: []Message{{Role: "system", Content: "S"}, {Role: "user", Content: "read a.txt"}}}, nil)
	if err != nil || len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "file_read" || !strings.Contains(res.ToolCalls[0].Arguments, "a.txt") || res.ToolCalls[0].ID == "" || res.FinishReason != "tool_calls" {
		t.Fatalf("a tool call comes back as one: %+v %v", res, err)
	}
	if !nativeToolsBroken.Load() {
		t.Fatal("a Codex that refuses client tools is remembered, so later turns do not try again")
	}
	res, err = c.Chat(context.Background(), ep, Request{Tools: tools, Messages: []Message{{Role: "system", Content: "S"}, {Role: "user", Content: "read a.txt"},
		{Role: "assistant", ToolCalls: res.ToolCalls}, {Role: "tool", ToolCallID: res.ToolCalls[0].ID, Content: "hi"}}}, nil)
	if err != nil || len(res.ToolCalls) != 0 || res.Content != "the file says hi" {
		t.Fatalf("with the result in hand it answers: %+v %v", res, err)
	}
}

func TestCodexJSONModeAndErrors(t *testing.T) {
	fakeCodex(t)
	c := NewClient()
	var v struct{ Word string }
	res, err := c.Chat(context.Background(), Endpoint{Kind: KindCodex, Model: "gpt-x"}, Request{JSON: true, Messages: []Message{{Role: "user", Content: "WHO"}}}, nil)
	if err != nil || !strings.Contains(res.Content, "single JSON object") {
		t.Fatalf("JSON mode is a prompt instruction: %+v %v", res, err)
	}
	_ = v
	_, err = c.Chat(context.Background(), Endpoint{Kind: KindCodex, Model: "gpt-x"}, Request{Messages: []Message{{Role: "user", Content: "FAIL please"}}}, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 429 || ae.RetryAfter < time.Minute {
		t.Fatalf("a used-up plan looks like a rate limit so the router falls back to another model: %v", err)
	}
	if _, err := c.Embed(context.Background(), Endpoint{Kind: KindCodex}, []string{"x"}); err == nil {
		t.Fatal("no embeddings from the subscription")
	}
}

func TestCodexStatusAndModels(t *testing.T) {
	fakeCodex(t)
	ctx := context.Background()
	st := codexapp.Default.Status(ctx)
	if !st.Installed || !st.LoggedIn || st.Plan != "plus" || st.Kind != "chatgpt" {
		t.Fatalf("status: %+v", st)
	}
	ms, err := NewClient().ListModels(ctx, CodexBaseURL, "")
	if err != nil || len(ms) != 1 || ms[0].ID != "gpt-x" || !ms[0].Vision || !ms[0].Tools {
		t.Fatalf("hidden models are left out: %+v %v", ms, err)
	}
	fakeCodex(t, "PRISM_FAKE_NOLOGIN=1")
	if st := codexapp.Default.Status(ctx); st.LoggedIn || !strings.Contains(st.Error, "codex login") {
		t.Fatalf("not signed in is said plainly: %+v", st)
	}
	t.Setenv("PRISM_CODEX_BIN", "/nonexistent/codex")
	codexapp.Default.Close()
	if st := codexapp.Default.Status(ctx); st.Installed {
		t.Fatalf("missing binary: %+v", st)
	}
}

// Runs against the real Codex CLI and the signed-in ChatGPT plan: PRISM_CODEX_LIVE=1 go test -run TestCodexLive ./internal/llm -v
func TestCodexLive(t *testing.T) {
	if os.Getenv("PRISM_CODEX_LIVE") != "1" {
		t.Skip("PRISM_CODEX_LIVE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	c := NewClient()
	ms, err := c.ListModels(ctx, CodexBaseURL, "")
	if err != nil || len(ms) == 0 {
		t.Fatalf("models: %v %v", ms, err)
	}
	t.Logf("models: %+v", ms)
	ep := Endpoint{Kind: KindCodex, Model: ms[0].ID, Tools: true}
	raw, err := codexapp.Default.Complete(ctx, codexapp.Request{Model: ms[0].ID, Instructions: "Answer in one word.", Input: "Colour of a clear daytime sky?", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("input tokens Codex reports for a one-word question: %d (about 12.4k without the slimming)", raw.In)
	if raw.In > 8000 {
		t.Fatalf("Codex's own context is not being slimmed: %d input tokens", raw.In)
	}
	t0 := time.Now()
	res, err := c.Chat(ctx, ep, Request{Messages: []Message{{Role: "system", Content: "You are a terse assistant."}, {Role: "user", Content: "In one word: what colour is a clear daytime sky?"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("plain (%v): %q usage=%+v", time.Since(t0).Round(time.Millisecond), res.Content, res.Usage)
	tools := []ToolSpec{{Name: "calculator", Description: "Evaluate an arithmetic expression exactly.", Parameters: json.RawMessage(`{"type":"object","properties":{"expression":{"type":"string"}},"required":["expression"]}`)}}
	t0 = time.Now()
	res, err = c.Chat(ctx, ep, Request{Tools: tools, Messages: []Message{{Role: "system", Content: "You are an assistant. Use tools for arithmetic."}, {Role: "user", Content: "What is 4817 * 3921? Use the calculator."}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tool turn (%v): content=%q calls=%+v", time.Since(t0).Round(time.Millisecond), res.Content, res.ToolCalls)
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "calculator" {
		t.Fatalf("expected one calculator call, got %+v", res)
	}
	res2, err := c.Chat(ctx, ep, Request{Tools: tools, Messages: []Message{{Role: "system", Content: "You are an assistant. Use tools for arithmetic."}, {Role: "user", Content: "What is 4817 * 3921? Use the calculator."},
		{Role: "assistant", ToolCalls: res.ToolCalls}, {Role: "tool", ToolCallID: res.ToolCalls[0].ID, Name: "calculator", Content: "18887457"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after the tool result: %q calls=%d", res2.Content, len(res2.ToolCalls))
	if len(res2.ToolCalls) != 0 || !strings.Contains(strings.ReplaceAll(res2.Content, ",", ""), "18887457") {
		t.Fatalf("it should answer with the result: %+v", res2)
	}
	var v struct {
		OK bool `json:"ok"`
	}
	if err := c.completeJSONForTest(ctx, ep, &v); err != nil {
		t.Fatal(err)
	}
	t.Logf("json mode ok=%v", v.OK)
}

func (c *Client) completeJSONForTest(ctx context.Context, ep Endpoint, out any) error {
	res, err := c.Chat(ctx, ep, Request{JSON: true, Messages: []Message{{Role: "user", Content: `Return {"ok": true}.`}}}, nil)
	if err != nil {
		return err
	}
	return decodeJSON(ExtractJSON(res.Content), out)
}

// Debug aid: replays the model check's tool probe against one Codex model and shows both answers. PRISM_CODEX_LIVE=1 PRISM_CODEX_MODEL=gpt-6-luna
func TestCodexToolProbeDebug(t *testing.T) {
	if os.Getenv("PRISM_CODEX_LIVE") != "1" || os.Getenv("PRISM_CODEX_MODEL") == "" {
		t.Skip("PRISM_CODEX_LIVE / PRISM_CODEX_MODEL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	c := NewClient()
	ep := Endpoint{Kind: KindCodex, Model: os.Getenv("PRISM_CODEX_MODEL"), Tools: true}
	spec := ToolSpec{Name: "get_time", Description: "Get the current local time in a city.", Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}
	msgs := []Message{{Role: "system", Content: "You are an assistant with tools. Use a tool whenever one fits; never write a tool call as text."}, {Role: "user", Content: "What time is it in Paris right now? Use the tool."}}
	r1, err := c.Chat(ctx, ep, Request{Messages: msgs, Tools: []ToolSpec{spec}, MaxTokens: 600}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first: content=%q calls=%+v", r1.Content, r1.ToolCalls)
	if len(r1.ToolCalls) == 0 {
		return
	}
	msgs = append(msgs, Message{Role: "assistant", ToolCalls: r1.ToolCalls}, Message{Role: "tool", ToolCallID: r1.ToolCalls[0].ID, Name: r1.ToolCalls[0].Name, Content: "14:07"})
	r2, err := c.Chat(ctx, ep, Request{Messages: msgs, Tools: []ToolSpec{spec}, MaxTokens: 600}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("second: content=%q calls=%+v finish=%s", r2.Content, r2.ToolCalls, r2.FinishReason)
}
