package llm

import "strings"

// thinkSplitter routes <think>…</think> spans found inside streamed content
// to the reasoning channel (some local models emit reasoning inline).
type thinkSplitter struct {
	in  bool
	buf string
}

const (
	openTag  = "<think>"
	closeTag = "</think>"
)

// feed consumes a chunk and returns (content, reasoning) parts that are safe to emit.
func (t *thinkSplitter) feed(chunk string) (content, reasoning string) {
	t.buf += chunk
	var c, r strings.Builder
	for {
		tag := openTag
		if t.in {
			tag = closeTag
		}
		i := strings.Index(t.buf, tag)
		if i >= 0 {
			seg := t.buf[:i]
			if t.in {
				r.WriteString(seg)
			} else {
				c.WriteString(seg)
			}
			t.buf = t.buf[i+len(tag):]
			t.in = !t.in
			continue
		}
		// keep a possible partial tag at the end of the buffer
		keep := partialSuffix(t.buf, tag)
		seg := t.buf[:len(t.buf)-keep]
		if t.in {
			r.WriteString(seg)
		} else {
			c.WriteString(seg)
		}
		t.buf = t.buf[len(t.buf)-keep:]
		break
	}
	return c.String(), r.String()
}

// flush emits whatever is left in the buffer.
func (t *thinkSplitter) flush() (content, reasoning string) {
	s := t.buf
	t.buf = ""
	if t.in {
		return "", s
	}
	return s, ""
}

func partialSuffix(s, tag string) int {
	max := len(tag) - 1
	if max > len(s) {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}
