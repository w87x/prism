package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"prism/internal/codexapp"
)

// KindCodex is a provider that runs models on the user's ChatGPT subscription through the Codex CLI's app-server (see codexapp). It has
// no base URL and no key: Codex holds the sign-in.
const KindCodex = "codex"

// CodexBaseURL marks a provider as the Codex one in lists and in discovery.
const CodexBaseURL = "codex://app-server"

// The model sees none of Codex's own machinery: its system prompt is replaced by PRISM's, and it is told it has no tools of its own.
const codexNoTools = "\n\n[Runtime] You are the language model of an application. You have no tools of your own here — do not run commands, read or write files, or browse; work only from this conversation."

// toolContract is how tool calls travel: the app-server has no slot for the application's tool definitions, so a turn that may call tools
// answers in a fixed JSON shape and PRISM turns that back into tool calls. (arguments is a JSON object written as a string because a strict
// output schema cannot describe every tool's parameters.)
const toolContract = `

[Tools] You may call the tools listed below. Answer with ONE JSON object and nothing else:
{"reply": "<what you say to the user — empty when you are calling tools>", "tool_calls": [{"name": "<tool name>", "arguments": "<a JSON object, written as a string>"}]}
Put tool calls in tool_calls to run them now (several are allowed when they are independent); leave tool_calls empty to give your final answer in reply. Never invent a tool that is not listed.

TOOLS:
`

func codexSchema(tools []ToolSpec) json.RawMessage {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	s := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"reply", "tool_calls"},
		"properties": map[string]any{
			"reply": map[string]any{"type": "string"},
			"tool_calls": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"name", "arguments"},
				"properties": map[string]any{"name": map[string]any{"type": "string", "enum": names}, "arguments": map[string]any{"type": "string"}},
			}},
		},
	}
	b, _ := json.Marshal(s)
	return b
}

func toolList(tools []ToolSpec) string {
	var sb strings.Builder
	for _, t := range tools {
		params := string(t.Parameters)
		if params == "" {
			params = `{"type":"object","properties":{}}`
		}
		fmt.Fprintf(&sb, "- %s: %s\n  parameters: %s\n", t.Name, strings.Join(strings.Fields(t.Description), " "), params)
	}
	return sb.String()
}

// transcript renders the conversation (without the system prompt) as the text of one turn.
func transcript(ms []Message) string {
	names := map[string]string{} // tool call id → tool name, so a result says which call it answers
	var sb strings.Builder
	for _, m := range ms {
		switch m.Role {
		case "system":
		case "user":
			sb.WriteString("USER:\n" + m.Content + "\n\n")
			if n := len(m.Images); n > 0 {
				fmt.Fprintf(&sb, "(%d picture(s) attached to this message)\n\n", n)
			}
		case "assistant":
			sb.WriteString("ASSISTANT:\n")
			if strings.TrimSpace(m.Content) != "" {
				sb.WriteString(m.Content + "\n")
			}
			for _, tc := range m.ToolCalls {
				names[tc.ID] = tc.Name
				fmt.Fprintf(&sb, "[called tool %s with %s]\n", tc.Name, tc.Arguments)
			}
			sb.WriteString("\n")
		case "tool":
			name := firstNonEmpty(m.Name, names[m.ToolCallID], "tool")
			fmt.Fprintf(&sb, "TOOL RESULT (%s):\n%s\n\n", name, m.Content)
		}
	}
	sb.WriteString("Continue as the ASSISTANT.")
	return sb.String()
}

func firstNonEmpty(a ...string) string {
	for _, x := range a {
		if x != "" {
			return x
		}
	}
	return ""
}

func randID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "call_" + hex.EncodeToString(b[:])
}

