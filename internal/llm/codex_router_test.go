package llm_test

import (
	"context"
	"os"
	"testing"
	"time"

	"prism/internal/llm"
	"prism/internal/settings"
	"prism/internal/testutil"
)

// The Codex provider as the app uses it: saved like any provider (no URL, no key), its models imported, set as the chat role, called
// through the Router. Runs on the real Codex login: PRISM_CODEX_LIVE=1 go test -run TestCodexThroughTheRouter ./internal/llm -v
func TestCodexThroughTheRouter(t *testing.T) {
	if os.Getenv("PRISM_CODEX_LIVE") != "1" {
		t.Skip("PRISM_CODEX_LIVE not set")
	}
	d := testutil.DB(t)
	st := settings.New(d.Pool)
	r := llm.NewRouter(d.Pool, st)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pid, err := r.Store().SaveProvider(ctx, llm.Provider{Name: "ChatGPT", Kind: llm.KindCodex, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := r.Store().Providers(ctx)
	if len(ps) != 1 || ps[0].BaseURL != llm.CodexBaseURL {
		t.Fatalf("the preset fills in the base URL: %+v", ps)
	}
	found, err := r.Client().ListModels(ctx, ps[0].BaseURL, "")
	if err != nil || len(found) == 0 {
		t.Fatalf("discovery: %v %v", found, err)
	}
	if _, err := r.Store().SaveModel(ctx, llm.Model{Name: "chatgpt", ProviderID: pid, ModelID: found[0].ID, Kind: "chat", ContextWindow: found[0].Context, SupportsTools: found[0].Tools, Vision: found[0].Vision}); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, settings.KeyModelRoles, settings.ModelRoles{Chat: "chatgpt", Fast: "chatgpt"}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Word string `json:"word"`
	}
	if err := r.CompleteJSON(ctx, "role:chat", "You answer with JSON only.", `Return {"word": "prism"}.`, &v); err != nil || v.Word != "prism" {
		t.Fatalf("CompleteJSON through the router: %+v %v", v, err)
	}
	t.Logf("window=%d, answer=%+v", r.Window(ctx, "role:chat"), v)
}
