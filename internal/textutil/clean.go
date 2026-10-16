// Package textutil holds small helpers for text that comes from outside PRISM.
package textutil

import "strings"

// Clean makes text safe to store in Postgres: text columns and jsonb both reject NUL bytes, and invalid UTF-8 is
// refused too. Tool output (a binary file, a web page, an MCP server) can contain either, and one stray byte used to
// fail a whole task ("invalid byte sequence for encoding UTF8: 0x00").
func Clean(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.ToValidUTF8(s, "�")
}

// FirstNonEmpty returns the first argument that is not the empty string.
func FirstNonEmpty(a ...string) string {
	for _, x := range a {
		if x != "" {
			return x
		}
	}
	return ""
}

// Brief collapses whitespace runs to single spaces and cuts the result to n runes (with an ellipsis when it cut).
func Brief(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	return Cut(s, n)
}

// Cut shortens s to n runes (with an ellipsis when it cut), leaving its whitespace alone.
func Cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
