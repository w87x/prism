package llm

import "prism/internal/redact"

// cloudKind: providers whose servers are not the user's own. A prompt carries the whole working context — memory, tool results, files read — and
// a key, token or password that happens to be in it must not travel to a third party (nor be stored in its logs).
func cloudKind(kind string) bool {
	switch kind {
	case "openai", "gemini", "grok", "openrouter", KindCodex:
		return true
	}
	return false
}

// redactMessages returns the messages with every credential in their text replaced; the originals are left alone.
func redactMessages(ms []Message) []Message {
	out := make([]Message, len(ms))
	for i, m := range ms {
		if clean, found := redact.Secrets(m.Content); len(found) > 0 {
			m.Content = clean
		}
		out[i] = m
	}
	return out
}
