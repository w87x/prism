package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// ExtractJSON pulls the first JSON object/array out of an LLM answer (tolerates code fences).
func ExtractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			s = strings.TrimSpace(rest[:j])
		}
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return s
	}
	open := s[start]
	closeCh := byte('}')
	if open == '[' {
		closeCh = ']'
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

// CompleteJSON asks for a JSON answer and decodes it into out. Small local models regularly return an empty
// reply (all the budget went into thinking) or a JSON object that is cut off; giving up on the first bad
// reply made background jobs (task summaries, memory extraction…) fail for no good reason. A bad reply is
// retried twice with the bad answer and a correction in the conversation; the final error says what came
// back so the cause is visible in the logs.
func (r *Router) CompleteJSON(ctx context.Context, ref, system, user string, out any) error {
	msgs := []Message{}
	if system != "" {
		msgs = append(msgs, Message{Role: "system", Content: system})
	}
	msgs = append(msgs, Message{Role: "user", Content: user})
	var raw string
	var perr error
	for attempt := 0; attempt < 3; attempt++ {
		res, err := r.Chat(ctx, ref, Request{Messages: msgs, JSON: true}, nil)
		if err != nil {
			return err
		}
		raw = res.Content
		if perr = decodeJSON(ExtractJSON(raw), out); perr == nil {
			return nil
		}
		if strings.TrimSpace(raw) != "" {
			msgs = append(msgs, Message{Role: "assistant", Content: raw})
		}
		msgs = append(msgs, Message{Role: "user", Content: "That was not a complete, valid JSON object. Reply again with ONLY the JSON object — no commentary, no markdown — and keep every field short."})
	}
	snip := strings.TrimSpace(raw)
	if r := []rune(snip); len(r) > 100 {
		snip = string(r[:100]) + "…"
	}
	return fmt.Errorf("unparsable model output after 3 tries (last reply %d chars: %q): %w", len(raw), snip, perr)
}

// decodeJSON unmarshals into out. Models often answer a {"facts":[...]} request with the bare array; when out
// is a struct with exactly one slice field, such an array is accepted as that field's value.
func decodeJSON(raw string, out any) error {
	err := json.Unmarshal([]byte(raw), out)
	if err == nil || !strings.HasPrefix(strings.TrimSpace(raw), "[") {
		return err
	}
	t := reflect.TypeOf(out)
	if t == nil || t.Kind() != reflect.Ptr || t.Elem().Kind() != reflect.Struct {
		return err
	}
	name := ""
	for i := 0; i < t.Elem().NumField(); i++ {
		f := t.Elem().Field(i)
		if f.Type.Kind() != reflect.Slice || !f.IsExported() {
			continue
		}
		if name != "" {
			return err // ambiguous
		}
		name = strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" {
			name = f.Name
		}
	}
	if name == "" {
		return err
	}
	if json.Unmarshal([]byte(`{"`+name+`":`+raw+`}`), out) != nil {
		return err
	}
	return nil
}
