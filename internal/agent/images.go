package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"prism/internal/tools"
)

// A markdown image in an answer — ![alt](https://…) — is never rendered by the chat as a remote picture (the
// browser would fetch it by itself, which lets an injected page smuggle data out in the address). Instead the
// engine turns it into something safe: when the address comes from web content this run really read, the picture is
// downloaded on PRISM's side and shown as an artifact; otherwise it stays a plain link.
var mdImageRe = regexp.MustCompile(`!\[([^\]\n]*)\]\((https?://[^)\s]+)\)`)

const maxEmbeddedImages = 4

func (e *Engine) embedWebImages(ctx context.Context, text string, src *tools.Sources, agent string) string {
	if !strings.Contains(text, "![") {
		return text
	}
	fetch, haveFetch := e.Tools.Get("image_fetch")
	n := 0
	return mdImageRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := mdImageRe.FindStringSubmatch(m)
		alt, u := strings.TrimSpace(sub[1]), sub[2]
		link := func() string {
			if alt == "" {
				alt = u
			}
			return "[" + alt + "](" + u + ")"
		}
		if !haveFetch || n >= maxEmbeddedImages || src == nil || !src.HasURL(u) {
			return link()
		}
		args, _ := json.Marshal(map[string]string{"url": u, "caption": alt})
		out, err := fetch.Run(ctx, &tools.Env{Agent: agent, Tainted: true, Sources: src}, args)
		if err != nil {
			return link()
		}
		id := imageMarker.FindStringSubmatch(out)
		if id == nil {
			return link()
		}
		n++
		res := "[image:" + id[1] + "]"
		if alt != "" {
			res += "\n_" + strings.ReplaceAll(alt, "_", " ") + "_"
		}
		return res
	})
}

var imageMarker = regexp.MustCompile(`\[image:(\d+)\]`)