// codexChat answers a Chat request through the Codex app-server.
func (c *Client) codexChat(ctx context.Context, ep Endpoint, r Request, onDelta func(Delta)) (*Response, error) {
	var system []string
	for _, m := range r.Messages {
		if m.Role == "system" && strings.TrimSpace(m.Content) != "" {
			system = append(system, m.Content)
		}
	}
	instr := strings.Join(system, "\n\n") + codexNoTools
	withTools := len(r.Tools) > 0 && ep.Tools
	var schema json.RawMessage
	if withTools {
		instr += toolContract + toolList(r.Tools)
		schema = codexSchema(r.Tools)
	} else if r.JSON {
		instr += "\n\nAnswer with a single JSON object and nothing else — no commentary, no code fences."
	}

	// pictures go to Codex as files
	var files []string
	if len(r.Messages) > 0 {
		dir, err := os.MkdirTemp("", "prism-codex-img-*")
		if err == nil {
			defer os.RemoveAll(dir)
			for _, m := range r.Messages {
				for i, im := range m.Images {
					ext := ".png"
					if strings.Contains(im.MIME, "jpeg") || strings.Contains(im.MIME, "jpg") {
						ext = ".jpg"
					} else if strings.Contains(im.MIME, "webp") {
						ext = ".webp"
					}
					f := filepath.Join(dir, fmt.Sprintf("img%d-%d%s", len(files), i, ext))
					if os.WriteFile(f, im.Data, 0o600) == nil {
						files = append(files, f)
					}
				}
			}
		}
	}

	rq := codexapp.Request{Model: ep.Model, Instructions: instr, Input: transcript(r.Messages), Images: files, Schema: schema}
	if onDelta != nil && !withTools {
		rq.OnDelta = func(s string) { onDelta(Delta{Content: s}) }
	}
	res, err := codexapp.Default.Complete(ctx, rq)
	if err != nil {
		var ce *codexapp.Error
		if errors.As(err, &ce) {
			switch {
			case ce.Limit:
				return nil, &APIError{Status: 429, Body: ce.Message, RetryAfter: 5 * time.Minute}
			case ce.Auth:
				return nil, &APIError{Status: 401, Body: ce.Message + " — run `codex login` and choose Sign in with ChatGPT"}
			}
		}
		return nil, err
	}
	// Codex reports its own count, which includes ~12k tokens of built-in instructions and tool definitions that PRISM neither sent nor
	// controls; the context accounting (compaction) must see what PRISM actually put in the prompt, so that is what is reported.
	out := &Response{Model: ep.Model, Usage: Usage{Prompt: EstimateTokens(instr) + EstimateTokens(rq.Input) + len(files)*ImageTokens, Completion: res.Out}, FinishReason: "stop"}
	if res.Out == 0 {
		out.Usage.Completion = EstimateTokens(res.Text)
	}
	if !withTools {
		out.Content = res.Text
		return out, nil
	}
	var v struct {
		Reply     string `json:"reply"`
		ToolCalls []struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"tool_calls"`
	}
	if err := decodeJSON(ExtractJSON(res.Text), &v); err != nil {
		out.Content = res.Text // the model ignored the shape: its words are still an answer
		return out, nil
	}
	out.Content = v.Reply
	for _, tc := range v.ToolCalls {
		args := strings.TrimSpace(tc.Arguments)
		if args == "" {
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: randID(), Name: tc.Name, Arguments: args})
	}
	if len(out.ToolCalls) > 0 {
		out.FinishReason = "tool_calls"
	}
	return out, nil
}

// codexModels lists what the signed-in plan offers, in the shape of model discovery.
func codexModels(ctx context.Context) ([]Discovered, error) {
	ms, err := codexapp.Default.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Discovered, 0, len(ms))
	for _, m := range ms {
		// the real window is ~258k tokens; 200k leaves room for what Codex adds around PRISM's prompt
		out = append(out, Discovered{ID: m.ID, Kind: "chat", Tools: true, Vision: m.Images, Loaded: true, Context: 200000, MaxContext: 200000})
	}
	return out, nil
}
