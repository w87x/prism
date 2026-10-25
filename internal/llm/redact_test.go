package llm

import (
	"strings"
	"testing"
)

func TestCloudPromptsCarryNoCredentials(t *testing.T) {
	ms := []Message{{Role: "system", Content: "You are Atlas."}, {Role: "user", Content: "my key is sk-proj-abcdefghijklmnop1234 and magnet:?xt=urn:btih:8f5e7a3c2b1d9e4f6a0c8d7e6f5a4b3c2d1e0f9a"}}
	out := redactMessages(ms)
	if strings.Contains(out[1].Content, "sk-proj") || !strings.Contains(out[1].Content, "btih:8f5e7a3c") {
		t.Fatalf("the key goes, the info-hash stays: %q", out[1].Content)
	}
	if !strings.Contains(ms[1].Content, "sk-proj") {
		t.Fatal("the original messages must be left alone")
	}
	for kind, cloud := range map[string]bool{"openai": true, "codex": true, "openrouter": true, "lmstudio": false, "custom": false} {
		if cloudKind(kind) != cloud {
			t.Errorf("%s: cloud=%v", kind, cloudKind(kind))
		}
	}
}
