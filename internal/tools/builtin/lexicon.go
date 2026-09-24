package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"prism/internal/settings"
	"prism/internal/tools"
)

func registerLexicon(reg *tools.Registry, d Deps) {
	reg.Register(&tools.Tool{
		Name: "lexicon", Category: "language", Risk: tools.RiskRead,
		Description: "Look up / translate a foreign-language word, term or short phrase (Google Cloud Translation when a key is configured, otherwise the fast model with a short definition).",
		Params: tools.Obj("term", tools.Str("term", "word or phrase"), tools.Str("to", "target language code or name (default: user's language)"),
			tools.Str("from", "source language code (auto-detect if empty)")),
		Run: func(ctx context.Context, env *tools.Env, raw json.RawMessage) (string, error) {
			a, err := tools.Decode[struct{ Term, To, From string }](raw)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Term) == "" {
				return "", fmt.Errorf("empty term")
			}
			if a.To == "" {
				g := settings.Load(ctx, d.Settings, settings.KeyGeneral, settings.General{})
				a.To = "en"
				if g.Language != "" {
					a.To = g.Language
				}
			}
			w := settings.Load(ctx, d.Settings, settings.KeyWeb, settings.Web{})
			if w.TranslateKey != "" && len(a.To) <= 5 {
				form := url.Values{"q": {a.Term}, "target": {a.To}, "format": {"text"}, "key": {w.TranslateKey}}
				if a.From != "" {
					form.Set("source", a.From)
				}
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://translation.googleapis.com/language/translate/v2", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
				if err == nil {
					defer resp.Body.Close()
					body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
					var r struct {
						Data struct {
							Translations []struct {
								Text     string `json:"translatedText"`
								Detected string `json:"detectedSourceLanguage"`
							} `json:"translations"`
						} `json:"data"`
					}
					if resp.StatusCode/100 == 2 && json.Unmarshal(body, &r) == nil && len(r.Data.Translations) > 0 {
						t := r.Data.Translations[0]
						return fmt.Sprintf("%s → %s: %s (detected source: %s)", a.Term, a.To, t.Text, t.Detected), nil
					}
				}
			}
			out, err := d.LLM.Complete(ctx, "role:fast", "You are a precise bilingual dictionary. Give the translation, part of speech, a one-line definition and one short example. Be brief.",
				fmt.Sprintf("Term: %s\nTarget language: %s\nSource language: %s", a.Term, a.To, firstNonEmpty(a.From, "auto-detect")), false)
			return out, err
		},
	})
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
