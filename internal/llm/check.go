package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Model check: small probes of the abilities PRISM leans on, run against one configured model, so the choice of main and auxiliary
// models rests on evidence instead of guesswork. A model that answers chat fine can still write tool calls as plain text (the
// agent then ends a run with a made-up report), break JSON mode (memory and planning calls fail) or ignore "reply NO_REPLY".

// Probe is the outcome of one check.
type Probe struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	OK    bool   `json:"ok"`
	Ms    int64  `json:"ms"`
	Note  string `json:"note,omitempty"`
}

// CheckReport is what a model check found.
type CheckReport struct {
	Model        string    `json:"model"`
	Ref          string    `json:"ref"`
	Probes       []Probe   `json:"probes"`
	TokensPerSec float64   `json:"tokens_per_sec,omitempty"`
	Window       int       `json:"window"`
	Role         string    `json:"role"` // main | aux | none — what the model is fit for
	Verdict      string    `json:"verdict"`
	At           time.Time `json:"at"`
}

const probeTimeout = 3 * time.Minute

// CheckModel runs the probes against ref (a model name, a list or "role:chat" / "role:fast").
func (r *Router) CheckModel(ctx context.Context, ref string) CheckReport {
	rep := CheckReport{Ref: ref, At: time.Now(), Window: r.Window(ctx, ref)}
	run := func(id, label string, f func(ctx context.Context) (string, error)) bool {
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		t0 := time.Now()
		note, err := f(pctx)
		p := Probe{ID: id, Label: label, Ms: time.Since(t0).Milliseconds(), OK: err == nil, Note: note}
		if err != nil {
			p.Note = err.Error()
		}
		rep.Probes = append(rep.Probes, p)
		return p.OK
	}
	chat := func(ctx context.Context, req Request) (*Response, error) {
		resp, err := r.Chat(ctx, ref, req, nil)
		if err == nil && resp != nil && rep.Model == "" {
			rep.Model = resp.Model
		}
		return resp, err
	}
	sys := func(s string) Message { return Message{Role: "system", Content: s} }
	usr := func(s string) Message { return Message{Role: "user", Content: s} }

	run("reply", "Answers a plain question", func(ctx context.Context) (string, error) {
		t0 := time.Now()
		resp, err := chat(ctx, Request{Messages: []Message{usr("Reply with exactly one word: PONG")}, MaxTokens: 400})
		if err != nil {
			return "", err
		}
		if resp.Usage.Completion > 0 {
			if secs := time.Since(t0).Seconds(); secs > 0.2 {
				rep.TokensPerSec = float64(resp.Usage.Completion) / secs
			}
		}
		if !strings.Contains(strings.ToUpper(resp.Content), "PONG") {
			return "", fmt.Errorf("answered %q instead of PONG", clipStr(resp.Content, 60))
		}
		return fmt.Sprintf("%.0f tokens/s", rep.TokensPerSec), nil
	})
	run("json", "Returns valid JSON when asked", func(ctx context.Context) (string, error) {
		resp, err := chat(ctx, Request{JSON: true, MaxTokens: 600, Messages: []Message{
			sys("Answer with a single JSON object and nothing else."),
			usr(`From the sentence "Anna, 34, lives in Lisbon and plays the cello" extract {"name": string, "age": number, "city": string, "instrument": string}.`)}})
		if err != nil {
			return "", err
		}
		var out struct {
			Name       string  `json:"name"`
			Age        float64 `json:"age"`
			City       string  `json:"city"`
			Instrument string  `json:"instrument"`
		}
		if err := json.Unmarshal([]byte(ExtractJSON(resp.Content)), &out); err != nil {
			return "", fmt.Errorf("not valid JSON: %q", clipStr(resp.Content, 80))
		}
		if !strings.EqualFold(out.Name, "Anna") || int(out.Age) != 34 || !strings.EqualFold(out.City, "Lisbon") {
			return "", fmt.Errorf("valid JSON but wrong content: %s", clipStr(resp.Content, 80))
		}
		return "", nil
	})
	run("tools", "Makes a real tool call and uses its result", func(ctx context.Context) (string, error) {
		spec := ToolSpec{Name: "get_time", Description: "Get the current local time in a city.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string","description":"city name"}},"required":["city"]}`)}
		msgs := []Message{sys("You are an assistant with tools. Use a tool whenever one fits; never write a tool call as text."), usr("What time is it in Paris right now? Use the tool.")}
		resp, err := chat(ctx, Request{Messages: msgs, Tools: []ToolSpec{spec}, MaxTokens: 600})
		if err != nil {
			return "", err
		}
		if len(resp.ToolCalls) == 0 {
			low := strings.ToLower(resp.Content)
			if strings.Contains(low, "get_time(") || strings.Contains(low, "[tool") || strings.Contains(low, "<tool_call") {
				return "", fmt.Errorf("wrote the tool call as text instead of making it (agents on this model would invent results)")
			}
			return "", fmt.Errorf("did not call the tool: %q", clipStr(resp.Content, 80))
		}
		tc := resp.ToolCalls[0]
		var args struct {
			City string `json:"city"`
		}
		if tc.Name != "get_time" || json.Unmarshal([]byte(tc.Arguments), &args) != nil || strings.TrimSpace(args.City) == "" {
			return "", fmt.Errorf("called %s with unusable arguments %q", tc.Name, clipStr(tc.Arguments, 60))
		}
		msgs = append(msgs, Message{Role: "assistant", ToolCalls: resp.ToolCalls}, Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Name, Content: "14:07"})
		resp2, err := chat(ctx, Request{Messages: msgs, Tools: []ToolSpec{spec}, MaxTokens: 600})
		if err != nil {
			return "", err
		}
		if !strings.Contains(resp2.Content, "14:07") && !strings.Contains(resp2.Content, "2:07") {
			return "", fmt.Errorf("called the tool but did not use its result: %q", clipStr(resp2.Content, 80))
		}
		return "", nil
	})
	run("silence", "Stays silent when told to (NO_REPLY)", func(ctx context.Context) (string, error) {
		resp, err := chat(ctx, Request{MaxTokens: 400, Messages: []Message{
			sys("You watch listings for a user. If the user's message says nothing relevant was found, reply with exactly NO_REPLY and nothing else. Otherwise write a one-line message."),
			usr("Checked 12 ads: no bike under 100 euro right now.")}})
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(strings.Trim(strings.TrimSpace(resp.Content), ".`\"")) != "NO_REPLY" {
			return "", fmt.Errorf("answered %q instead of NO_REPLY", clipStr(resp.Content, 80))
		}
		return "", nil
	})

	ok := map[string]bool{}
	for _, p := range rep.Probes {
		ok[p.ID] = p.OK
	}
	switch {
	case !ok["reply"]:
		rep.Role, rep.Verdict = "none", "Does not answer reliably — not usable."
	case ok["tools"] && ok["json"]:
		rep.Role, rep.Verdict = "main", "Good as the main model: tool calls and JSON both work."
		if !ok["silence"] {
			rep.Verdict += " It tends to chatter where it should stay silent, so background jobs may over-report."
		}
	case ok["json"]:
		rep.Role, rep.Verdict = "aux", "Fine as the auxiliary model (extraction, classification, planning). Do not give it agents that need tools — it does not make tool calls reliably."
	case ok["tools"]:
		rep.Role, rep.Verdict = "main", "Makes tool calls, but its JSON mode is unreliable — memory and planning actions should use another model."
	default:
		rep.Role, rep.Verdict = "none", "Neither tool calls nor JSON work reliably — not recommended for PRISM."
	}
	if rep.Model == "" {
		rep.Model = ref
	}
	return rep
}

func clipStr(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
