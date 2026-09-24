package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"prism/internal/llm"
	"prism/internal/netguard"
	"prism/internal/web"
)

// BookmarkSuggestion is what EnrichBookmark offers back for the user to review before saving — never
// written directly, so a bad fetch or a model hallucination never lands in the bookmark unseen.
type BookmarkSuggestion struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
}

const enrichPrompt = `Given a bookmarked web page's title and text, write one short sentence describing what it is or when it's useful, and 4-8 short search keywords (topics someone would search by, not phrases lifted verbatim from the page). Answer JSON only: {"description":"...","keywords":["...","..."]}`

// EnrichBookmark fetches the page (plain HTTP — the same "cheap first attempt" web_fetch itself uses, no
// browser escalation for a quick bookmark save) and, when a fast model is configured, asks it for a
// description and keywords. Best-effort throughout: a fetch failure is returned as an error (the caller has
// nothing to offer), but a model hiccup after a successful fetch still returns the title alone rather than
// failing the whole suggestion.
func EnrichBookmark(ctx context.Context, d Deps, rawURL string) (BookmarkSuggestion, error) {
	title, text, err := fetchTitleAndText(ctx, d, rawURL)
	if err != nil {
		return BookmarkSuggestion{}, err
	}
	out := BookmarkSuggestion{Title: title}
	if d.LLM == nil || d.LLM.RoleRef(ctx, "fast") == "" {
		return out, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := d.LLM.Complete(rctx, "role:fast", enrichPrompt, "Title: "+title+"\n\n"+text, true)
	if err != nil {
		return out, nil
	}
	var parsed struct {
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
	}
	if jerr := json.Unmarshal([]byte(llm.ExtractJSON(resp)), &parsed); jerr != nil {
		return out, nil
	}
	out.Description, out.Keywords = parsed.Description, parsed.Keywords
	return out, nil
}

func fetchTitleAndText(ctx context.Context, d Deps, rawURL string) (title, text string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", fmt.Errorf("invalid URL %q (http/https only)", rawURL)
	}
	if !d.allowPrivate(ctx) {
		if err := netguard.CheckURL(ctx, u.String()); err != nil {
			return "", "", err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (PRISM bookmark)")
	resp, err := netguard.Client(d.allowPrivate(ctx), 15*time.Second).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("page returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 3<<20))
	if err != nil {
		return "", "", err
	}
	doc, err := web.Parse(string(b))
	if err != nil {
		return "", "", err
	}
	title = web.Title(doc)
	text = web.Markdown(doc, u, false)
	if r := []rune(text); len(r) > 4000 {
		text = string(r[:4000])
	}
	return title, text, nil
}
