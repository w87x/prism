package textutil

import "testing"

func TestCleanStripsNULAndBadUTF8(t *testing.T) {
	if got := Clean("a\x00b\xffc"); got != "ab�c" {
		t.Fatalf("%q", got)
	}
	if got := Clean("plain ✓ текст"); got != "plain ✓ текст" {
		t.Fatalf("clean text changed: %q", got)
	}
}
