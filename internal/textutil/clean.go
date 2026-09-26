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
